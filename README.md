# Velocity: Distributed High-Performance API Rate Limiter Backend

## Executive Summary

Velocity is a production-grade, distributed API rate limiting system designed to protect downstream infrastructure from traffic surges and abuse. The system implements the Token Bucket algorithm with atomic operations guaranteed through Redis Lua scripts, ensuring accurate rate limiting across multiple distributed application instances. The core value proposition is ensuring system stability via distributed concurrency control, enabling horizontal scalability while maintaining strict rate limit enforcement.

The system is architected as a microservices component that can be deployed as middleware within Go web applications, providing transparent rate limiting with minimal performance overhead. Through empirical testing, the system has demonstrated perfect atomicity under extreme concurrency conditions, validating its suitability for high-throughput production environments.

---

## Architecture & Technology Stack

The Velocity rate limiter is built using the following technology stack:

| Component | Technology | Role |
|-----------|-----------|------|
| **Language** | Go 1.21+ | High-performance, concurrent request processing |
| **Web Framework** | Fiber v2 | HTTP middleware integration and request handling |
| **State Store** | Redis 7+ | Distributed token bucket state management |
| **Atomic Operations** | Redis Lua Scripting | Enforcing atomic token operations (ACID compliance) |
| **Containerization** | Docker & Docker Compose | Deployment and environment management |
| **Sharding** | Consistent Hashing (FNV-1a) | Horizontal scalability across Redis instances |

### Component Roles

- **Go**: Provides high-concurrency processing through goroutines, enabling the system to handle thousands of concurrent rate limit checks with minimal overhead.

- **Fiber**: A high-performance HTTP framework that integrates the rate limiter as middleware, providing seamless request interception and response generation.

- **Redis**: Serves as the distributed state store, maintaining token bucket state (token count and last refill timestamp) for each user across all application instances.

- **Redis Lua Scripting**: Executes token bucket operations atomically on the Redis server, eliminating distributed race conditions and ensuring ACID-compliant state updates.

- **Docker & Docker Compose**: Enables consistent deployment across development, staging, and production environments with minimal configuration overhead.

---

## Key Engineering Features

### Atomic Distributed State Management

The system implements the Token Bucket algorithm using custom Redis Lua scripts to eliminate race conditions and ensure ACID properties. The critical challenge in distributed rate limiting is maintaining consistency when multiple application instances simultaneously check and update the same user's token bucket.

**Solution**: All token bucket operations (read current state, calculate refill, check availability, consume token, update state) are executed atomically within a single Redis Lua script. This single-call execution model eliminates the distributed race condition inherent in multi-step Redis transactions.

**Verification**: Empirical testing with 100 concurrent goroutines targeting the same user demonstrates that exactly the configured capacity (10 tokens) is consumed, proving perfect atomicity under maximal contention.

### Horizontal Scalability (Sharding)

The system utilizes a consistent hashing strategy via the `RedisShardManager` to distribute load across multiple Redis instances, preventing single-instance bottlenecks.

**Implementation**: User identifiers are hashed using the FNV-1a algorithm and mapped to Redis shards via modulo operation. This deterministic mapping ensures:
- The same user always maps to the same shard (consistency guarantee)
- Users are uniformly distributed across available shards (load balancing)
- Minimal rehashing when shards are added or removed (graceful scaling)

**Benefits**:
- Linear capacity scaling with additional Redis instances
- Parallel processing of rate limit checks across shards
- Fault isolation: failure of one shard affects only its assigned users
- No cross-shard coordination required, simplifying the architecture

### High-Concurrency Performance

The system leverages Go Goroutines to handle high-throughput traffic with non-blocking I/O, enabling efficient processing of thousands of concurrent rate limit checks.

**Performance Characteristics**:
- Each rate limit check involves a single network round-trip to Redis (Lua script execution)
- No application-level locking or synchronization primitives required (atomicity guaranteed by Redis)
- Middleware overhead is minimal, adding microseconds to request processing time
- Linear scaling characteristics with the number of application instances

**Concurrency Model**: Go's M:N scheduler multiplexes goroutines onto OS threads, minimizing context switching overhead while maintaining high CPU utilization. When a goroutine executes a Redis command, it can yield to other goroutines, allowing the system to handle thousands of concurrent requests efficiently.

---

## Getting Started

### Prerequisites

- Docker 20.10+
- Docker Compose 2.0+

### Deployment

1. **Clone the repository**:
   ```bash
   git clone <repository-url>
   cd velocity-rate-limiter
   ```

2. **Start the services**:
   ```bash
   docker-compose up --build
   ```

   This command will:
   - Build the Go application using a multi-stage Docker build
   - Start a Redis instance on port 6379
   - Start the rate limiter API on port 3000

3. **Verify the deployment**:
   ```bash
   curl http://localhost:3000/health
   ```

   Expected response:
   ```json
   {
     "status": "ok",
     "service": "velocity-rate-limiter"
   }
   ```

### Testing

The system includes comprehensive tests that verify concurrency safety and rate limiting accuracy.

**Run all tests**:
```bash
go test -v ./...
```

**Key Test: Concurrency Verification**

The `TestRateLimitConcurrency` test verifies that exactly 10 requests are allowed under concurrent load of 100 goroutines, proving the atomicity and integrity of the Lua script execution under maximal contention.

**Test Parameters**:
- 100 concurrent goroutines
- Single target user identifier
- Capacity: 10 tokens
- Rate: 1000 req/sec (effectively infinite, focusing on capacity constraint)

**Expected Result**: Exactly 10 requests are allowed, confirming perfect atomicity and preventing token overconsumption.

**Run specific test**:
```bash
go test -v -run TestRateLimitConcurrency
```

**Rate Refill Test**:
```bash
go test -v -run TestRateLimitRefill
```

This test verifies that tokens are correctly refilled over time based on the configured rate.

### Configuration

The system can be configured via environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `REDIS_ADDR` | Single Redis instance address | `localhost:6379` |
| `REDIS_ADDRS` | Comma-separated Redis addresses for sharding | Falls back to `REDIS_ADDR` |
| `PORT` | HTTP server port | `3000` |

**Example: Multiple Redis Shards**:
```bash
REDIS_ADDRS="redis1:6379,redis2:6379,redis3:6379" docker-compose up
```

### API Usage

**Rate Limited Endpoint**:
```bash
curl http://localhost:3000/api/resource
```

The endpoint is protected by rate limiting middleware. Response headers include:
- `X-RateLimit-Limit`: Maximum bucket capacity
- `X-RateLimit-Remaining`: Tokens remaining after the request
- `X-RateLimit-Retry-After`: Seconds until next token available (only when blocked)

**Rate Limit Exceeded Response (429)**:
```json
{
  "error": "Rate limit exceeded",
  "message": "Too many requests. Please try again later."
}
```

---

## Documentation & Analysis

For a deep dive into the architectural decisions, atomicity proofs, and sharding logic, please refer to the [Technical Report](REPORT.md).

The technical report provides:
- Detailed system architecture and request flow diagrams
- In-depth explanation of atomicity through Redis Lua scripts
- Scalability and distributed design rationale
- Empirical verification results from concurrency testing

---

## Production Considerations

### Monitoring

The system logs all rate limiting decisions at the INFO level:
- **Allowed requests**: User identifier, remaining tokens, and limit
- **Blocked requests**: User identifier, reason (429), and retry-after duration
- **System errors**: Critical Redis connection or execution failures

### Scaling

**Horizontal Scaling**:
- Add application instances behind a load balancer for increased throughput
- Add Redis shards to increase capacity and distribute load
- All instances share the same Redis configuration for consistency

**Vertical Scaling**:
- Increase Redis instance memory for larger user bases
- Adjust Go runtime parameters (GOMAXPROCS) based on CPU cores

### Fault Tolerance

The system implements a fail-open policy for Redis errors: if rate limiting cannot be determined due to Redis failures, requests are allowed to proceed. This ensures service availability during infrastructure issues, though rate limiting protection is temporarily disabled.

---

## License

This project is provided as a technical demonstration of distributed systems engineering principles.
