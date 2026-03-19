# Step 07 — Redis Queue Implementation (`internal/queue/redis/redis_queue.go`)

## What is this file?

This is the **concrete implementation** of the `Queue` interface from Step 05. This is where actual Redis commands get called.

The separation is important:
- `queue/queue.go` (Step 05) says: *"there must be an Enqueue and Dequeue method"*
- `queue/redis/redis_queue.go` (this file) says: *"here's how to do it using Redis Streams"*

If you ever want to switch to NATS JetStream or Apache Kafka, you write a new file that implements the same interface. Zero changes to the scheduler or worker.

## Redis Streams — the core concept

Redis has several data structures. We use **Streams**, which work like this:

```
Stream: orion:queue:default
┌─────────────────────────────────────────────┐
│ 1704067200000-0  job_id: "abc"  payload: {} │  ← oldest
│ 1704067201000-0  job_id: "def"  payload: {} │
│ 1704067202000-0  job_id: "ghi"  payload: {} │  ← newest
└─────────────────────────────────────────────┘
```

Each entry has:
- A **unique ID** (timestamp-sequence, like `1704067200000-0`)
- **Fields**: our `job_id` and `payload` (the serialized job JSON)

## Consumer Groups — the delivery guarantee

A Consumer Group is Redis's way of enabling multiple workers to share a stream:

```
Stream: orion:queue:default
Consumer Group: orion-workers
    Consumer: worker-1 → processing message 1704067200000-0
    Consumer: worker-2 → processing message 1704067201000-0
    Consumer: worker-3 → waiting for next message
```

Key property: **each message is delivered to exactly one consumer in the group**. Two workers never get the same job.

### The Pending Entry List (PEL)
When a consumer reads a message with XREADGROUP, the message moves to the **PEL** — a "in-flight" list:

```
PEL for consumer worker-1:
  1704067200000-0  (idle for 47 seconds)

PEL for consumer worker-2:
  1704067201000-0  (idle for 12 seconds)
```

The message stays in the PEL until:
- Worker calls `XACK` → removed from PEL permanently ✓
- Worker crashes → message stays in PEL → XAUTOCLAIM reclaims it after timeout

This is how at-least-once delivery works.

## Key functions explained

### `New()` — startup setup
```go
func New(client *redis.Client, logger *slog.Logger) (*RedisQueue, error)
```

Creates the queue and ensures Redis consumer groups exist:
```redis
XGROUP CREATE orion:queue:default orion-workers $ MKSTREAM
```
- `MKSTREAM`: creates the stream if it doesn't exist
- `$`: start reading only new messages (not old ones that already exist)
- `BUSYGROUP` error is ignored — group already exists, that's fine

### `Enqueue()` — scheduler puts a job in
```go
func (r *RedisQueue) Enqueue(ctx context.Context, job *domain.Job) error
```

For immediate jobs:
```redis
XADD orion:queue:default * job_id "550e8400..." payload "{\"id\":\"550e8400...\"}"
```
- `*` means: auto-generate the message ID (use current timestamp)

For future-scheduled jobs (ScheduledAt is in the future):
```redis
ZADD orion:queue:scheduled 1704153600 "{serialized job JSON}"
```
- The score is the Unix timestamp when the job should run
- A separate goroutine (not yet built) periodically checks `ZRANGEBYSCORE` for jobs whose score ≤ NOW() and moves them to the stream

### `Dequeue()` — worker reads a job
```go
func (r *RedisQueue) Dequeue(ctx context.Context, queueName string, visibilityTimeout time.Duration) (*domain.Job, AckFunc, error)
```

```redis
XREADGROUP GROUP orion-workers worker-xyz COUNT 1 BLOCK 5000 STREAMS orion:queue:default >
```
- `GROUP orion-workers worker-xyz`: read as consumer "worker-xyz" in the group
- `COUNT 1`: read at most 1 message
- `BLOCK 5000`: wait up to 5 seconds for a message (instead of returning empty immediately)
- `>`: only read NEW messages (not ones already in PEL from a previous crash)

The `BLOCK 5000` is important: workers don't spin-loop. They sleep for up to 5 seconds waiting for a job, then wake up to check if their context was cancelled (shutdown signal).

The returned `AckFunc`:
```go
ackFn := func(processingErr error) error {
    if processingErr == nil {
        return r.client.XAck(ctx, streamName, consumerGroup, msg.ID).Err()
        // XACK orion:queue:default orion-workers 1704067200000-0
    }
    // On failure: do nothing — message stays in PEL for reclaim
    return nil
}
```

### `ReclaimStalePending()` — crash recovery
```go
func (r *RedisQueue) ReclaimStalePending(ctx context.Context, visibilityTimeout time.Duration)
```

This runs in a background goroutine, sweeping the PEL every 30 seconds:
```redis
XAUTOCLAIM orion:queue:default orion-workers reclaimer 300000 0-0 COUNT 100
```
- Find messages idle for > `visibilityTimeout` milliseconds
- Transfer them from the crashed consumer to "reclaimer"
- They'll be picked up by the next `XREADGROUP >`

This is how we recover from worker crashes without manual intervention.

## Crash scenario walkthrough

```
1. Worker-1 dequeues job "abc" → job moves to Worker-1's PEL
2. Worker-1 crashes without calling ackFn
3. Job "abc" stays in PEL, idle timer starts
4. After 5 minutes (visibility timeout):
   ReclaimStalePending runs → XAUTOCLAIM transfers "abc" to reclaimer
5. Worker-2 runs XREADGROUP > → reads "abc" from the stream
6. Worker-2 processes it successfully → XACK → job removed from PEL
```

## File location
```
orion/
└── internal/
    └── queue/
        ├── queue.go                ← Step 05 (interface)
        └── redis/
            └── redis_queue.go      ← you are here (implementation)
```

## Next step
Now we build the **observability system** — Prometheus metrics, OpenTelemetry tracing, and structured logging that give us visibility into what's happening at runtime.