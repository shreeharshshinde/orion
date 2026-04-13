# Phase 2 — Wiring the Entry Points
## `cmd/api/main.go` · `cmd/scheduler/main.go` · `cmd/worker/main.go`

---

## What changed from Phase 1

In Phase 1, every `main.go` had `nil` where the real dependencies should be:

```go
// Phase 1 (broken)
jobHandler := handler.NewJobHandler(nil, logger)   // nil → panic on first request
sched := scheduler.New(cfg, nil, nil, nil, logger) // nil → nothing works
pool := worker.NewPool(cfg, nil, nil, nil, logger) // nil → nothing works
```

Phase 2 replaces every `nil` with a real PostgreSQL store and Redis queue:

```go
// Phase 2 (working)
db, _ := pgxpool.NewWithConfig(ctx, poolCfg)
pgStore := postgres.New(db)
queue, _ := redisqueue.New(redisClient, logger)

jobHandler := handler.NewJobHandler(pgStore, logger)      // real store
sched := scheduler.New(cfg, db, pgStore, queue, logger)   // real db + store + queue
pool := worker.NewPool(cfg, queue, pgStore, nil, logger)  // real queue + store (executors still nil)
```

---

## Startup sequence (all three services)

Every `main.go` follows the same 9-step pattern:

```
1. config.Load()                    read all ORION_* env vars
2. observability.NewLogger()        structured logging (JSON in prod)
3. context.WithCancel()             cancellable context for shutdown
4. signal.Notify(SIGTERM/SIGINT)    OS signals → cancel context
5. pgxpool.NewWithConfig()          PostgreSQL connection pool
6. db.Ping()                        fail fast if DB unreachable
7. postgres.New(db)                 wrap pool in store interface
8. redis.NewClient() + .Ping()      Redis connection + health check
9. redisqueue.New() / postgres.New()  wire into service + start
```

---

## cmd/api/main.go — detailed walkthrough

### Why the API also connects to PostgreSQL directly

```go
// Readiness probe: real DB ping
mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
    if err := db.Ping(r.Context()); err != nil {
        w.WriteHeader(http.StatusServiceUnavailable)
        ...
    }
    ...
})
```

The `/readyz` endpoint pings PostgreSQL on every call. Kubernetes uses this to decide whether to send traffic. If the DB is unreachable, Kubernetes stops routing to this pod. This prevents requests from failing with 500 when the DB is down — Kubernetes routes them elsewhere.

### The `/jobs/{id}/executions` route — Phase 2 addition

```go
mux.HandleFunc("GET /jobs/{id}/executions", jobHandler.GetExecutions)
```

This route was not present in Phase 1. It returns the audit log of all execution attempts for a job. After a failed job is retried 3 times, this endpoint shows all 3 attempts with their errors and timestamps.

### HTTP timeouts — why they matter

```go
srv := &http.Server{
    ReadTimeout:  10 * time.Second,  // max time to read request body
    WriteTimeout: 30 * time.Second,  // max time to write response
    IdleTimeout:  60 * time.Second,  // max keep-alive idle
}
```

Without `ReadTimeout`: a client that sends data very slowly holds a goroutine open indefinitely. 10,000 such clients = OOM. With `ReadTimeout`: the connection is killed after 10 seconds.

`WriteTimeout` of 30 seconds accommodates slow clients and large response bodies. Most responses complete in milliseconds; the 30s is for pathological cases.

---

## cmd/scheduler/main.go — why it needs the raw pool

```go
// The scheduler needs the raw *pgxpool.Pool, not just the Store interface.
sched := scheduler.New(cfg, db, pgStore, queue, logger)
//                        ^^
//                        raw pool — needed for advisory lock queries
```

The scheduler uses PostgreSQL advisory locks for leader election:
```sql
SELECT pg_try_advisory_lock(7331001)
```

Advisory locks are **session-scoped** — they must be acquired and released on the same database connection. If we used the store interface, pgxpool could route the lock query and the release query to different connections, breaking the session semantics.

By passing the raw `*pgxpool.Pool`, the scheduler can acquire a dedicated connection for the lock and hold it for the duration of leadership.

### Redis startup check

```go
if err := redisClient.Ping(ctx).Err(); err != nil {
    logger.Error("failed to connect to redis", "addr", cfg.Redis.Addr, "err", err)
    os.Exit(1)
}
```

The scheduler fails fast if Redis is unreachable. There's no point starting the dispatch loops if jobs can't be pushed to the queue — dispatched jobs would get stuck in `scheduled` state in PostgreSQL with no corresponding Redis message.

---

## cmd/worker/main.go — the executors nil

```go
var executors []worker.Executor  // still nil in Phase 2
```

This is intentional. In Phase 2:
- Jobs are picked up from Redis ✓
- `MarkJobRunning` is called ✓
- The executor lookup finds no executor → `MarkJobFailed` with "no executor for job type inline"
- Job enters the retry cycle ✓

This is **correct Phase 2 behaviour**. The retry machinery, orphan detection, and state transitions all work. What's missing is the actual execution.

Phase 4 adds:
```go
registry := inline.NewRegistry()
registry.Register("noop", handlers.Noop)
executors := []worker.Executor{inline.NewExecutor(registry)}
```

After Phase 4, jobs with `"type": "inline"` will actually run and reach `status: "completed"`.

### Queue names default

```go
queueNames := cfg.Worker.Queues
if len(queueNames) == 0 {
    queueNames = []string{
        "orion:queue:high",
        "orion:queue:default",
        "orion:queue:low",
    }
}
```

If `ORION_WORKER_QUEUES` is not set, the worker pulls from all three queues. The order matters: `XREADGROUP` is called for each queue name in sequence. The worker checks high-priority jobs first, then default, then low.

---

## Environment variables for Phase 2

All have sensible defaults — nothing is required for local dev with docker compose.

| Variable | Default | Used by |
|---|---|---|
| `ORION_DATABASE_DSN` | `postgres://orion:orion@localhost:5432/orion` | all three |
| `ORION_DB_MAX_CONNS` | `20` | all three |
| `ORION_DB_MIN_CONNS` | `2` | all three |
| `ORION_REDIS_ADDR` | `localhost:6379` | scheduler, worker |
| `ORION_REDIS_PASSWORD` | (empty) | scheduler, worker |
| `ORION_WORKER_ID` | hostname | worker |
| `ORION_WORKER_CONCURRENCY` | `10` | worker |
| `ORION_WORKER_QUEUES` | (see above) | worker |
| `ORION_SCHEDULER_BATCH_SIZE` | `50` | scheduler |
| `ORION_SCHEDULER_INTERVAL` | `2s` | scheduler |

---

## Graceful shutdown — what happens in each service

### API Server (SIGTERM)

```
1. SIGTERM received → context cancelled
2. http.Server.Shutdown(15s timeout) called
3. No new connections accepted
4. In-flight requests continue until complete
5. After all requests done (or 15s timeout): process exits
6. pgxpool.Close() called via defer
```

### Scheduler (SIGTERM)

```
1. SIGTERM received → context cancelled
2. scheduler.Run() sees ctx.Done() → exits current dispatch cycle
3. releaseLeaderLock() called → pg_advisory_unlock(7331001)
4. db.Close() via defer → advisory lock released (also automatic)
5. queue.Close() via defer → Redis connection closed
6. Process exits cleanly
```

### Worker (SIGTERM)

```
1. SIGTERM received → context cancelled
2. Dequeue goroutines see ctx.Done() → stop pulling from Redis
3. pool.drain() called → close(jobCh)
4. Worker goroutines finish current in-flight jobs (up to ShutdownTimeout=30s)
5. heartbeatLoop calls store.DeregisterWorker() → status='offline'
6. pool.Start() returns
7. queue.Close(), db.Close() via defers
8. Process exits
```

---

## Verifying startup works

After wiring Phase 2, run all three services and check:

```bash
# API
curl http://localhost:8080/healthz
# → {"status":"ok"}

curl http://localhost:8080/readyz
# → {"status":"ready"}   (DB reachable)

# PostgreSQL — worker registered
psql postgres://orion:orion@localhost:5432/orion -c \
  "SELECT id, status, last_heartbeat FROM workers;"
# → your-hostname | idle | 2024-01-15 10:00:02

# Scheduler acquired leader lock
# Look in scheduler logs for:
# level=INFO msg="acquired scheduler leader lock"
```