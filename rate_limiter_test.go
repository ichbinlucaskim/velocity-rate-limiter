package main

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
)

var testCtx = context.Background()

// setupTestRateLimiter creates a rate limiter for testing with a real Redis connection
func setupTestRateLimiter(rate, capacity float64) (*RateLimiter, func(), error) {
	// Get Redis address from environment or use default
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	// Create shard manager with single Redis instance for testing
	manager, err := NewRedisShardManager([]string{redisAddr})
	if err != nil {
		return nil, nil, err
	}

	// Create rate limiter
	limiter := NewRateLimiter(manager, rate, capacity)

	// Cleanup function to clear test data
	cleanup := func() {
		// Clear test keys from all shards
		// Since we don't know which shard each test user is on, we clear from all shards
		for _, shard := range manager.shards {
			// Get all test keys from this shard
			keys, err := shard.Keys(testCtx, "ratelimit:test_*").Result()
			if err == nil && len(keys) > 0 {
				shard.Del(testCtx, keys...)
			}
		}
	}

	return limiter, cleanup, nil
}

// TestRateLimitConcurrency tests that the rate limiter correctly handles concurrent requests
// and ensures atomicity prevents token overconsumption
func TestRateLimitConcurrency(t *testing.T) {
	// Setup: Capacity 10, very high rate (1000 req/sec) to focus on capacity constraint
	limiter, cleanup, err := setupTestRateLimiter(1000.0, 10.0)
	if err != nil {
		t.Fatalf("Failed to setup test rate limiter: %v", err)
	}
	defer cleanup()

	// Use the same userID for all concurrent requests
	userID := "test_user_concurrent"

	// Clear any existing state for this user
	client := limiter.manager.GetClient(userID)
	key := "ratelimit:" + userID
	client.Del(testCtx, key)

	// Number of concurrent goroutines
	numGoroutines := 100
	capacity := 10

	// Use atomic counter to safely count allowed requests
	var allowedCount int64

	// Use WaitGroup to wait for all goroutines to complete
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Launch all goroutines simultaneously
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			result, err := limiter.Allow(userID)
			if err != nil {
				t.Errorf("Error calling Allow: %v", err)
				return
			}

			if result.Allowed {
				atomic.AddInt64(&allowedCount, 1)
			}
		}()
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Verify that exactly capacity number of requests were allowed
	finalCount := int(atomic.LoadInt64(&allowedCount))
	if finalCount != capacity {
		t.Errorf("Expected exactly %d allowed requests, but got %d", capacity, finalCount)
	}

	t.Logf("Concurrency test passed: %d out of %d requests were allowed (expected %d)", finalCount, numGoroutines, capacity)
}

// TestRateLimitRefill tests that tokens are correctly refilled over time
func TestRateLimitRefill(t *testing.T) {
	// Setup: Rate 5 req/sec, Capacity 10
	rate := 5.0
	capacity := 10.0
	limiter, cleanup, err := setupTestRateLimiter(rate, capacity)
	if err != nil {
		t.Fatalf("Failed to setup test rate limiter: %v", err)
	}
	defer cleanup()

	userID := "test_user_refill"

	// Clear any existing state for this user
	client := limiter.manager.GetClient(userID)
	key := "ratelimit:" + userID
	client.Del(testCtx, key)

	// Step 1: Consume all tokens (10 requests)
	t.Log("Step 1: Consuming all tokens...")
	for i := 0; i < int(capacity); i++ {
		result, err := limiter.Allow(userID)
		if err != nil {
			t.Fatalf("Error calling Allow: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Request %d should have been allowed (initial capacity: %.0f)", i+1, capacity)
		}
	}

	// Step 2: Verify that no more requests are allowed
	t.Log("Step 2: Verifying capacity exhausted...")
	result, err := limiter.Allow(userID)
	if err != nil {
		t.Fatalf("Error calling Allow: %v", err)
	}
	if result.Allowed {
		t.Error("Request should have been blocked after consuming all tokens")
	}

	// Step 3: Wait for tokens to refill (1 second should refill 5 tokens at 5 req/sec)
	waitTime := 1 * time.Second
	t.Logf("Step 3: Waiting %v for tokens to refill...", waitTime)
	time.Sleep(waitTime)

	// Step 4: Verify that expected number of tokens have been refilled
	// At 5 req/sec, after 1 second we should have 5 tokens
	expectedRefilled := int(rate) // 5 tokens
	t.Logf("Step 4: Verifying %d tokens have been refilled...", expectedRefilled)

	allowedCount := 0
	for i := 0; i < expectedRefilled; i++ {
		result, err := limiter.Allow(userID)
		if err != nil {
			t.Fatalf("Error calling Allow: %v", err)
		}
		if result.Allowed {
			allowedCount++
		} else {
			t.Errorf("Request %d should have been allowed after refill (expected %d tokens)", i+1, expectedRefilled)
		}
	}

	if allowedCount != expectedRefilled {
		t.Errorf("Expected %d requests to be allowed after refill, but got %d", expectedRefilled, allowedCount)
	}

	// Step 5: Verify that after consuming refilled tokens, no more are available
	result, err = limiter.Allow(userID)
	if err != nil {
		t.Fatalf("Error calling Allow: %v", err)
	}
	if result.Allowed {
		t.Error("Request should have been blocked after consuming all refilled tokens")
	}

	t.Logf("Refill test passed: %d tokens were correctly refilled after %v", allowedCount, waitTime)
}

