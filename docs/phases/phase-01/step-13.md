# Step 13 — Entry Points (cmd/)
### Files: `cmd/api/main.go` · `cmd/scheduler/main.go` · `cmd/worker/main.go`

---

## What are these files?

Each file is the `main()` function for one of Orion's three independent binaries:

```bash
go build ./cmd/api        →  bin/api        (HTTP server)
go build ./cmd/scheduler  →  bin/scheduler  (dispatch loops)
go build ./cmd/worker     →  bin/worker     (execution engine)
```

These are the **wiring layer** — they connect all the packages built in Steps 01–12 together, inject real dependencies, and start the service.

---

## Why 3 separate binaries?

| Binary | Scales how | Runs how many |
|---|---|---|
| `api` | Horizontally (more HTTP traffic → more pods) | Many |
| `scheduler` | Does NOT scale — leader election limits to 1 active | Many pods, 1 active |
| `worker` | Horizontally (more jobs → more worker pods) | Many |

If all three were one binary:
- Scaling workers would also scale the scheduler (unnecessary)
- A worker crash kills the API server
- You can't give workers more CPU without giving the API more CPU too

---

## Shared pattern in all three main.go files

Every main.go follows the same 6-step pattern:

```
1. config.Load()              → read env vars
2. observability.NewLogger()  → structured logging
3. context.WithCancel()       → cancellable context for clean shutdown
4. signal.Notify(SIGTERM)     → OS signal → cancel context
5. New(nil, nil, nil, ...)    → create the service (nil = Phase 2 TODOs)
6. service.Start(ctx)         → blocks until ctx cancelled
```

---

## cmd/api/main.go — the HTTP server

```go
// Route registration using Go 1.22 pattern routing
mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
mux.HandleFunc("GET /jobs", jobHandler.ListJobs)
mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
mux.HandleFunc("GET /healthz", ...)   // liveness probe
mux.HandleFunc("GET /readyz", ...)    // readiness probe
```

### HTTP timeouts — why every production server needs them
```go
srv := &http.Server{
    ReadTimeout:  10 * time.Second,  // abort if client takes >10s to send body
    WriteTimeout: 30 * time.Second,  // abort if we take >30s to respond
    IdleTimeout:  60 * time.Second,  // close keep-alive connections after 60s idle
}
```

Without timeouts: a slow/malicious client holds a connection open forever. With 1000 such clients, you exhaust all available goroutines (the "slow loris" attack). Timeouts bound resource usage.

### Graceful shutdown
```go
signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
<-stop   // blocks here

// Kubernetes sends SIGTERM → this unblocks
srv.Shutdown(context.WithTimeout(ctx, 15*time.Second))
// → stops accepting new connections
// → waits for in-flight requests to finish (up to 15s)
// → then exits
```

**Without graceful shutdown:** Kubernetes kills the process mid-request. Clients get `connection reset` errors. Response bodies are truncated.

**With graceful shutdown:** In-flight requests complete normally. Clients see clean responses.

### Liveness vs Readiness probes
```
GET /healthz  → "is this process alive?"
              → Kubernetes restarts the pod if this returns non-200
              → Always returns 200 if the process is running

GET /readyz   → "is this process ready to receive traffic?"
              → Kubernetes stops sending traffic if this returns non-200
              → Should check: can we reach PostgreSQL? Can we reach Redis?
              → (Phase 2 TODO: add real DB/Redis ping here)
```

---

## cmd/scheduler/main.go — the dispatch service

```go
sched := scheduler.New(
    scheduler.Config{
        BatchSize:        50,   // dispatch 50 jobs per cycle
        ScheduleInterval: 2s,   // run dispatch loop every 2 seconds
        OrphanInterval:   30s,  // run orphan sweep every 30 seconds
    },
    db,     // *pgxpool.Pool for advisory lock queries
    store,  // store.Store for job state transitions
    queue,  // queue.Queue for pushing to Redis
    logger,
)

sched.Run(ctx)  // blocks until ctx cancelled
```

The scheduler's `Run()` loop:
1. Calls `pg_try_advisory_lock(7331001)` — tries to become leader
2. If it gets the lock: runs dispatch loops
3. If not: waits 3 seconds and tries again
4. If it LOSES the lock (connection dropped): retries from step 1

When `cancel()` is called (SIGTERM): `ctx.Done()` fires inside `Run()`, the loops exit, and the advisory lock is released automatically when the DB connection closes.

---

## cmd/worker/main.go — the execution service

```go
pool := worker.NewPool(
    worker.WorkerConfig{
        WorkerID:          cfg.Worker.WorkerID,          // hostname by default
        QueueNames:        []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"},
        Concurrency:       10,     // run up to 10 jobs at once
        VisibilityTimeout: 5 * time.Minute,  // Redis PEL timeout
        HeartbeatInterval: 15 * time.Second, // tell scheduler we're alive
        ShutdownTimeout:   30 * time.Second, // max time to drain on SIGTERM
    },
    queue,     // queue.Queue
    store,     // store.Store
    executors, // []worker.Executor  ← Phase 4/5
    logger,
)

pool.Start(ctx)  // blocks until ctx cancelled, then drains
```

### What happens on SIGTERM (e.g. Kubernetes stopping a worker pod):
```
1. signal received → cancel() called → ctx.Done() fires
2. dequeue goroutines stop pulling from Redis
3. pool.drain() called → close(jobCh)
4. worker goroutines finish their current in-flight jobs
5. wg.Wait() — wait for all workers to call Done()
6. heartbeatLoop calls store.DeregisterWorker()
7. process exits
```

Jobs that don't finish within 30s: the scheduler's orphan sweep reclaims them after 90s (2× heartbeat TTL). They'll be retried on another worker.

---

## The TODO placeholders — why nil is OK in Phase 1

Every `main.go` has nil placeholders:
```go
jobHandler := handler.NewJobHandler(nil, logger)  // store = nil
```

This is intentional. Phase 1 establishes the complete skeleton:
- All interfaces defined
- All business logic written
- All goroutine patterns in place

Phase 2 fills in the nils:
```go
db, _ := pgxpool.New(ctx, cfg.Database.DSN)
store := postgres.New(db)
jobHandler := handler.NewJobHandler(store, logger)  // real store
```

If you call `POST /jobs` now it will panic (nil pointer). That's expected — Phase 1 is not runnable end-to-end. Phase 2 makes it fully operational.

---

## File locations

```
orion/
└── cmd/
    ├── api/
    │   └── main.go        ← step13-api-main.go
    ├── scheduler/
    │   └── main.go        ← step13-scheduler-main.go
    └── worker/
        └── main.go        ← step13-worker-main.go
```

---

## To run locally (after Phase 2 is complete)

```bash
# Terminal 1
make run-api
# → INFO API server listening port=8080

# Terminal 2
make run-scheduler
# → INFO starting scheduler
# → INFO acquired scheduler leader lock

# Terminal 3
make run-worker
# → INFO starting worker pool worker_id=your-hostname concurrency=10

# Test
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"test-job","type":"inline","payload":{"handler_name":"noop"}}'
```