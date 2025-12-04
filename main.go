package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gofiber/fiber/v2"
)

var (
	ctx         = context.Background()
	rateLimiter *RateLimiter
)

// RedisShardManager manages multiple Redis shards for horizontal scaling
type RedisShardManager struct {
	shards []*redis.Client
}

// NewRedisShardManager creates a new shard manager and connects to all Redis instances
func NewRedisShardManager(addresses []string) (*RedisShardManager, error) {
	if len(addresses) == 0 {
		return nil, fmt.Errorf("at least one Redis address is required")
	}

	shards := make([]*redis.Client, len(addresses))
	for i, addr := range addresses {
		client := redis.NewClient(&redis.Options{
			Addr:         addr,
			Password:     "", // no password set
			DB:           0,  // use default DB
			DialTimeout:  5 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		})

		// Test the connection
		_, err := client.Ping(ctx).Result()
		if err != nil {
			log.Printf("ERROR: Critical Redis Error: Connection failure to Redis shard at %s - %v", addr, err)
			return nil, fmt.Errorf("failed to connect to Redis at %s: %w", addr, err)
		}

		shards[i] = client
		fmt.Printf("Successfully connected to Redis shard %d at %s\n", i, addr)
	}

	return &RedisShardManager{
		shards: shards,
	}, nil
}

// GetClient returns the Redis client for the given userID using consistent hashing
func (rsm *RedisShardManager) GetClient(userID string) *redis.Client {
	// Hash the userID to get a consistent value
	hash := fnv.New32a()
	hash.Write([]byte(userID))
	hashValue := hash.Sum32()

	// Use modulo operation to map to a shard
	shardIndex := int(hashValue) % len(rsm.shards)
	return rsm.shards[shardIndex]
}

// RateLimiter represents a distributed rate limiter using Token Bucket algorithm
type RateLimiter struct {
	manager  *RedisShardManager
	rate     float64 // tokens per second
	capacity float64 // maximum bucket capacity
}

// NewRateLimiter creates a new RateLimiter instance
func NewRateLimiter(manager *RedisShardManager, rate, capacity float64) *RateLimiter {
	return &RateLimiter{
		manager:  manager,
		rate:     rate,
		capacity: capacity,
	}
}

// tokenBucketLuaScript is the Lua script for atomic token bucket operations
const tokenBucketLuaScript = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

-- Get current state from Redis hash
local bucket = redis.call('HMGET', key, 'tokens', 'lastRefill')
local tokens = tonumber(bucket[1]) or capacity
local lastRefill = tonumber(bucket[2]) or now

-- Calculate elapsed time in seconds
local elapsed = now - lastRefill

-- Refill tokens based on elapsed time and rate
if elapsed > 0 then
    local tokensToAdd = elapsed * rate
    tokens = math.min(capacity, tokens + tokensToAdd)
end

-- Check if we can consume a token
local allowed = 0
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
end

-- Update the bucket state atomically
redis.call('HMSET', key, 'tokens', tokens, 'lastRefill', now)
redis.call('EXPIRE', key, 3600) -- Expire after 1 hour of inactivity

return {allowed, tokens}
`

// AllowResult contains the result of a rate limit check
type AllowResult struct {
	Allowed   bool
	Remaining float64 // remaining tokens after the check
}

// Allow checks if a request from the given userID should be allowed
// Returns AllowResult with allowed status and remaining tokens, and an error if something went wrong
func (rl *RateLimiter) Allow(userID string) (*AllowResult, error) {
	// Get the appropriate Redis shard for this userID
	client := rl.manager.GetClient(userID)

	// Create a unique key for this user
	key := fmt.Sprintf("ratelimit:%s", userID)

	// Get current timestamp in seconds (with millisecond precision)
	now := float64(time.Now().UnixNano()) / 1e9

	// Execute the Lua script atomically on the selected shard
	script := redis.NewScript(tokenBucketLuaScript)
	result, err := script.Run(ctx, client, []string{key}, rl.rate, rl.capacity, now, 1.0).Result()
	if err != nil {
		log.Printf("ERROR: Critical Redis Error: Lua script execution failure for userID %s - %v. Falling back to Fail-Open Policy.", userID, err)
		return nil, fmt.Errorf("failed to execute rate limit script: %w", err)
	}

	// Parse the result (Lua script returns {allowed, tokens})
	resultArray, ok := result.([]interface{})
	if !ok || len(resultArray) < 2 {
		return nil, fmt.Errorf("unexpected result format from Lua script")
	}

	// Parse allowed status (can be int64 or float64)
	var allowed int64
	switch v := resultArray[0].(type) {
	case int64:
		allowed = v
	case float64:
		allowed = int64(v)
	default:
		return nil, fmt.Errorf("failed to parse allowed status: unexpected type")
	}

	// Parse remaining tokens (can be int64 or float64)
	var remaining float64
	switch v := resultArray[1].(type) {
	case int64:
		remaining = float64(v)
	case float64:
		remaining = v
	default:
		return nil, fmt.Errorf("failed to parse remaining tokens: unexpected type")
	}

	return &AllowResult{
		Allowed:   allowed == 1,
		Remaining: remaining,
	}, nil
}

func initRedisShardManager() *RedisShardManager {
	// Get Redis addresses from environment variable (comma-separated)
	// Default to single Redis instance for backward compatibility
	redisAddrsEnv := os.Getenv("REDIS_ADDRS")
	if redisAddrsEnv == "" {
		// Fallback to single REDIS_ADDR for backward compatibility
		redisAddr := os.Getenv("REDIS_ADDR")
		if redisAddr == "" {
			redisAddr = "localhost:6379"
		}
		redisAddrsEnv = redisAddr
	}

	// Parse comma-separated addresses
	var addresses []string
	parts := strings.Split(redisAddrsEnv, ",")
	for _, part := range parts {
		addr := strings.TrimSpace(part)
		if addr != "" {
			addresses = append(addresses, addr)
		}
	}

	if len(addresses) == 0 {
		addresses = []string{"localhost:6379"}
	}

	manager, err := NewRedisShardManager(addresses)
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize Redis shard manager: %v", err))
	}

	return manager
}

// RateLimitMiddleware creates a Fiber middleware that applies rate limiting
func RateLimitMiddleware(limiter *RateLimiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Extract client identifier (IP address)
		userID := c.IP()

		// Check rate limit
		result, err := limiter.Allow(userID)
		if err != nil {
			// On error, allow the request but log the error (fail-open policy)
			log.Printf("ERROR: Critical Redis Error: Rate limiter execution failure for userID %s - %v. Falling back to Fail-Open Policy.", userID, err)
			return c.Next()
		}

		// Set rate limit headers
		limit := limiter.capacity
		remaining := result.Remaining
		c.Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", limit))
		c.Set("X-RateLimit-Remaining", fmt.Sprintf("%.0f", remaining))

		if !result.Allowed {
			// Calculate retry-after time in seconds
			// When blocked, remaining tokens are what we had before (we didn't consume)
			// We need (1 - remaining) tokens to be refilled
			// At rate tokens/sec, we need (1 - remaining) / rate seconds
			tokensNeeded := 1.0 - result.Remaining
			if tokensNeeded < 0 {
				tokensNeeded = 1.0
			}
			retryAfterSeconds := tokensNeeded / limiter.rate
			// Round up to at least 1 second for practical purposes
			if retryAfterSeconds < 1.0 {
				retryAfterSeconds = 1.0
			}
			retryAfter := int(retryAfterSeconds)

			c.Set("X-RateLimit-Retry-After", fmt.Sprintf("%d", retryAfter))

			// Log blocked request with structured information
			log.Printf("INFO: Decision: BLOCKED (429) - userID: %s, Reason: Rate limit exceeded, Retry-After: %d seconds", userID, retryAfter)

			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":   "Rate limit exceeded",
				"message": "Too many requests. Please try again later.",
			})
		}

		// Log allowed request with structured information
		log.Printf("INFO: Decision: ALLOWED - userID: %s, Remaining: %.2f, Limit: %.0f", userID, remaining, limit)

		// Request allowed, proceed to next handler
		return c.Next()
	}
}

func main() {
	// Initialize Redis shard manager
	shardManager := initRedisShardManager()

	// Initialize Rate Limiter with 5 req/sec rate and capacity of 10
	rateLimiter = NewRateLimiter(shardManager, 5.0, 10.0)

	// Create Fiber app
	app := fiber.New(fiber.Config{
		AppName: "Velocity Rate Limiter",
	})

	// Health check endpoint
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"service": "velocity-rate-limiter",
		})
	})

	// Basic root endpoint
	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"message": "Velocity Rate Limiter API",
		})
	})

	// Rate limited endpoint with middleware
	app.Get("/api/resource", RateLimitMiddleware(rateLimiter), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"message": "Resource accessed successfully",
			"data":    "This is a protected resource",
		})
	})

	// Start server on port 3000
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	fmt.Printf("Server starting on port %s\n", port)
	if err := app.Listen(":" + port); err != nil {
		panic(fmt.Sprintf("Failed to start server: %v", err))
	}
}

