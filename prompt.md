
# Prompt Log


## Velocity: Distributed API Rate Limiter (Go) Step-by-Step Prompts 

### Step 1: Environment Setup (Go & Redis)

**Goal:** Set up the Go project structure and configure `docker-compose.yml` to include Redis.

```
I want to set up the initial environment for building a high-performance distributed API Rate Limiter using Go and Redis.

1.  **Project Setup:**
    * Create the files: `main.go`, `go.mod`, and `go.sum`.
    * In `go.mod`, include the dependencies: `github.com/go-redis/redis/v8` and `github.com/gofiber/fiber/v2` (a fast Go web framework).
    * In `main.go`, include the basic configuration for a Fiber app (listening on port 3000) and the necessary code to initialize and connect the Redis client.

2.  **Container Setup (`docker-compose.yml`):**
    * Create a `docker-compose.yml` file that includes two services: `app` (the Go Rate Limiter) and `redis`.
    * Configure the `app` service to build from a `Dockerfile` and set an environment variable (e.g., `REDIS_ADDR=redis:6379`) so it can connect to the Redis host.
    * The `redis` service should use the official Redis image and expose port 6379.

3.  **Dockerfile:**
    * Create a `Dockerfile` for building and running the Go application. Utilize a **multi-stage build process** for production readiness and minimal image size (e.g., use a builder stage and a small base image like `alpine` for the final stage).
```

-----

### Step 2: Rate Limiter Core Logic - Token Bucket Algorithm

**Goal:** Implement the core **Token Bucket Algorithm** logic in Go, managing token information in Redis based on a `user_id`.

```
Implement the core distributed Rate Limiter logic in Go.

1.  **Rate Limiter Structure:**
    * Define a struct named `RateLimiter` containing a member for the `*redis.Client` to enable communication with Redis in a distributed environment.
    * Add an essential method to this struct: `Allow(userID string) (bool, error)`.

2.  **Token Bucket Implementation:**
    * Inside the `Allow` method, implement the **Token Bucket Algorithm**.
    * Define the key parameters: **Rate** (e.g., $\text{5 req/sec}$) and **Capacity** (e.g., 10).
    * Use Redis to store and update the user's **last request time** and **current token count**. A **Hash** or **Sorted Set** data structure in Redis is recommended for this.
    * The logic should calculate the **number of tokens to refill** based on the elapsed time and then update the current token count. Finally, it checks if the request can be served and returns a boolean.
    * **Crucially:** The token update and consumption logic **must guarantee Atomicity** in a distributed environment. Use a **Redis Lua Script** for the entire operation. Provide the Go code to execute the Lua Script and the Lua Script code itself.
```

[Image of Token Bucket Algorithm Flowchart]

---

### Step 3: API Integration & Middleware

**Goal:** Integrate the Rate Limiter logic into the `gofiber` app and apply it as middleware to protect an API endpoint.

```
Integrate the Rate Limiter logic from Step 2 into the Fiber application and configure it as a global middleware.

1.  **Middleware Function:**
    * Write a function: `RateLimitMiddleware(limiter *RateLimiter) fiber.Handler`.
    * This middleware should extract a **Client Identifier** (e.g., a header like `X-Client-ID` or the request IP address; use the request **IP address** for simplicity as the `userID`).
    * Call `limiter.Allow(userID)` to determine if the request is allowed.

2.  **API Response Handling:**
    * **If Allowed (true):** Call `c.Next()` to proceed to the next handler.
    * **If Blocked (false):** Return an HTTP **429 Too Many Requests** status code. **Crucially**, include the recommended Rate Limiter headers in the response: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and `X-RateLimit-Retry-After` (calculate the retry-after time based on the token refill rate).

3.  **Router Configuration:**
    * In `main.go`, define a simple GET endpoint, `/api/resource`, and apply the middleware to it.
```

---

### Step 4: Concurrency & Unit Testing (Go Concurrency)

**Goal:** Write test code to verify the concurrency safety and correctness of the Rate Limiter, demonstrating proficiency with **Go Goroutines**.

```
Using Go's built-in testing framework, write test code to verify the concurrency safety and accuracy of the Rate Limiter.

1.  **Test Setup:**
    * Create the file `rate_limiter_test.go`. The test should utilize a real Redis connection (as configured in the environment setup).

2.  **Concurrency Test (`TestRateLimitConcurrency`):**
    * Set the **Capacity** to 10 and the **Rate** to a very high number (focusing the test on Capacity constraint).
    * Launch **100 Goroutines** simultaneously, all targeting the **same single `userID`**.
    * Each Goroutine attempts to call `limiter.Allow()` once.
    * Use `sync.WaitGroup` to manage the Goroutines.
    * The test must verify that the **total count of successfully allowed requests** is **exactly equal to the Capacity value (10)**, proving that the Token Bucket's atomic consumption logic works under high contention.

3.  **Rate Limit Refill Test (`TestRateLimitRefill`):**
    * Write a test that sends 10 requests (consuming all tokens).
    * Then, wait for a calculated time interval (e.g., 1 second if rate is $\text{5 req/sec}$) and verify that the expected number of tokens have been refilled, allowing the next batch of requests to succeed.
```

---

### Step 5: Distributed System Enhancement - Sharding Concept

**Goal:** Introduce the concept of **Sharding** to demonstrate how the Rate Limiter can scale horizontally in a true distributed environment.

```
To enhance the project's appeal as a distributed system, implement a **Sharding concept** to manage multiple Redis instances.

1.  **Sharding Manager Structure:**
    * Define a struct named `RedisShardManager`. This struct should hold an array of connected `*redis.Client` instances.
    * Implement a constructor function that takes a list of Redis addresses and connects to each instance.

2.  **Key Sharding Function:**
    * Add a method: `GetClient(userID string) *redis.Client`.
    * This function must implement a strategy to consistently map the input `userID` string to one of the available Redis clients (shards). Use a simple **modulo operation on a hash of the `userID`** (e.g., `hash(userID) % N`, where N is the number of shards).

3.  **Integration:**
    * Update the `RateLimiter` struct to replace the single `*redis.Client` with the new `*RedisShardManager`.
    * Modify the `Allow(userID)` method to first call `manager.GetClient(userID)` to ensure the user's token information is always read from and written to the **same Redis shard**.
```

---

### Step 6: Code Quality & Professional Documentation

**Goal:** Improve logging and create the final comprehensive **REPORT.md** to professionally summarize the project's technical architecture for a hiring manager.

```
Perform the final necessary actions to ensure the highest professional standards for the Velocity project and generate the core documentation artifact.

1.  **Robust Logging Implementation:**
    * Implement robust, structured logging using a standard Go logging library (e.g., standard `log` or a structured library like `zap`) within the Rate Limiter logic and the middleware. Logging must be at the **INFO** level unless errors occur.
    * The logs must accurately and formally track the Rate Limiter's decision process:
        * **Decision: ALLOWED:** Log a clear message indicating a successful request allowance. Include the `userID`, the current `Remaining` tokens, and the `Limit` applied.
        * **Decision: BLOCKED (429):** Log the event when a request is rate-limited. Include the `userID`, the reason (429), and the calculated `Retry-After` duration.
        * **System Error:** Log any critical failures during Redis connection or Lua script execution (e.g., "Critical Redis Error: Falling back to Fail-Open Policy").

2.  **REPORT.md Creation (Technical Summary):**
    * Create a formal and highly technical **REPORT.md** file in the root directory. This document must summarize the system's technical architecture, core engineering decisions, and empirical verification. **Do not use emojis or casual language.**

    * **Section 1: System Architecture and Request Flow:**
        * Provide a high-level overview of the Velocity Distributed Rate Limiter architecture.
        * Detail the components involved: **Go Fiber, Rate Limiter Core (Token Bucket), Redis Lua Script Engine, and Redis Shard Manager.**
        * Describe the sequence of operations for a single API request, from Middleware interception to the Shard Manager key resolution, Lua script execution, and final response headers. 

    * **Section 2: Core Engineering: Atomicity and Concurrency Control:**
        * Detail the rationale for selecting the **Token Bucket Algorithm** over fixed or sliding window approaches.
        * Provide an in-depth explanation of how **Redis Lua Scripting** was leveraged to enforce **atomicity** (A in ACID) for the token consumption and refill operations. Explicitly state that this single-call execution model eliminates the **distributed race condition** inherent in multi-step Redis transactions.

    * **Section 3: Scalability and Distributed Design:**
        * Discuss the role of **Go Goroutines** in enabling high-concurrency, low-latency processing of incoming requests.
        * Explain the implementation and necessity of the **Redis Shard Manager** (using hash/modulo logic). Articulate how this design ensures **horizontal scalability** of the persistence layer, prevents a single Redis instance from becoming a bottleneck, and guarantees that a user's token state is consistently mapped to the same shard.

    * **Section 4: Empirical Verification Summary:**
        * Present the key findings from the executed **Concurrency Test (`TestRateLimitConcurrency`)** as empirical proof of system stability.
        * State the test parameters (e.g., 100 concurrent Goroutines, Capacity 10) and the outcome. Formally conclude: "The test successfully verified that the system allowed exactly 10 requests, confirming the perfect atomicity and integrity of the Lua script execution under maximal contention."
```

---

## Step 7: Professional README.md Creation Prompt

**Objective:** Generate a comprehensive, professional-grade **README.md** file that serves as the entry point for the Velocity project. The document must be written in a formal, technical tone suitable for evaluation by Senior Software Engineers and Engineering Managers. **Avoid emojis, casual phrasing, or marketing fluff.**

```
Create a highly professional **README.md** file for the Velocity project. The document should communicate the project's engineering depth and business value immediately.

**Guidelines:**
* **Tone:** Formal, technical, and objective. (e.g., instead of "Fast and cool!", use "High-throughput and low-latency design").
* **Target Audience:** Technical Recruiters, Engineering Leads, and System Architects.
* **Formatting:** Use clean Markdown with headers, bullet points, and code blocks.

**Content Requirements:**

1.  **Project Title & Executive Summary:**
    * **Title:** Velocity: Distributed High-Performance API Rate Limiter Backend.
    * **Summary:** Define the project as a scalable microservices component designed to protect downstream infrastructure from traffic surges and abuse. Mention the core value proposition: **Ensuring system stability via distributed concurrency control.**

2.  **Architecture & Technology Stack:**
    * Present the stack (Go, Fiber, Redis, Docker) in a clean table or list.
    * Briefly explain the role of each component (e.g., "Redis Lua Scripting: Enforcing atomic token operations").

3.  **Key Engineering Features (Technical Highlights):**
    * Highlight the specific engineering problems solved. Use the following points:
        * **Atomic Distributed State Management:** implementation of the Token Bucket algorithm using custom Redis Lua scripts to eliminate race conditions (ACID properties).
        * **Horizontal Scalability (Sharding):** utilization of a consistent hashing strategy (via `RedisShardManager`) to distribute load across multiple Redis instances, preventing bottlenecks.
        * **High-Concurrency Performance:** leverage of Go Goroutines to handle high-throughput traffic with non-blocking I/O.

4.  **Getting Started (Deployment & Testing):**
    * Provide concise, standard instructions for running the system.
    * **Prerequisites:** Docker & Docker Compose.
    * **Run Command:** `docker-compose up --build`
    * **Verification:** Provide the command to run the concurrency tests (`go test -v ./...`) and explain *what* this test proves (e.g., "Verifies that exactly 10 requests are allowed under concurrent load of 100 goroutines").

5.  **Documentation & Analysis:**
    * Add a specific section pointing to **REPORT.md**.
    * Description: "For a deep dive into the architectural decisions, atomicity proofs, and sharding logic, please refer to the [Technical Report](REPORT.md)."
```