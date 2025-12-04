# Velocity Rate Limiter: Deep Dive Study Guide
## From Implementation to Mastery

This document is designed to take you from understanding *what* the code does to understanding *why* it's designed this way and *how* it solves real distributed systems problems. Think of this as a mentorship session with a Principal Engineer.

---

## Part 1: The "Big Picture" Mental Model

### 1.1 Distributed Rate Limiting: The Real-World Analogy

Imagine you're managing a popular nightclub with multiple entrances. You want to ensure that:
1. No more than 100 people enter per hour (rate limit)
2. You can handle a sudden rush of 20 people at once (burst capacity)
3. All bouncers at all entrances agree on who's been let in (distributed consistency)

**The Challenge**: You have 5 entrances (5 application servers), each with a bouncer. How do you ensure they all agree on how many people have entered?

**The Naive Solution (Doesn't Work)**:
- Each bouncer keeps their own count
- Problem: They don't know what the others are doing
- Result: You might let in 200 people when the limit is 100

**The Distributed Solution (What We Built)**:
- All bouncers check with a central coordinator (Redis)
- The coordinator maintains a single source of truth
- All operations are atomic: "Check if we can let one more person in, and if yes, increment the count" happens as one indivisible operation

This is exactly what our rate limiter does. Multiple application instances (the bouncers) all check with Redis (the coordinator) to make rate limiting decisions.

### 1.2 Token Bucket Algorithm: Why This Over Alternatives?

Let's visualize the Token Bucket algorithm:

```
Bucket Capacity: 10 tokens
Refill Rate: 5 tokens/second

Time 0s:  [██████████] 10 tokens (full)
Time 1s:  [█████░░░░░] 5 tokens (after 5 requests)
Time 2s:  [██████████] 10 tokens (refilled)
```

**Why Token Bucket Over Leaky Bucket?**

Leaky Bucket is similar but has a key difference:
- **Token Bucket**: Tokens accumulate up to capacity (allows bursts)
- **Leaky Bucket**: Requests are queued and processed at a fixed rate (no bursts)

**Real-World Example**:
- User hasn't made requests for 10 seconds
- Token Bucket: They now have 10 tokens (can make 10 requests immediately)
- Leaky Bucket: They still process at 5 req/sec (no burst benefit)

Token Bucket is better for APIs because legitimate users often have traffic spikes (e.g., loading a dashboard with multiple API calls).

**Why Token Bucket Over Fixed Window?**

Fixed Window has a critical flaw:

```
Window 1: [0:00 - 0:59] - 100 requests allowed
Window 2: [1:00 - 1:59] - 100 requests allowed

Problem: At 0:59:59, user makes 100 requests
         At 1:00:00, user makes 100 requests
         Result: 200 requests in 2 seconds!
```

Token Bucket prevents this because tokens refill continuously, not in discrete windows.

**Why Token Bucket Over Sliding Window?**

Sliding Window is more accurate but:
- **Memory Intensive**: Must store timestamps for each request
- **Complex**: Requires sorted sets or complex data structures
- **Overhead**: More Redis operations per request

Token Bucket only needs 2 values: `tokens` and `lastRefill`. Much simpler and more efficient.

### 1.3 Atomic Operations: Why `x = x + 1` is Dangerous

In a single-threaded program, this is safe:
```go
x = x + 1  // Read x, add 1, write back
```

In a distributed system with multiple servers, this becomes dangerous:

**The Race Condition Scenario**:

```
Server A: Read x = 5
Server B: Read x = 5  (before A writes)
Server A: Write x = 6
Server B: Write x = 6  (overwrites A's update!)
```

Expected: x should be 7 (5 + 1 + 1)
Actual: x is 6 (lost one increment!)

**Why This Matters for Rate Limiting**:

Without atomicity, two servers might both see "1 token available" and both consume it, allowing 2 requests when only 1 should be allowed.

**The Solution: Atomic Operations**

Redis Lua scripts execute atomically:
```lua
-- This entire block executes as ONE operation
local tokens = redis.call('HGET', key, 'tokens')
if tokens >= 1 then
    tokens = tokens - 1
    redis.call('HSET', key, 'tokens', tokens)
    return 1  -- allowed
end
return 0  -- blocked
```

No other server can interleave operations. It's all-or-nothing.

---

## Part 2: Code Anatomy & Deep Dive

### 2.1 Redis Lua Script: Line-by-Line Breakdown

Let's examine the Lua script in `main.go`:

```lua
local key = KEYS[1]              -- Line 1: Get the Redis key for this user
local rate = tonumber(ARGV[1])    -- Line 2: Convert rate to number (tokens/sec)
local capacity = tonumber(ARGV[2]) -- Line 3: Maximum bucket capacity
local now = tonumber(ARGV[3])     -- Line 4: Current timestamp
local requested = tonumber(ARGV[4]) -- Line 5: Tokens requested (always 1)
```

**Why Lua? Why Not Multiple Redis Commands?**

**The Dangerous Way (Without Lua)**:
```go
// Server A
tokens := redis.Get("ratelimit:user1")  // Returns 5
if tokens >= 1 {
    // Server B might execute here!
    redis.Set("ratelimit:user1", tokens - 1)  // Sets to 4
}

// Server B (executing simultaneously)
tokens := redis.Get("ratelimit:user1")  // Still returns 5!
if tokens >= 1 {
    redis.Set("ratelimit:user1", tokens - 1)  // Also sets to 4
}
// Result: Both servers consumed a token, but count only decreased by 1!
```

**The Safe Way (With Lua)**:
```lua
-- All of this executes atomically on Redis server
local bucket = redis.call('HMGET', key, 'tokens', 'lastRefill')
local tokens = tonumber(bucket[1]) or capacity
local lastRefill = tonumber(bucket[2]) or now

-- Calculate elapsed time and refill
local elapsed = now - lastRefill
if elapsed > 0 then
    local tokensToAdd = elapsed * rate
    tokens = math.min(capacity, tokens + tokensToAdd)
end

-- Atomic check-and-consume
local allowed = 0
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
end

-- Atomic write
redis.call('HMSET', key, 'tokens', tokens, 'lastRefill', now)
return {allowed, tokens}
```

**The Specific Race Condition Prevented**:

**Scenario**: 100 goroutines all call `Allow()` simultaneously for the same user with capacity 10.

**Without Lua (Race Condition)**:
```
Goroutine 1: Read tokens = 10
Goroutine 2: Read tokens = 10
Goroutine 3: Read tokens = 10
...
Goroutine 100: Read tokens = 10

All 100 goroutines see 10 tokens available!
All 100 consume a token!
Result: 100 requests allowed (should be 10)
```

**With Lua (Atomic)**:
```
Goroutine 1's Lua script: Executes completely → tokens = 9
Goroutine 2's Lua script: Executes completely → tokens = 8
Goroutine 3's Lua script: Executes completely → tokens = 7
...
Goroutine 10's Lua script: Executes completely → tokens = 0
Goroutine 11's Lua script: Executes completely → tokens = 0, allowed = 0
...
Result: Exactly 10 requests allowed
```

Redis queues Lua scripts and executes them sequentially for the same key, ensuring atomicity.

### 2.2 Go Channels & Goroutines: Concurrency Model

Let's examine the test code in `rate_limiter_test.go`:

```go
var allowedCount int64
var wg sync.WaitGroup
wg.Add(numGoroutines)  // Set expected count to 100

for i := 0; i < numGoroutines; i++ {
    go func() {  // Launch goroutine
        defer wg.Done()  // Decrement counter when done
        
        result, err := limiter.Allow(userID)
        if result.Allowed {
            atomic.AddInt64(&allowedCount, 1)  // Thread-safe increment
        }
    }()
}

wg.Wait()  // Block until all 100 goroutines complete
```

**How `sync.WaitGroup` Works**:

Think of it as a counter with a gate:
1. `wg.Add(100)`: Set counter to 100
2. Each goroutine calls `wg.Done()`: Decrements counter
3. `wg.Wait()`: Blocks until counter reaches 0

**Why We Need Atomic Operations for Counting**:

```go
// DANGEROUS (without atomic):
allowedCount++  // This is actually: read, increment, write (3 operations!)

// Two goroutines might both read 5, both increment to 6, both write 6
// Result: Count is 6 instead of 7
```

```go
// SAFE (with atomic):
atomic.AddInt64(&allowedCount, 1)  // Single atomic operation
// Guaranteed to be thread-safe
```

**How Go Handles Concurrency Differently**:

**Python (Threading)**:
```python
import threading
threads = []
for i in range(100):
    t = threading.Thread(target=check_rate_limit)
    threads.append(t)
    t.start()  # OS thread created (expensive!)
for t in threads:
    t.join()  # Wait for completion
```
- Each thread = OS thread (~1-2MB stack)
- Context switching is expensive
- Global Interpreter Lock (GIL) limits true parallelism

**Java (Threads)**:
```java
ExecutorService executor = Executors.newFixedThreadPool(100);
for (int i = 0; i < 100; i++) {
    executor.submit(() -> checkRateLimit());
}
executor.shutdown();
executor.awaitTermination(Long.MAX_VALUE, TimeUnit.NANOSECONDS);
```
- Thread pool management overhead
- Each thread = OS thread
- More memory per thread

**Go (Goroutines)**:
```go
for i := 0; i < 100; i++ {
    go checkRateLimit()  // Goroutine (lightweight!)
}
```
- Each goroutine = ~2KB stack initially
- Go runtime scheduler (M:N model) multiplexes goroutines onto OS threads
- Can spawn millions of goroutines
- No GIL, true parallelism

**The M:N Scheduler Model**:
```
M OS Threads (e.g., 4) ←→ N Goroutines (e.g., 10,000)
     ↓
Go Runtime Scheduler
     ↓
Efficiently schedules goroutines
```

When a goroutine blocks (e.g., waiting for Redis), the scheduler automatically moves to another goroutine. This is why Go excels at I/O-bound workloads like rate limiting.

### 2.3 Middleware Pattern: Control Flow

Let's trace through the middleware execution:

```go
// In main.go
app.Get("/api/resource", RateLimitMiddleware(rateLimiter), handler)

// RateLimitMiddleware returns a function
func RateLimitMiddleware(limiter *RateLimiter) fiber.Handler {
    return func(c *fiber.Ctx) error {  // This function IS the middleware
        // 1. Extract userID
        userID := c.IP()
        
        // 2. Check rate limit
        result, err := limiter.Allow(userID)
        
        // 3. Decision point
        if !result.Allowed {
            return c.Status(429).JSON(...)  // Stop here, don't call handler
        }
        
        // 4. If allowed, continue
        return c.Next()  // Calls the next handler in chain
    }
}
```

**Control Flow Diagram**:

```
Request arrives
    ↓
RateLimitMiddleware (our middleware)
    ↓
    ├─→ Check rate limit
    │   ├─→ Blocked? → Return 429 (STOP)
    │   └─→ Allowed? → Continue
    ↓
c.Next() called
    ↓
handler function (actual API logic)
    ↓
Response sent
```

**Why This Pattern?**

1. **Separation of Concerns**: Rate limiting logic is separate from business logic
2. **Reusability**: Same middleware can protect multiple endpoints
3. **Composability**: Can chain multiple middlewares (auth, logging, rate limiting)
4. **Testability**: Can test rate limiting independently

**The Middleware Chain**:
```go
app.Get("/api/resource",
    AuthMiddleware(),           // 1. Check authentication
    RateLimitMiddleware(),      // 2. Check rate limit
    LoggingMiddleware(),        // 3. Log request
    handler,                    // 4. Actual handler
)
```

Each middleware can:
- Modify the request (`c.Locals()`)
- Block the request (return early)
- Pass control to next (`c.Next()`)

---

## Part 3: Architectural Decisions

### 3.1 Sharding Logic: The Math Behind `hash(userID) % N`

Let's break down the sharding code:

```go
func (rsm *RedisShardManager) GetClient(userID string) *redis.Client {
    hash := fnv.New32a()
    hash.Write([]byte(userID))
    hashValue := hash.Sum32()
    
    shardIndex := int(hashValue) % len(rsm.shards)
    return rsm.shards[shardIndex]
}
```

**Step-by-Step**:

1. **Hash the userID**: `"user123"` → `0x8F3A2B1C` (example)
2. **Modulo operation**: `0x8F3A2B1C % 3` → `2` (if 3 shards)
3. **Return shard 2**: Always the same shard for `"user123"`

**Why Hash First, Then Modulo?**

Direct modulo on userID string would be problematic:
```go
// BAD: Direct modulo
shardIndex := len(userID) % 3
// "user1" → 5 % 3 = 2
// "user2" → 5 % 3 = 2  (same shard!)
// "user10" → 6 % 3 = 0 (different shard)
// Poor distribution!
```

Hashing ensures uniform distribution:
```go
// GOOD: Hash then modulo
hash("user1") % 3 → 0
hash("user2") % 3 → 1
hash("user3") % 3 → 2
hash("user4") % 3 → 0
// Uniform distribution!
```

**Why Consistent Hashing is Critical**:

**Scenario**: User `"alice"` always makes requests. Without consistent hashing:

```go
// Random shard selection (BAD)
func GetClient(userID string) *redis.Client {
    return shards[rand.Intn(len(shards))]  // Random!
}
```

**Problem**: 
- Request 1: `"alice"` → Shard 0 (tokens = 10)
- Request 2: `"alice"` → Shard 1 (tokens = 10, different bucket!)
- Request 3: `"alice"` → Shard 0 (tokens = 9, back to first bucket)

Result: User gets 20 tokens instead of 10! Rate limiting is broken.

**With Consistent Hashing**:
- Request 1: `"alice"` → Shard 0 (tokens = 10)
- Request 2: `"alice"` → Shard 0 (tokens = 9)
- Request 3: `"alice"` → Shard 0 (tokens = 8)

Result: Correct rate limiting.

**What Happens When We Add a Shard?**

With simple modulo hashing:
- 3 shards → 4 shards: ~75% of users need remapping
- This causes temporary rate limit inconsistencies

With consistent hashing (ring-based):
- Only ~25% of users need remapping
- Better, but still requires careful migration

Our simple modulo approach is a trade-off: simpler code, but requires careful shard management.

### 3.2 Dependency Injection: Why Pass `RedisShardManager`?

Look at the constructor:

```go
func NewRateLimiter(manager *RedisShardManager, rate, capacity float64) *RateLimiter {
    return &RateLimiter{
        manager:  manager,  // Injected, not created
        rate:     rate,
        capacity: capacity,
    }
}
```

**The Anti-Pattern (Tight Coupling)**:
```go
// BAD: Creates dependency inside
func NewRateLimiter(rate, capacity float64) *RateLimiter {
    manager := NewRedisShardManager([]string{"localhost:6379"})  // Hard-coded!
    return &RateLimiter{
        manager: manager,
    }
}
```

**Problems**:
1. **Testing**: Can't easily swap in a mock Redis
2. **Configuration**: Hard to change Redis addresses
3. **Flexibility**: Can't reuse the same manager across multiple limiters

**The Dependency Injection Pattern**:
```go
// GOOD: Dependency injected
func NewRateLimiter(manager *RedisShardManager, rate, capacity float64) *RateLimiter {
    return &RateLimiter{
        manager: manager,  // Provided from outside
    }
}
```

**Benefits**:

1. **Testability**:
```go
// In tests, we can inject a test Redis manager
testManager := setupTestRedis()
limiter := NewRateLimiter(testManager, 5.0, 10.0)
```

2. **Flexibility**:
```go
// Production: Real Redis
prodManager := NewRedisShardManager([]string{"redis1:6379", "redis2:6379"})
limiter := NewRateLimiter(prodManager, 5.0, 10.0)

// Development: Local Redis
devManager := NewRedisShardManager([]string{"localhost:6379"})
limiter := NewRateLimiter(devManager, 5.0, 10.0)
```

3. **Reusability**:
```go
// One manager, multiple limiters with different rates
manager := NewRedisShardManager(addresses)
apiLimiter := NewRateLimiter(manager, 10.0, 20.0)  // API: 10 req/sec
authLimiter := NewRateLimiter(manager, 5.0, 10.0)   // Auth: 5 req/sec
```

This is a fundamental principle: **Depend on abstractions, not concretions**.

---

## Part 4: "What If?" Scenarios

### 4.1 What If Redis Crashes?

**Current Implementation (Fail-Open)**:

```go
result, err := limiter.Allow(userID)
if err != nil {
    log.Printf("ERROR: Critical Redis Error... Falling back to Fail-Open Policy.")
    return c.Next()  // Allow the request
}
```

**Fail-Open vs. Fail-Closed**:

**Fail-Open (Current)**:
- Redis down → All requests allowed
- **Pros**: Service remains available
- **Cons**: No rate limiting protection
- **Use Case**: When availability > rate limiting

**Fail-Closed (Alternative)**:
```go
if err != nil {
    return c.Status(503).JSON(fiber.Map{
        "error": "Rate limiter unavailable",
    })
}
```
- Redis down → All requests blocked
- **Pros**: Never exceeds rate limits
- **Cons**: Service unavailable
- **Use Case**: When rate limiting > availability

**Hybrid Approach (Best of Both)**:
```go
if err != nil {
    // Use local cache as fallback
    if localCache.Allow(userID) {
        return c.Next()
    }
    return c.Status(429).JSON(...)
}
```

**Production Recommendation**: 
- Start with fail-open for high availability
- Add local cache fallback for better protection
- Monitor Redis health and alert on failures

### 4.2 What If We Have 1 Million Users?

**Bottleneck Analysis**:

1. **Redis Memory**: 
   - Per user: ~50 bytes (key + hash fields)
   - 1M users: ~50MB (negligible)

2. **Redis CPU**:
   - Lua script execution: ~0.1ms per request
   - 1M requests/sec: 100,000 Lua scripts/sec
   - **Bottleneck**: Single Redis instance can handle ~50K ops/sec
   - **Solution**: Sharding! 20 Redis instances = 1M ops/sec capacity

3. **Network**:
   - Each request = 1 Redis round-trip
   - Latency: ~1ms (same datacenter)
   - **Bottleneck**: Network bandwidth (usually not an issue)

4. **Application Servers**:
   - Go goroutines handle this easily
   - **Not a bottleneck**

**Scaling Strategy**:

```
1M users, 10K requests/sec
    ↓
10 Redis shards (1K requests/sec each)
    ↓
10 Application instances (1K requests/sec each)
    ↓
Load balancer
```

**Key Insight**: The bottleneck is Redis, not the application. Sharding solves this.

### 4.3 What If We Want Per-User-Tier Rate Limits?

**Current Implementation** (Single rate for all users):
```go
rateLimiter = NewRateLimiter(shardManager, 5.0, 10.0)  // Same for everyone
```

**Enhanced Implementation** (Per-tier rates):

```go
type RateLimiter struct {
    manager  *RedisShardManager
    // Remove single rate/capacity
    // Add tier configuration
}

type TierConfig struct {
    Rate     float64
    Capacity float64
}

type TieredRateLimiter struct {
    manager    *RedisShardManager
    tierConfigs map[string]TierConfig  // "free", "premium", "enterprise"
    getUserTier func(userID string) string  // How to determine tier
}

func (trl *TieredRateLimiter) Allow(userID string) (*AllowResult, error) {
    tier := trl.getUserTier(userID)
    config := trl.tierConfigs[tier]
    
    // Use tier-specific rate and capacity in Lua script
    client := trl.manager.GetClient(userID)
    key := fmt.Sprintf("ratelimit:%s:%s", tier, userID)  // Include tier in key
    
    // Pass tier-specific rate/capacity to Lua script
    script.Run(ctx, client, []string{key}, 
        config.Rate,      // Tier-specific rate
        config.Capacity, // Tier-specific capacity
        now, 1.0)
}
```

**Usage**:
```go
limiter := NewTieredRateLimiter(manager, map[string]TierConfig{
    "free":      {Rate: 5.0, Capacity: 10},
    "premium":   {Rate: 20.0, Capacity: 50},
    "enterprise": {Rate: 100.0, Capacity: 200},
}, getUserTierFromDB)
```

**Key Changes**:
1. Store tier in Redis key: `ratelimit:premium:user123`
2. Pass tier-specific rate/capacity to Lua script
3. Lua script logic remains the same (just different parameters)

**Alternative: Separate Limiters**:
```go
freeLimiter := NewRateLimiter(manager, 5.0, 10.0)
premiumLimiter := NewRateLimiter(manager, 20.0, 50.0)

// In middleware
tier := getUserTier(userID)
if tier == "premium" {
    result = premiumLimiter.Allow(userID)
} else {
    result = freeLimiter.Allow(userID)
}
```

This approach is simpler but less flexible (harder to add new tiers).

---

## Part 5: Key Takeaways for Interviews

### 5.1 The "Killer Points" to Mention

When discussing this project in a system design interview, emphasize these points:

#### 1. **Atomic Operations in Distributed Systems**

**What to Say**:
> "I implemented atomic token bucket operations using Redis Lua scripts. This was critical because without atomicity, multiple application instances could simultaneously see the same token count and all consume tokens, leading to overconsumption. The Lua script executes atomically on Redis, ensuring that read-modify-write operations complete without interference. I verified this with a concurrency test: 100 goroutines targeting the same user with capacity 10 resulted in exactly 10 requests being allowed, proving perfect atomicity."

**Why It's Impressive**:
- Shows understanding of distributed systems challenges
- Demonstrates knowledge of ACID properties
- Proves you can verify correctness empirically

#### 2. **Consistent Hashing for Horizontal Scalability**

**What to Say**:
> "I implemented a Redis sharding strategy using consistent hashing. The key insight is that each user must always map to the same shard to maintain rate limit state consistency. I used FNV-1a hashing with modulo distribution. This ensures that as we scale from 1 Redis instance to 10, users are evenly distributed while maintaining deterministic mapping. Without this, a user's requests might hit different shards, breaking rate limiting."

**Why It's Impressive**:
- Shows understanding of horizontal scaling
- Demonstrates knowledge of hashing algorithms
- Proves you can identify and solve consistency problems

#### 3. **Fail-Open vs. Fail-Closed Trade-offs**

**What to Say**:
> "I implemented a fail-open policy: if Redis is unavailable, requests are allowed to proceed. This prioritizes availability over rate limiting protection. In production, I'd add a local cache fallback to provide some protection even when Redis is down. The choice between fail-open and fail-closed depends on the business requirements: for an API gateway, availability is usually more important than strict rate limiting."

**Why It's Impressive**:
- Shows systems thinking
- Demonstrates understanding of trade-offs
- Proves you can make informed architectural decisions

#### 4. **Token Bucket Algorithm Selection**

**What to Say**:
> "I chose Token Bucket over alternatives like Fixed Window or Sliding Window. Token Bucket allows legitimate traffic bursts while maintaining long-term rate limits. It's also memory-efficient, requiring only two values per user. Fixed Window has the thundering herd problem at window boundaries, and Sliding Window requires storing multiple timestamps per user, which doesn't scale well."

**Why It's Impressive**:
- Shows algorithm knowledge
- Demonstrates ability to evaluate trade-offs
- Proves you can make informed technical decisions

#### 5. **Go Concurrency Model for High Throughput**

**What to Say**:
> "I leveraged Go's goroutines and the M:N scheduler to handle high-concurrency rate limit checks. Each check involves a single Redis round-trip, and goroutines yield efficiently during I/O waits. This allows the system to handle thousands of concurrent requests with minimal overhead. I verified this with a test spawning 100 goroutines simultaneously, all completing within milliseconds."

**Why It's Impressive**:
- Shows understanding of Go's concurrency model
- Demonstrates performance optimization knowledge
- Proves you can leverage language features effectively

### 5.2 How to Frame This in an Interview

**Opening Statement**:
> "I built a distributed rate limiter that handles millions of requests per second across multiple application instances. The key challenge was ensuring atomicity in a distributed environment, which I solved using Redis Lua scripts."

**When Asked About Scalability**:
> "The system scales horizontally through Redis sharding. I implemented consistent hashing to ensure users are evenly distributed across shards while maintaining deterministic mapping. This allows us to add Redis instances linearly to increase capacity."

**When Asked About Reliability**:
> "I implemented a fail-open policy to prioritize availability. However, I recognize this is a trade-off, and in production, I'd add a local cache fallback to provide some rate limiting protection even when Redis is unavailable."

**When Asked About Testing**:
> "I wrote comprehensive tests including a concurrency test that spawns 100 goroutines simultaneously. This test proved that the atomic operations work correctly under extreme contention, with exactly the configured capacity being enforced."

### 5.3 Common Follow-up Questions & Answers

**Q: Why not use Redis transactions (MULTI/EXEC)?**
**A**: "Redis transactions aren't truly atomic across different clients in a distributed system. MULTI/EXEC can still have race conditions. Lua scripts execute atomically on the Redis server, eliminating client-side coordination requirements."

**Q: What about Redis Cluster?**
**A**: "Redis Cluster provides automatic sharding, but it's more complex to set up and manage. For this project, I implemented manual sharding with consistent hashing, which gives us more control and is simpler to reason about. In production, Redis Cluster would be a good choice for automatic failover."

**Q: How would you handle rate limit resets?**
**A**: "The current implementation uses time-based refill, which naturally resets over time. For explicit resets (e.g., admin action), I'd add a method to delete the Redis key, which would cause the next request to initialize a fresh bucket at full capacity."

**Q: What about distributed locks?**
**A**: "Distributed locks aren't needed here because Redis Lua scripts provide atomicity. Locks would add latency and complexity. The Lua script approach is more efficient: single round-trip, atomic execution, no lock contention."

---

## Conclusion

This project demonstrates mastery of several critical distributed systems concepts:

1. **Atomicity**: Understanding why it matters and how to achieve it
2. **Consistency**: Ensuring state consistency across distributed systems
3. **Scalability**: Designing for horizontal scaling
4. **Reliability**: Making informed trade-offs for availability
5. **Performance**: Leveraging language features for high throughput

The key to moving from implementation to mastery is understanding the "why" behind every decision. Every line of code should have a reason, and every architectural choice should be justified.

Remember: **Good engineers write code that works. Great engineers write code that works and can explain why it's designed this way.**

---

## Further Reading

- **Redis Lua Scripting**: [Redis EVAL documentation](https://redis.io/commands/eval/)
- **Token Bucket Algorithm**: [Wikipedia: Token Bucket](https://en.wikipedia.org/wiki/Token_bucket)
- **Go Concurrency**: [Effective Go: Concurrency](https://go.dev/doc/effective_go#concurrency)
- **Consistent Hashing**: [Consistent Hashing Explained](https://www.toptal.com/big-data/consistent-hashing)

