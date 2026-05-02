# Instrumentation Wiring — Phase 7

## What This Document Covers

How job state transitions are connected to the gRPC streaming layer without coupling the store package to gRPC. The `InstrumentedStore` wrapper pattern, why it was chosen over alternatives, and how events flow from worker → store → broadcaster → gRPC stream → client.

---

## The Problem

The gRPC `WatchJob` stream needs to receive an event the moment a job transitions state. State transitions happen in the store layer (`MarkJobRunning`, `MarkJobCompleted`, `MarkJobFailed`). The store package must not import the gRPC package — that would create a circular dependency and violate the clean architecture boundary.

```
store/store.go          ← defines the Store interface
store/postgres/db.go    ← implements Store with SQL
internal/api/grpc/      ← gRPC server, broadcaster

# Forbidden:
store/postgres/db.go → imports internal/api/grpc  ← circular / wrong direction
```

---

## The Solution: InstrumentedStore Wrapper

`internal/api/grpc/instrumented_store.go` implements the wrapper pattern:

```go
type InstrumentedStore struct {
    store.Store              // embed the real store
    broadcaster *Broadcaster // publish events here
}

func (s *InstrumentedStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
    err := s.Store.MarkJobCompleted(ctx, id)  // delegate to real store
    if err == nil {
        s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
            JobId:     id.String(),
            NewStatus: "completed",
            Timestamp: timestamppb.Now(),
        })
    }
    return err
}
```

The wrapper:
1. Calls the real store method first
2. Only publishes if the DB update succeeded (no phantom events on failure)
3. Is transparent to callers — it satisfies `store.Store` exactly

---

## Why Not Option A: Inject Broadcaster into postgres.DB

```go
// Option A — rejected
type DB struct {
    pool        *pgxpool.Pool
    broadcaster *grpc.Broadcaster  // ← store imports grpc
}
```

This creates a dependency from `store/postgres` → `internal/api/grpc`. The store package would need to import the proto-generated types. Any change to the proto would require recompiling the store. The store becomes aware of the transport layer — wrong direction.

---

## Why Not Option B: Return Events from Store Methods

```go
// Option B — rejected
func (db *DB) MarkJobCompleted(ctx context.Context, id uuid.UUID) (*JobEvent, error) {
    // ...
}
```

This changes the `store.Store` interface. Every caller (worker pool, scheduler, tests) would need to handle the returned event. The interface becomes transport-aware.

---

## Wiring in cmd/api/main.go

```go
// Create broadcaster and wrap the store
broadcaster := grpcserver.NewBroadcaster()
instrumentedStore := grpcserver.NewInstrumentedStore(pgStore, broadcaster)

// gRPC server uses instrumentedStore (events published on state transitions)
grpcserver.RegisterGRPCServer(grpcSrv, grpcserver.NewServer(instrumentedStore, broadcaster, logger))

// HTTP handlers use pgStore directly (no events needed for HTTP)
jobHandler := handler.NewJobHandler(pgStore, logger)
```

The worker pool would use `instrumentedStore` when wired in Phase 8 so that job completions from workers also publish events.

---

## Event Flow Diagram

```
Worker goroutine
  └── executor.Execute(ctx, job) → success
      └── instrumentedStore.MarkJobCompleted(ctx, id)
          ├── db.MarkJobCompleted(ctx, id)          ← SQL UPDATE
          └── broadcaster.Publish(id, JobEvent{     ← in-memory fan-out
                  NewStatus: "completed"
              })
              ├── ch1 ← event  (WatchJob client 1)
              ├── ch2 ← event  (WatchJob client 2)
              └── ch3 ← event  (WatchJob client 3)
                  └── stream.Send(event)            ← gRPC HTTP/2 frame
                      └── client receives event
```

---

## Which Methods Are Instrumented

| Method | Event published |
|---|---|
| `MarkJobRunning` | `new_status: "running"`, includes `worker_id` |
| `MarkJobCompleted` | `new_status: "completed"` |
| `MarkJobFailed` | `new_status: "failed"`, includes `error_message` |

`CreateJob`, `TransitionJobState`, and other methods are not instrumented — they are either not state transitions that clients watch, or they are handled by the polling fallback in `WatchJob`.

---

## Broadcaster Design

`internal/api/grpc/broadcaster.go`:

```
Broadcaster
├── subscribers: map[jobID → map[subID → chan *JobEvent]]
├── Publish(jobID, event)   — non-blocking, drops if buffer full
├── Subscribe(jobID)        — returns (chan, unsubscribeFn)
└── SubscriberCount(jobID)  — for tests and health checks
```

**Why buffered channels (capacity 16)?**
If a `WatchJob` goroutine is slow to call `stream.Send`, `Publish` must not block — it would stall the worker pool's `MarkJobCompleted` call. The buffer absorbs bursts. If the buffer fills (16 events behind), the 17th is dropped. The 500ms polling fallback in `WatchJob` catches any dropped events.

**Why `sync.RWMutex`?**
`Publish` only reads the subscriber map (RLock). Multiple concurrent publishes for different job IDs don't block each other. Only `Subscribe` and `Unsubscribe` take a write lock.

---

## Memory Safety

Every `Subscribe` call returns an `unsubscribe` function. The `WatchJob` implementation defers it immediately:

```go
ch, unsubscribe := s.broadcaster.Subscribe(id.String())
defer unsubscribe()  // CRITICAL: called when WatchJob returns (any path)
```

`unsubscribe` removes the channel from the map and closes it. Without this, every client connection would leak a channel and a map entry permanently.

The `defer` fires on all four exit paths: normal completion, client disconnect, write failure, and already-terminal.
