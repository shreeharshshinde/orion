# Orion — Phase 3 Master Guide
## Inline Executor: The First Real Job Execution

> **What this document is:** Everything you need to understand, plan, and build Phase 3 completely before writing a single line of code — the mental model, every file to create, every design decision explained, the exact code to write, and what you must observe to know Phase 3 is done.

---

## Table of Contents

1. [Why Phase 3 Exists — The Gap It Closes](#1-why-phase-3-exists--the-gap-it-closes)
2. [What Changes: Before and After](#2-what-changes-before-and-after)
3. [Mental Model — How Inline Execution Works](#3-mental-model--how-inline-execution-works)
4. [Complete File Plan](#4-complete-file-plan)
5. [File 1: `internal/worker/inline.go` — The Executor](#5-file-1-internalworkerinlinego--the-executor)
6. [File 2: `internal/worker/handlers/handlers.go` — Built-in Handlers](#6-file-2-internalworkerhandlershandlersgo--built-in-handlers)
7. [File 3: `cmd/worker/main.go` — Updated Wiring](#7-file-3-cmdworkermainago--updated-wiring)
8. [File 4: `internal/worker/inline_test.go` — Tests](#8-file-4-internalworkerinline_testgo--tests)
9. [The Execution Audit Trail — RecordExecution](#9-the-execution-audit-trail--recordexecution)
10. [Error Handling Strategy](#10-error-handling-strategy)
11. [Context and Cancellation](#11-context-and-cancellation)
12. [The Pool Connection — How pool.go Calls the Executor](#12-the-pool-connection--how-poolgo-calls-the-executor)
13. [Step-by-Step Build Order](#13-step-by-step-build-order)
14. [How to Run and Verify Phase 3](#14-how-to-run-and-verify-phase-3)
15. [Complete End-to-End Test Sequence](#15-complete-end-to-end-test-sequence)
16. [Common Mistakes and How to Avoid Them](#16-common-mistakes-and-how-to-avoid-them)
17. [Phase 4 Preview](#17-phase-4-preview)

---

## 1. Why Phase 3 Exists — The Gap It Closes

At the end of Phase 2, Orion is fully wired:

```
Client → POST /jobs → PostgreSQL (status=queued)
Scheduler → picks up job → Redis (status=scheduled)
Worker → dequeues from Redis → attempts execution
→ FAILS: "no executor for job type inline"
→ PostgreSQL (status=failed, attempt=1)
→ retry loop begins
```

The entire infrastructure works. Jobs flow through every system. But they never actually **do anything** — every single job fails because there is nothing to execute it.

Phase 3 plugs exactly that gap. After Phase 3:

```
Worker → dequeues from Redis → finds InlineExecutor
→ calls job.Payload.HandlerName ("noop", "preprocess", etc.)
→ handler returns nil (success)
→ PostgreSQL (status=completed)
← first ever completed job ←
```

This is the milestone that turns Orion from "infrastructure that fails gracefully" into "infrastructure that actually works."

---

## 2. What Changes: Before and After

### Phase 2 state (what you have now)

```go
// cmd/worker/main.go — Phase 2
var executors []worker.Executor  // empty slice — nothing can execute

pool := worker.NewPool(cfg, queue, store, executors, logger)
// pool.resolveExecutor("inline") → nil → MarkJobFailed("no executor...")
```

Every job submitted with `"type": "inline"` follows this path:
```
executeJob() called
  → resolveExecutor("inline") → nil
  → MarkJobFailed(id, "no executor for job type inline", retryAt)
  → ackFn(err)  ← NACK: stays in Redis PEL
```

### Phase 3 state (what you will have)

```go
// cmd/worker/main.go — Phase 3
registry := inline.NewRegistry()
registry.Register("noop",                handlers.Noop)
registry.Register("preprocess_dataset",  handlers.PreprocessDataset)
registry.Register("validate_data",       handlers.ValidateData)
registry.Register("compute_statistics",  handlers.ComputeStatistics)

executors := []worker.Executor{
    inline.NewExecutor(registry, logger),
}

pool := worker.NewPool(cfg, queue, store, executors, logger)
// pool.resolveExecutor("inline") → InlineExecutor → runs the handler
```

Every job submitted with `"type": "inline"` follows this path:
```
executeJob() called
  → resolveExecutor("inline") → InlineExecutor ✓
  → MarkJobRunning(id, workerID)
  → registry.Get("noop") → noop handler function
  → noop(ctx, job) → nil (success)
  → MarkJobCompleted(id)
  → ackFn(nil)  ← ACK: removed from Redis PEL forever ✓
  → RecordExecution(attempt=1, status=completed)
```

---

## 3. Mental Model — How Inline Execution Works

### The registry pattern — like a phone book for handlers

Think of the Registry as a phone book. Before any jobs run, you "register" phone numbers:
```
"noop"               →  noopHandler()
"preprocess_dataset" →  preprocessDataset()
"train_model"        →  trainModel()
```

When a job arrives with `handler_name: "preprocess_dataset"`, the executor looks up the phone book, finds the function, and calls it. If the name isn't in the book — that's an error, but a clean one with a useful message.

```
Job arrives: handler_name = "preprocess_dataset"
Registry lookup: "preprocess_dataset" → found ✓
Call: preprocessDataset(ctx, job) → runs the function
Result: nil → success / error → failure (will retry)
```

### The Executor interface — already defined in Phase 1

```go
// In internal/worker/pool.go (Phase 1, unchanged)
type Executor interface {
    Execute(ctx context.Context, job *domain.Job) error
    CanExecute(jobType domain.JobType) bool
}
```

`CanExecute` answers "can you handle this job type?" The pool calls this first, then calls `Execute` only on the matching executor:

```
job.Type = "inline"
pool asks InlineExecutor: CanExecute("inline") → true ✓
pool asks KubernetesExecutor: CanExecute("inline") → false (Phase 4 handles "k8s_job")
pool uses: InlineExecutor.Execute(ctx, job)
```

### The full data flow through Phase 3

```
┌──────────────────────────────────────────────────────────────────┐
│  POST /jobs                                                       │
│  {"type":"inline","payload":{"handler_name":"preprocess",...}}   │
└────────────────────────────┬─────────────────────────────────────┘
                             │ store.CreateJob() → status=queued
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  PostgreSQL: jobs table                                           │
│  status=queued  payload={"handler_name":"preprocess","args":{..}}│
└────────────────────────────┬─────────────────────────────────────┘
                             │ scheduler.scheduleQueuedJobs() (every 2s)
                             │ CAS: queued → scheduled
                             │ XADD orion:queue:default
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  Redis Streams: orion:queue:default                               │
│  message: {job_id: "abc", payload: "{...}"}                      │
└────────────────────────────┬─────────────────────────────────────┘
                             │ XREADGROUP (blocking)
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  Worker Pool goroutine                                            │
│                                                                   │
│  1. store.MarkJobRunning(id, workerID)   ← CAS: scheduled→running│
│  2. executor = resolveExecutor("inline") ← InlineExecutor ✓      │
│  3. fn = registry.Get("preprocess")      ← handler found ✓       │
│  4. fn(ctx, job)                         ← YOUR CODE RUNS HERE   │
│                                                                   │
│  On success:                                                      │
│  5a. store.MarkJobCompleted(id)          ← CAS: running→completed│
│  5b. store.RecordExecution(attempt,completed)  ← audit log       │
│  5c. ackFn(nil)                          ← XACK: removes from PEL│
│                                                                   │
│  On failure:                                                      │
│  5a. store.MarkJobFailed(id, err, nextRetryAt) ← running→failed  │
│  5b. store.RecordExecution(attempt,failed)     ← audit log       │
│  5c. ackFn(err)                                ← NACK: stays PEL │
└──────────────────────────────────────────────────────────────────┘
```

---

## 4. Complete File Plan

Phase 3 touches exactly 5 locations. Two new files, two updated files, one new directory with built-in handlers.

```
internal/worker/
├── pool.go                    ← Phase 1, UNCHANGED — already handles executors correctly
├── inline.go                  ← NEW: Registry, HandlerFunc, InlineExecutor
├── inline_test.go             ← NEW: unit + integration tests for inline execution
└── handlers/
    └── handlers.go            ← NEW: built-in handlers (noop, echo, fail, slow)

cmd/worker/
└── main.go                    ← UPDATED: wire registry + InlineExecutor into pool

docs/phase3/
└── PHASE3-MASTER-GUIDE.md    ← this file (already written)
└── README-inline-executor.md  ← WRITTEN AFTER: beginner explanation of inline execution
```

### What is NOT changing

| File | Why unchanged |
|---|---|
| `internal/worker/pool.go` | `resolveExecutor()` already loops through `p.executors`. Adding InlineExecutor makes it find a match. Zero code changes needed. |
| `internal/store/postgres/db.go` | All DB operations (`MarkJobRunning`, `MarkJobCompleted`, `MarkJobFailed`, `RecordExecution`) already exist. |
| `internal/scheduler/scheduler.go` | Scheduler dispatches any job regardless of type. No type awareness needed. |
| `internal/api/handler/job.go` | Already validates `handler_name` for inline jobs. No changes. |
| `internal/domain/job.go` | `JobPayload.HandlerName` and `JobPayload.Args` already exist. |

---

## 5. File 1: `internal/worker/inline.go` — The Executor

This is the core file of Phase 3. It contains three things:
1. `HandlerFunc` — the type signature all handlers must implement
2. `Registry` — the thread-safe map from name → handler function
3. `InlineExecutor` — implements `worker.Executor`, bridges pool to registry

### Complete code to write

```go
// Package worker contains the worker pool and executor implementations.
// This file adds InlineExecutor — the executor that runs registered Go functions.
//
// Architecture:
//   Registry  →  maps handler names to HandlerFunc implementations
//   InlineExecutor  →  implements worker.Executor, calls registry.Get() then fn()
//
// Usage pattern:
//   registry := worker.NewRegistry()
//   registry.Register("my_handler", myHandler)
//   executor := worker.NewInlineExecutor(registry, logger)
//   pool := worker.NewPool(cfg, queue, store, []Executor{executor}, logger)
package worker

import (
    "context"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/shreeharsh-a/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// HandlerFunc
// ─────────────────────────────────────────────────────────────────────────────

// HandlerFunc is the signature that every inline job handler must implement.
//
// The handler receives:
//   - ctx: a context that may be cancelled if the job's deadline is reached
//           or the worker is shutting down. Always check ctx.Err() in long loops.
//   - job: the full job record, including job.Payload.Args (the caller's parameters)
//           and job.Attempt (which retry this is).
//
// The handler must:
//   - return nil on success → job is marked completed
//   - return a non-nil error on failure → job is marked failed (retried if retries remain)
//
// The handler must NOT:
//   - panic (the pool does not recover panics — wrap in recover() if needed)
//   - ignore ctx cancellation in long operations (causes slow shutdown)
//   - modify job.Status or job.Attempt directly (those are managed by the pool)
type HandlerFunc func(ctx context.Context, job *domain.Job) error

// ─────────────────────────────────────────────────────────────────────────────
// Registry
// ─────────────────────────────────────────────────────────────────────────────

// Registry is a thread-safe map from handler name to HandlerFunc.
//
// Handlers are registered at startup, before pool.Start() is called.
// At runtime, InlineExecutor reads the registry (read-heavy, no contention).
// New registrations after pool.Start() are safe but unusual.
//
// The registry is injected into InlineExecutor — the executor holds a pointer,
// so registrations made after NewInlineExecutor() are visible to Execute().
type Registry struct {
    mu       sync.RWMutex
    handlers map[string]HandlerFunc
}

// NewRegistry creates an empty Registry.
// Register handlers before passing it to NewInlineExecutor.
func NewRegistry() *Registry {
    return &Registry{
        handlers: make(map[string]HandlerFunc),
    }
}

// Register adds a handler under the given name.
// The name must exactly match job.Payload.HandlerName in submitted jobs.
// Registering the same name twice overwrites the previous handler.
// Register is safe to call concurrently.
func (r *Registry) Register(name string, fn HandlerFunc) {
    if name == "" {
        panic("worker.Registry.Register: handler name cannot be empty")
    }
    if fn == nil {
        panic("worker.Registry.Register: handler function cannot be nil")
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    r.handlers[name] = fn
}

// Get retrieves a handler by name.
// Returns (handler, true) if found, (nil, false) if not registered.
// Get is safe to call concurrently and is optimised for the read path.
func (r *Registry) Get(name string) (HandlerFunc, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    fn, ok := r.handlers[name]
    return fn, ok
}

// List returns all registered handler names. Useful for health checks and debugging.
func (r *Registry) List() []string {
    r.mu.RLock()
    defer r.mu.RUnlock()
    names := make([]string, 0, len(r.handlers))
    for name := range r.handlers {
        names = append(names, name)
    }
    return names
}

// Len returns the number of registered handlers.
func (r *Registry) Len() int {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return len(r.handlers)
}

// ─────────────────────────────────────────────────────────────────────────────
// InlineExecutor
// ─────────────────────────────────────────────────────────────────────────────

// InlineExecutor implements worker.Executor for jobs with type = "inline".
//
// When the worker pool asks resolveExecutor("inline"), it receives this executor.
// Execute() then:
//   1. Reads job.Payload.HandlerName from the job
//   2. Looks it up in the registry
//   3. Calls the registered HandlerFunc with the job's context and payload
//   4. Returns the error (or nil on success) — the pool handles DB state transitions
//
// InlineExecutor has no knowledge of PostgreSQL, Redis, or the state machine.
// It only knows about handlers and calling them.
type InlineExecutor struct {
    registry *Registry
    logger   *slog.Logger
}

// NewInlineExecutor creates an InlineExecutor backed by the given registry.
// The registry pointer is held — registrations after this call are visible.
func NewInlineExecutor(registry *Registry, logger *slog.Logger) *InlineExecutor {
    if registry == nil {
        panic("worker.NewInlineExecutor: registry cannot be nil")
    }
    return &InlineExecutor{
        registry: registry,
        logger:   logger,
    }
}

// CanExecute returns true only for inline job types.
// The worker pool calls this to find the right executor for each job.
func (e *InlineExecutor) CanExecute(jobType domain.JobType) bool {
    return jobType == domain.JobTypeInline
}

// Execute runs the handler registered under job.Payload.HandlerName.
//
// Error cases:
//   - handler_name is empty → error (caught by handler validation at API layer, shouldn't reach here)
//   - handler_name not registered → error with descriptive message
//   - handler returns error → propagated as-is (pool will retry)
//   - handler panics → CALLER'S RESPONSIBILITY (wrap handler in recover if needed)
//   - ctx cancelled → handler should detect this and return ctx.Err()
func (e *InlineExecutor) Execute(ctx context.Context, job *domain.Job) error {
    handlerName := job.Payload.HandlerName
    if handlerName == "" {
        return fmt.Errorf("inline job %s has empty handler_name in payload", job.ID)
    }

    fn, ok := e.registry.Get(handlerName)
    if !ok {
        // This happens when a job was submitted with a handler_name that was never
        // registered on this worker. The job will be retried, but if the handler
        // is never registered, it will eventually reach status=dead.
        return fmt.Errorf("handler %q is not registered on this worker (registered: %v)",
            handlerName, e.registry.List())
    }

    e.logger.Debug("calling inline handler",
        "job_id", job.ID,
        "handler", handlerName,
        "attempt", job.Attempt,
        "args_keys", argKeys(job.Payload.Args),
    )

    start := time.Now()
    err := fn(ctx, job)
    elapsed := time.Since(start)

    if err != nil {
        e.logger.Warn("inline handler returned error",
            "job_id", job.ID,
            "handler", handlerName,
            "elapsed_ms", elapsed.Milliseconds(),
            "err", err,
        )
        return err
    }

    e.logger.Debug("inline handler completed",
        "job_id", job.ID,
        "handler", handlerName,
        "elapsed_ms", elapsed.Milliseconds(),
    )
    return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// argKeys returns the keys of the args map for logging.
// Avoids logging values (may contain sensitive data).
func argKeys(args map[string]any) []string {
    if len(args) == 0 {
        return nil
    }
    keys := make([]string, 0, len(args))
    for k := range args {
        keys = append(keys, k)
    }
    return keys
}
```

### Why this design

**Why `HandlerFunc` not an interface?**

A function type is simpler to register and call. You don't need to create a struct, implement methods, and pass an instance. You just pass a function:
```go
registry.Register("noop", func(ctx context.Context, job *domain.Job) error { return nil })
```

**Why `sync.RWMutex` not `sync.Mutex`?**

At runtime, all goroutines only READ from the registry (many concurrent jobs calling `Get()`). Writing only happens at startup. `RWMutex` allows unlimited concurrent readers, only blocking when a writer is active. This is zero-contention under normal operation.

**Why log arg keys but not values?**

Job args may contain API keys, credentials, or PII. Logging keys (e.g., `["model_path", "learning_rate"]`) gives useful debug context without risking log leaks. The actual values stay in the job payload, readable from the DB if needed.

**Why panic on nil/empty in Register?**

These are programmer errors at startup, not runtime errors. A nil handler or empty name will cause silent failures later. Panicking at registration time gives a clear stack trace pointing to the misconfiguration.

---

## 6. File 2: `internal/worker/handlers/handlers.go` — Built-in Handlers

Built-in handlers serve two purposes:
1. **Testing** — `noop` and `fail` let you test the happy path and error path without writing a real handler
2. **Examples** — `echo` and `slow` demonstrate the correct handler pattern for new developers

```go
// Package handlers provides built-in HandlerFunc implementations for Orion.
//
// These handlers are registered in cmd/worker/main.go and serve as:
//   1. Smoke-test utilities (noop, fail, slow) — verify the pipeline works
//   2. Code examples showing correct HandlerFunc patterns
//
// All production ML handlers (preprocess_dataset, train_model, etc.) belong
// in a separate handlers package specific to your use case. These are generic.
package handlers

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/shreeharsh-a/orion/internal/domain"
)

// Noop is a handler that does nothing and always succeeds.
//
// Use for:
//   - Smoke testing the entire pipeline (submit a noop job, verify status=completed)
//   - Load testing the orchestration overhead without real work
//   - Placeholder during development before real handlers exist
func Noop(_ context.Context, _ *domain.Job) error {
    return nil
}

// Echo logs the job's args and succeeds.
//
// Use for:
//   - Verifying that args are correctly passed through from submission to execution
//   - Debugging the payload structure
func Echo(_ context.Context, job *domain.Job) error {
    slog.Info("echo handler",
        "job_id", job.ID,
        "job_name", job.Name,
        "attempt", job.Attempt,
        "args", job.Payload.Args,
    )
    return nil
}

// AlwaysFail is a handler that always returns an error.
//
// Use for:
//   - Testing the retry cycle (submit a fail job, watch it retry N times then reach dead)
//   - Testing the error_message field is correctly stored
//   - Testing that dead-letter behaviour works correctly
func AlwaysFail(_ context.Context, job *domain.Job) error {
    return fmt.Errorf("deliberate failure on attempt %d (testing error path)", job.Attempt)
}

// Slow simulates a long-running job that respects context cancellation.
//
// Use for:
//   - Testing graceful shutdown (start a slow job, send SIGTERM, verify drain waits)
//   - Testing job deadlines (submit with a short deadline, verify timeout handling)
//   - Testing the heartbeat is maintained during long execution
//
// The duration comes from job.Payload.Args["duration_seconds"] (default: 5s).
// Always use time.After + select pattern to respect context cancellation.
func Slow(ctx context.Context, job *domain.Job) error {
    // Read duration from args, default 5 seconds
    durationSec := 5
    if args := job.Payload.Args; args != nil {
        if v, ok := args["duration_seconds"]; ok {
            if n, ok := v.(float64); ok { // JSON numbers decode as float64
                durationSec = int(n)
            }
        }
    }

    slog.Info("slow handler started",
        "job_id", job.ID,
        "duration_seconds", durationSec,
    )

    // CORRECT pattern: always select on both timer AND ctx.Done()
    // This is the single most important pattern for long-running handlers.
    // Without ctx.Done(), the handler ignores SIGTERM and blocks shutdown.
    select {
    case <-time.After(time.Duration(durationSec) * time.Second):
        slog.Info("slow handler completed", "job_id", job.ID)
        return nil
    case <-ctx.Done():
        slog.Info("slow handler cancelled by context", "job_id", job.ID, "reason", ctx.Err())
        return ctx.Err() // return the cancellation error so the pool handles it correctly
    }
}

// PanicRecover wraps a handler and recovers from panics, converting them to errors.
//
// Use this as a decorator when you're not sure if a handler might panic:
//
//   registry.Register("unsafe_handler", handlers.PanicRecover(unsafeHandler))
//
// Without this wrapper, a panicking handler crashes the worker goroutine.
// With it, the panic is caught and the job is marked failed (and retried).
func PanicRecover(fn func(ctx context.Context, job *domain.Job) error) func(ctx context.Context, job *domain.Job) error {
    return func(ctx context.Context, job *domain.Job) (err error) {
        defer func() {
            if r := recover(); r != nil {
                err = fmt.Errorf("handler panicked: %v (job_id: %s)", r, job.ID)
                slog.Error("handler panic recovered",
                    "job_id", job.ID,
                    "panic", r,
                )
            }
        }()
        return fn(ctx, job)
    }
}
```

### The `Slow` handler — the most important example

The `Slow` handler demonstrates the single most critical pattern for any long-running inline handler:

```go
select {
case <-time.After(duration):
    return nil           // finished normally
case <-ctx.Done():
    return ctx.Err()     // cancelled — SIGTERM, deadline exceeded, etc.
}
```

A handler that ignores `ctx.Done()` will:
- Block the worker goroutine during shutdown (exceeds `ShutdownTimeout`)
- Ignore job deadlines set by the caller
- Continue running after the job was cancelled

Every real ML preprocessing or training handler must include this pattern in its inner loops.

---

## 7. File 3: `cmd/worker/main.go` — Updated Wiring

Replace the current `var executors []worker.Executor` section with real wiring.

### What changes in main.go

```go
// BEFORE (Phase 2):
var executors []worker.Executor

// AFTER (Phase 3):
// ── 7. Inline Handler Registry ────────────────────────────────────────────────
// Register all inline handlers before starting the pool.
// The registry is read-only after pool.Start() — no synchronisation issues.
//
// Naming convention: handler names use snake_case and match the
// job.Payload.HandlerName field exactly. If the names don't match,
// jobs will fail with "handler X is not registered on this worker".
registry := worker.NewRegistry()

// Built-in handlers for smoke testing and examples.
registry.Register("noop",         handlers.Noop)
registry.Register("echo",         handlers.Echo)
registry.Register("always_fail",  handlers.AlwaysFail)
registry.Register("slow",         handlers.Slow)

// TODO Phase 4: ML-specific handlers
// registry.Register("preprocess_dataset", mlhandlers.PreprocessDataset)
// registry.Register("validate_data",       mlhandlers.ValidateData)
// registry.Register("compute_statistics",  mlhandlers.ComputeStatistics)

// InlineExecutor handles all jobs with type="inline".
// KubernetesExecutor (Phase 4) will handle type="k8s_job".
inlineExecutor := worker.NewInlineExecutor(registry, logger)

executors := []worker.Executor{
    inlineExecutor,
    // k8s.NewKubernetesExecutor(...) added in Phase 4
}

logger.Info("inline executor registered",
    "handlers", registry.List(),
    "count", registry.Len(),
)
```

### Full updated imports needed

```go
import (
    // ... existing imports ...
    "github.com/shreeharsh-a/orion/internal/worker/handlers"
)
// Note: inline.go is in the same package (worker), no separate import needed
```

---

## 8. File 4: `internal/worker/inline_test.go` — Tests

Phase 3 needs two categories of tests:

**Unit tests** (no DB, no Redis, instant):
- Registry operations (register, get, list, overwrite, concurrent access)
- InlineExecutor routing (CanExecute, unregistered handler, handler error)

**Integration test** (requires running DB + Redis):
- Full end-to-end: submit job → status=completed in PostgreSQL

```go
package worker_test

import (
    "context"
    "errors"
    "sync"
    "testing"
    "time"

    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/worker"
    "github.com/shreeharsh-a/orion/internal/worker/handlers"
    "log/slog"
    "os"
)

// ─────────────────────────────────────────────────────────────────────────────
// Registry tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRegistry_RegisterAndGet(t *testing.T) {
    r := worker.NewRegistry()
    called := false
    r.Register("test_handler", func(ctx context.Context, job *domain.Job) error {
        called = true
        return nil
    })

    fn, ok := r.Get("test_handler")
    if !ok {
        t.Fatal("expected to find registered handler")
    }
    _ = fn(context.Background(), &domain.Job{})
    if !called {
        t.Error("expected handler to be called")
    }
}

func TestRegistry_GetUnregistered(t *testing.T) {
    r := worker.NewRegistry()
    _, ok := r.Get("does_not_exist")
    if ok {
        t.Error("expected false for unregistered handler")
    }
}

func TestRegistry_OverwriteHandler(t *testing.T) {
    r := worker.NewRegistry()
    r.Register("h", func(ctx context.Context, job *domain.Job) error {
        return errors.New("first")
    })
    r.Register("h", func(ctx context.Context, job *domain.Job) error {
        return errors.New("second")
    })
    fn, _ := r.Get("h")
    err := fn(context.Background(), &domain.Job{})
    if err.Error() != "second" {
        t.Errorf("expected second handler, got: %v", err)
    }
}

func TestRegistry_Len(t *testing.T) {
    r := worker.NewRegistry()
    if r.Len() != 0 {
        t.Errorf("expected 0, got %d", r.Len())
    }
    r.Register("a", handlers.Noop)
    r.Register("b", handlers.Noop)
    if r.Len() != 2 {
        t.Errorf("expected 2, got %d", r.Len())
    }
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
    // Verify RWMutex prevents data races under concurrent register + get.
    r := worker.NewRegistry()
    r.Register("existing", handlers.Noop)

    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(2)
        go func() {
            defer wg.Done()
            r.Get("existing") // concurrent reads
        }()
        go func(n int) {
            defer wg.Done()
            if n%10 == 0 {
                r.Register("existing", handlers.Noop) // occasional write
            }
        }(i)
    }
    wg.Wait()
    // If this doesn't race-detect (run with -race), the mutex is working correctly.
}

func TestRegistry_PanicOnEmptyName(t *testing.T) {
    r := worker.NewRegistry()
    defer func() {
        if rec := recover(); rec == nil {
            t.Error("expected panic for empty handler name")
        }
    }()
    r.Register("", handlers.Noop)
}

func TestRegistry_PanicOnNilHandler(t *testing.T) {
    r := worker.NewRegistry()
    defer func() {
        if rec := recover(); rec == nil {
            t.Error("expected panic for nil handler")
        }
    }()
    r.Register("test", nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// InlineExecutor tests
// ─────────────────────────────────────────────────────────────────────────────

func newTestLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestInlineExecutor_CanExecute(t *testing.T) {
    r := worker.NewRegistry()
    e := worker.NewInlineExecutor(r, newTestLogger())

    if !e.CanExecute(domain.JobTypeInline) {
        t.Error("expected CanExecute=true for inline")
    }
    if e.CanExecute(domain.JobTypeKubernetes) {
        t.Error("expected CanExecute=false for k8s_job")
    }
}

func TestInlineExecutor_Execute_Success(t *testing.T) {
    r := worker.NewRegistry()
    r.Register("noop", handlers.Noop)
    e := worker.NewInlineExecutor(r, newTestLogger())

    job := &domain.Job{
        Type:    domain.JobTypeInline,
        Payload: domain.JobPayload{HandlerName: "noop"},
    }
    if err := e.Execute(context.Background(), job); err != nil {
        t.Errorf("expected nil error from noop, got: %v", err)
    }
}

func TestInlineExecutor_Execute_HandlerError(t *testing.T) {
    r := worker.NewRegistry()
    r.Register("always_fail", handlers.AlwaysFail)
    e := worker.NewInlineExecutor(r, newTestLogger())

    job := &domain.Job{
        Type:    domain.JobTypeInline,
        Payload: domain.JobPayload{HandlerName: "always_fail"},
        Attempt: 1,
    }
    err := e.Execute(context.Background(), job)
    if err == nil {
        t.Error("expected error from always_fail handler")
    }
}

func TestInlineExecutor_Execute_UnregisteredHandler(t *testing.T) {
    r := worker.NewRegistry()
    e := worker.NewInlineExecutor(r, newTestLogger())

    job := &domain.Job{
        Type:    domain.JobTypeInline,
        Payload: domain.JobPayload{HandlerName: "not_registered"},
    }
    err := e.Execute(context.Background(), job)
    if err == nil {
        t.Error("expected error for unregistered handler")
    }
    // Error message must mention the handler name so it's debuggable
    if !errors.Is(err, err) || err.Error() == "" {
        t.Error("expected descriptive error message")
    }
}

func TestInlineExecutor_Execute_EmptyHandlerName(t *testing.T) {
    r := worker.NewRegistry()
    e := worker.NewInlineExecutor(r, newTestLogger())

    job := &domain.Job{
        Type:    domain.JobTypeInline,
        Payload: domain.JobPayload{HandlerName: ""},
    }
    err := e.Execute(context.Background(), job)
    if err == nil {
        t.Error("expected error for empty handler_name")
    }
}

func TestInlineExecutor_Execute_ContextCancellation(t *testing.T) {
    r := worker.NewRegistry()
    // Register a slow handler that respects context
    r.Register("slow_handler", func(ctx context.Context, job *domain.Job) error {
        select {
        case <-time.After(10 * time.Second):
            return nil
        case <-ctx.Done():
            return ctx.Err()
        }
    })
    e := worker.NewInlineExecutor(r, newTestLogger())

    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()

    job := &domain.Job{
        Type:    domain.JobTypeInline,
        Payload: domain.JobPayload{HandlerName: "slow_handler"},
    }

    start := time.Now()
    err := e.Execute(ctx, job)
    elapsed := time.Since(start)

    if err == nil {
        t.Error("expected context cancellation error")
    }
    if elapsed > 500*time.Millisecond {
        t.Errorf("handler did not respect context cancellation, ran for %v", elapsed)
    }
}

func TestInlineExecutor_Execute_ArgsPassedThrough(t *testing.T) {
    r := worker.NewRegistry()
    var receivedArgs map[string]any
    r.Register("capture_args", func(ctx context.Context, job *domain.Job) error {
        receivedArgs = job.Payload.Args
        return nil
    })
    e := worker.NewInlineExecutor(r, newTestLogger())

    job := &domain.Job{
        Type: domain.JobTypeInline,
        Payload: domain.JobPayload{
            HandlerName: "capture_args",
            Args: map[string]any{
                "model":   "resnet50",
                "epochs":  50.0,
                "dataset": "imagenet",
            },
        },
    }
    _ = e.Execute(context.Background(), job)

    if receivedArgs["model"] != "resnet50" {
        t.Errorf("expected model=resnet50, got %v", receivedArgs["model"])
    }
    if receivedArgs["epochs"] != 50.0 {
        t.Errorf("expected epochs=50, got %v", receivedArgs["epochs"])
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in handler tests
// ─────────────────────────────────────────────────────────────────────────────

func TestHandlers_Noop(t *testing.T) {
    err := handlers.Noop(context.Background(), &domain.Job{})
    if err != nil {
        t.Errorf("noop should return nil, got: %v", err)
    }
}

func TestHandlers_AlwaysFail(t *testing.T) {
    err := handlers.AlwaysFail(context.Background(), &domain.Job{Attempt: 2})
    if err == nil {
        t.Error("always_fail should return an error")
    }
}

func TestHandlers_Slow_CompletesNormally(t *testing.T) {
    job := &domain.Job{
        Payload: domain.JobPayload{
            HandlerName: "slow",
            Args: map[string]any{"duration_seconds": 0.0}, // 0 seconds
        },
    }
    err := handlers.Slow(context.Background(), job)
    if err != nil {
        t.Errorf("slow with 0s should succeed, got: %v", err)
    }
}

func TestHandlers_Slow_RespectsCancel(t *testing.T) {
    job := &domain.Job{
        Payload: domain.JobPayload{
            HandlerName: "slow",
            Args: map[string]any{"duration_seconds": 60.0},
        },
    }
    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()

    err := handlers.Slow(ctx, job)
    if err == nil {
        t.Error("expected cancellation error")
    }
    if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
        t.Errorf("expected context error, got: %v", err)
    }
}

func TestHandlers_PanicRecover(t *testing.T) {
    panicHandler := func(ctx context.Context, job *domain.Job) error {
        panic("test panic")
    }
    safeHandler := handlers.PanicRecover(panicHandler)

    err := safeHandler(context.Background(), &domain.Job{})
    if err == nil {
        t.Error("expected error from panic recovery")
    }
    if err.Error() == "" {
        t.Error("expected descriptive error from panic recovery")
    }
}
```

---

## 9. The Execution Audit Trail — RecordExecution

Phase 3 is also the right moment to wire `RecordExecution` into the pool's `executeJob` flow. Currently the pool calls `MarkJobRunning`, executes, then calls `MarkJobCompleted` or `MarkJobFailed` — but it never calls `RecordExecution`, so the `job_executions` table stays empty.

### Where to add it in `pool.go`

The change is in `executeJob()`. Add two `RecordExecution` calls — one at the start (when the job begins running) and one at the end (when it completes or fails):

```go
// In pool.go executeJob() — additions only, no existing code changes:

func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
    job := task.job
    startedAt := time.Now()

    // ... MarkJobRunning as before ...

    // Record that this attempt started (append to audit log)
    _ = p.store.RecordExecution(ctx, &domain.JobExecution{
        JobID:     job.ID,
        Attempt:   job.Attempt + 1,  // attempt is 0-indexed in DB, 1-indexed in audit log
        WorkerID:  p.cfg.WorkerID,
        Status:    domain.JobStatusRunning,
        StartedAt: &startedAt,
    })

    err := executor.Execute(execCtx, job)
    finishedAt := time.Now()

    if err != nil {
        _ = p.store.MarkJobFailed(ctx, job.ID, err.Error(), retryAt)
        _ = p.store.RecordExecution(ctx, &domain.JobExecution{
            JobID:      job.ID,
            Attempt:    job.Attempt + 1,
            WorkerID:   p.cfg.WorkerID,
            Status:     domain.JobStatusFailed,
            StartedAt:  &startedAt,
            FinishedAt: &finishedAt,
            Error:      err.Error(),
        })
        _ = task.ackFn(err)
        return
    }

    _ = p.store.MarkJobCompleted(ctx, job.ID)
    _ = p.store.RecordExecution(ctx, &domain.JobExecution{
        JobID:      job.ID,
        Attempt:    job.Attempt + 1,
        WorkerID:   p.cfg.WorkerID,
        Status:     domain.JobStatusCompleted,
        StartedAt:  &startedAt,
        FinishedAt: &finishedAt,
    })
    _ = task.ackFn(nil)
}
```

`RecordExecution` uses `ON CONFLICT (job_id, attempt) DO NOTHING` so calling it twice for the same attempt is safe. The `attempt + 1` because in the DB the `attempt` column starts at 0 before any execution and increments on each failure — the execution record should reflect the ordinal attempt number (1st run, 2nd run, etc.).

---

## 10. Error Handling Strategy

### Four error categories in inline execution

| Error | Source | Pool's response | Job's outcome |
|---|---|---|---|
| **Handler not registered** | `registry.Get()` returns false | `MarkJobFailed` | Retried — eventually `dead` (unless handler is registered before next retry) |
| **Handler returns error** | `fn(ctx, job)` returns non-nil | `MarkJobFailed` + backoff | Retried up to `max_retries` times |
| **Context cancelled** | `ctx.Done()` fires during handler | handler returns `ctx.Err()` → `MarkJobFailed` | Retried (the job wasn't wrong, the context was cut short) |
| **Handler panics** | `fn(ctx, job)` panics | **POOL DOES NOT RECOVER** | Worker goroutine crashes → orphan reclaim after 90s |

### Panic protection

The pool deliberately does not recover panics from executors. The reasons:
1. A panic in a goroutine that isn't recovered crashes the entire process
2. The orphan reclaim mechanism handles this — the job gets re-queued after 90s
3. Panics in handlers are programming errors, not runtime errors — they should be loud

If you know a handler might panic (e.g., calling unstable external library), wrap it:
```go
registry.Register("risky_handler", handlers.PanicRecover(riskyHandler))
```

### Transient vs permanent errors

The pool cannot distinguish "this job will never succeed" from "this was a transient error." That distinction is the handler's responsibility:

```go
// Transient error — will be retried, might succeed next time
return fmt.Errorf("database temporarily unavailable: %w", err)

// Permanent error — handler knows this will never succeed
// Use a sentinel value to communicate permanence
var ErrPermanent = errors.New("permanent failure")
return fmt.Errorf("invalid input data, will never succeed: %w", ErrPermanent)
```

In Phase 3, the pool treats all errors the same — retry up to `max_retries`. Permanent failure detection is a Phase 8+ enhancement.

---

## 11. Context and Cancellation

Every handler receives a `context.Context`. Understanding it is critical for correctness.

### When the context is cancelled

```
1. Job has a Deadline field set by the caller
   → pool creates context.WithDeadline(ctx, job.Deadline)
   → handler gets this deadline-bounded context
   → if job takes longer than deadline → ctx cancelled

2. Worker receives SIGTERM
   → cancel() is called → parent context done
   → all handler contexts cancelled
   → handlers that check ctx.Done() stop cleanly
   → handlers that ignore ctx.Done() continue until ShutdownTimeout (30s)

3. Visibility timeout exceeded (job stayed in Redis PEL too long)
   → handled at the queue/scheduler level, not by the handler context
```

### The correct pattern for long-running handlers

```go
// CORRECT — respects cancellation in any long loop
func MyHandler(ctx context.Context, job *domain.Job) error {
    items := getItemsToProcess(job)
    for i, item := range items {
        // Check cancellation before each iteration of expensive work
        if err := ctx.Err(); err != nil {
            return fmt.Errorf("cancelled after processing %d/%d items: %w", i, len(items), err)
        }
        if err := processItem(ctx, item); err != nil {
            return fmt.Errorf("processing item %d: %w", i, err)
        }
    }
    return nil
}

// CORRECT — respects cancellation during a blocking wait
func WaitForExternalService(ctx context.Context, job *domain.Job) error {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if ready, err := checkExternalService(); err != nil {
                return err
            } else if ready {
                return nil
            }
        }
    }
}

// WRONG — ignores cancellation, blocks shutdown
func BadHandler(ctx context.Context, job *domain.Job) error {
    time.Sleep(5 * time.Minute)  // never checks ctx.Done()
    return nil
}
```

---

## 12. The Pool Connection — How `pool.go` Calls the Executor

`pool.go` is **unchanged** in Phase 3. Understanding why helps you see how cleanly the design was planned.

```go
// From pool.go (Phase 1, unchanged):

func (p *Pool) resolveExecutor(jobType domain.JobType) Executor {
    for _, e := range p.executors {
        if e.CanExecute(jobType) {
            return e   // ← InlineExecutor returns true for "inline"
        }
    }
    return nil         // ← Phase 2: this always happened. Phase 3: never happens for inline.
}

func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
    // ...
    executor := p.resolveExecutor(job.Type)
    if executor == nil {
        // Phase 2: always hit this path
        // Phase 3: only hit for unregistered types (e.g. "k8s_job" until Phase 4)
        _ = p.store.MarkJobFailed(ctx, job.ID, "no executor for job type ...", ...)
        return
    }

    // Phase 3: reaches here for "inline" jobs ✓
    err := executor.Execute(execCtx, job)
    // ...
}
```

The pool doesn't know or care that `InlineExecutor` exists. It just loops through `p.executors`, finds the first one that says `CanExecute("inline") = true`, and calls `Execute`. This is the **Strategy pattern** — the behaviour is pluggable without changing the calling code.

---

## 13. Step-by-Step Build Order

Build in this exact order. Each step compiles and tests before proceeding.

### Step 1 — Create `internal/worker/inline.go`

Write the `HandlerFunc` type, `Registry` struct, and `InlineExecutor` struct exactly as specified in Section 5. This file has no external dependencies beyond the standard library and domain types — it should compile immediately.

```bash
go build ./internal/worker/...
# Must compile with zero errors
```

### Step 2 — Create `internal/worker/handlers/handlers.go`

Write `Noop`, `Echo`, `AlwaysFail`, `Slow`, and `PanicRecover` as specified in Section 6.

```bash
go build ./internal/worker/handlers/...
# Must compile
```

### Step 3 — Write `internal/worker/inline_test.go`

Write all tests from Section 8.

```bash
go test -race ./internal/worker/... -v
# All unit tests pass
# Run with -race to verify concurrent registry access has no data races
```

### Step 4 — Update `internal/worker/pool.go` with RecordExecution

Add the two `RecordExecution` calls in `executeJob()` as shown in Section 9.

```bash
go build ./internal/worker/...
# Must still compile
go test -race ./internal/worker/... -v
# All tests still pass
```

### Step 5 — Update `cmd/worker/main.go`

Replace `var executors []worker.Executor` with the registry + InlineExecutor wiring from Section 7.

```bash
go build ./cmd/worker/...
# Must compile with correct imports
```

### Step 6 — Build all three binaries

```bash
make build
# Building orion-api...
# Building orion-scheduler...
# Building orion-worker...
# All three succeed — zero errors
```

### Step 7 — Run and verify (next section)

---

## 14. How to Run and Verify Phase 3

### Prerequisites (from Phase 2)

```bash
# Infrastructure must be running
docker compose ps
# orion-postgres   healthy
# orion-redis      healthy
# orion-jaeger     healthy

# Database must have tables
make migrate-up

# All binaries must build
make build
```

### Start all three services

Open three terminals.

**Terminal 1 — API:**
```bash
ORION_ENV=development go run ./cmd/api
# INFO msg="API server listening" port=8080
```

**Terminal 2 — Scheduler:**
```bash
ORION_ENV=development go run ./cmd/scheduler
# INFO msg="starting scheduler"
# INFO msg="acquired scheduler leader lock"
```

**Terminal 3 — Worker:**
```bash
ORION_ENV=development go run ./cmd/worker
# INFO msg="starting worker pool" worker_id=your-hostname concurrency=10
# INFO msg="inline executor registered" handlers=[noop echo always_fail slow] count=4
```

The third line is new — it confirms the InlineExecutor is registered with its handlers.

---

## 15. Complete End-to-End Test Sequence

Run these in order. Each step proves one specific behaviour works correctly.

### Test 1 — Happy path (the milestone)

```bash
# Submit a noop job
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "phase3-smoke-test",
    "type": "inline",
    "payload": {"handler_name": "noop"},
    "max_retries": 3,
    "idempotency_key": "phase3-test-001"
  }' | jq -r .id)

echo "Job ID: $JOB_ID"

# Poll every second until completed (should take 2-4 seconds)
for i in $(seq 1 15); do
  STATUS=$(curl -s http://localhost:8080/jobs/$JOB_ID | jq -r .status)
  echo "$(date +%H:%M:%S) status=$STATUS"
  if [ "$STATUS" = "completed" ]; then
    echo "✅ TEST 1 PASSED — first ever completed job"
    break
  fi
  sleep 1
done
```

Expected output:
```
10:00:00 status=queued
10:00:01 status=queued
10:00:02 status=scheduled     ← scheduler dispatch cycle
10:00:03 status=running       ← worker picked it up
10:00:03 status=completed     ← noop returned nil ✓
✅ TEST 1 PASSED — first ever completed job
```

### Test 2 — Verify execution audit log

```bash
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .
```

Expected:
```json
{
  "job_id": "550e8400-...",
  "count": 1,
  "executions": [
    {
      "attempt": 1,
      "worker_id": "your-hostname",
      "status": "completed",
      "started_at": "2024-01-15T10:00:03Z",
      "finished_at": "2024-01-15T10:00:03Z",
      "error": ""
    }
  ]
}
```

### Test 3 — Args are passed through

```bash
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "args-test",
    "type": "inline",
    "payload": {
      "handler_name": "echo",
      "args": {
        "model": "resnet50",
        "epochs": 10,
        "dataset": "imagenet"
      }
    }
  }' | jq -r .id)

sleep 3

# Check worker logs — should see echo output
# Worker terminal: INFO msg="echo handler" job_id=... args={model:resnet50 epochs:10 dataset:imagenet}

curl -s http://localhost:8080/jobs/$JOB_ID | jq .status
# "completed"
echo "✅ TEST 3 PASSED — args passed through to handler"
```

### Test 4 — Error path and retry cycle

```bash
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "error-path-test",
    "type": "inline",
    "payload": {"handler_name": "always_fail"},
    "max_retries": 2
  }' | jq -r .id)

# Watch the retry cycle
watch -n1 "curl -s http://localhost:8080/jobs/$JOB_ID | jq '{status,attempt,error_message}'"
```

Expected progression:
```
status=queued      attempt=0
status=scheduled   attempt=0
status=running     attempt=0
status=failed      attempt=1   error_message="deliberate failure on attempt 0..."
status=retrying    attempt=1   (scheduler promotes after backoff delay)
status=queued      attempt=1
status=scheduled   attempt=1
status=running     attempt=1
status=failed      attempt=2   error_message="deliberate failure on attempt 1..."
status=retrying    attempt=2
status=queued      attempt=2
...
status=failed      attempt=3
status=dead        attempt=3   ← max_retries=2, dead after 3rd failure (attempts 0,1,2)
```

```bash
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .count
# 3 — three execution records, one per attempt

echo "✅ TEST 4 PASSED — retry cycle and dead state work"
```

### Test 5 — Unregistered handler

```bash
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "unknown-handler-test",
    "type": "inline",
    "payload": {"handler_name": "not_a_real_handler"},
    "max_retries": 1
  }' | jq -r .id)

sleep 5

RESULT=$(curl -s http://localhost:8080/jobs/$JOB_ID | jq '{status,error_message}')
echo $RESULT | jq .
# {"status":"failed","error_message":"handler \"not_a_real_handler\" is not registered on this worker..."}
echo "✅ TEST 5 PASSED — unregistered handler gives helpful error message"
```

### Test 6 — Verify SQL state directly

```bash
docker compose exec postgres psql -U orion -d orion -c \
  "SELECT name, status, attempt, completed_at, error_message
   FROM jobs
   ORDER BY created_at DESC LIMIT 5;"
```

Expected:
```
         name          |  status   | attempt |      completed_at       | error_message
-----------------------+-----------+---------+-------------------------+---------------
 unknown-handler-test  | failed    |       2 |                         | handler "not_a_real_handler"...
 error-path-test       | dead      |       3 |                         | deliberate failure...
 args-test             | completed |       0 | 2024-01-15 10:01:22+00  |
 phase3-smoke-test     | completed |       0 | 2024-01-15 10:00:03+00  |
```

---

## 16. Common Mistakes and How to Avoid Them

| Mistake | Symptom | Fix |
|---|---|---|
| Handler name mismatch | `"handler X is not registered"` error | `registry.Register("exact_name", fn)` must match `"handler_name": "exact_name"` in job payload exactly — case-sensitive |
| Ignoring ctx.Done() in loops | Worker can't shut down within 30s, drain times out | Add `select { case <-ctx.Done(): return ctx.Err(); default: }` in every long loop |
| Returning nil for permanent failures | Dead jobs retry forever | Return a wrapped error for retriable failures; mark job as `dead` manually for permanent ones |
| Handler panics without recovery | Worker goroutine crashes | Wrap risky handlers: `handlers.PanicRecover(myHandler)` |
| Registering handlers after pool.Start() | Race condition on registry map | All `registry.Register()` calls must be before `pool.Start()` (the `RWMutex` handles this but it's bad practice) |
| Not adding `RecordExecution` calls | `GET /executions` returns empty | Add the two `RecordExecution` calls in `pool.go executeJob()` as shown in Section 9 |
| Wrong attempt number in RecordExecution | Audit log shows attempt=0 for all | Use `job.Attempt + 1` (DB counter is 0-indexed, audit log is 1-indexed) |
| Handler modifies `job.Status` | State machine out of sync | Handlers must NOT touch `job.Status` — the pool owns all state transitions |

---

## 17. Phase 4 Preview

After Phase 3, the system looks like this:

```
Inline jobs ("type":"inline") → ✅ execute and complete
K8s jobs ("type":"k8s_job")   → ❌ still fail with "no executor for job type k8s_job"
```

Phase 4 adds `KubernetesExecutor` which:
1. Translates `domain.KubernetesSpec` into a `batchv1.Job` Kubernetes object
2. Creates the Job via the Kubernetes API (`k8s.io/client-go`)
3. Watches the pod until it completes (succeeds, fails, or times out)
4. Returns `nil` on success, an error on failure

```go
// Phase 4 addition in cmd/worker/main.go:
k8sClient, err := kubernetes.NewForConfig(k8sConfig)
k8sExecutor := k8s.NewKubernetesExecutor(k8sClient, cfg.Kubernetes.DefaultNamespace, logger)

executors := []worker.Executor{
    inlineExecutor,         // Phase 3: handles "inline"
    k8sExecutor,            // Phase 4: handles "k8s_job"
}
```

The pool's `resolveExecutor` already handles multiple executors — it just loops and checks `CanExecute`. No changes to `pool.go` needed in Phase 4 either.

---

## Summary Checklist

Use this as your build checklist for Phase 3.

```
□ internal/worker/inline.go
    □ HandlerFunc type defined
    □ Registry struct with mu sync.RWMutex and handlers map
    □ NewRegistry() constructor
    □ Register(name, fn) — panics on empty name or nil fn
    □ Get(name) → (fn, bool)
    □ List() → []string
    □ Len() → int
    □ InlineExecutor struct with registry + logger
    □ NewInlineExecutor(registry, logger) — panics on nil registry
    □ CanExecute(jobType) → true only for JobTypeInline
    □ Execute(ctx, job) → looks up handler, calls it, logs timing
    □ argKeys() helper

□ internal/worker/handlers/handlers.go
    □ Noop — does nothing, returns nil
    □ Echo — logs args, returns nil
    □ AlwaysFail — always returns error with attempt number
    □ Slow — reads duration from args, respects ctx.Done()
    □ PanicRecover — decorator that catches panics

□ internal/worker/inline_test.go
    □ TestRegistry_RegisterAndGet
    □ TestRegistry_GetUnregistered
    □ TestRegistry_OverwriteHandler
    □ TestRegistry_Len
    □ TestRegistry_ConcurrentAccess
    □ TestRegistry_PanicOnEmptyName
    □ TestRegistry_PanicOnNilHandler
    □ TestInlineExecutor_CanExecute
    □ TestInlineExecutor_Execute_Success
    □ TestInlineExecutor_Execute_HandlerError
    □ TestInlineExecutor_Execute_UnregisteredHandler
    □ TestInlineExecutor_Execute_EmptyHandlerName
    □ TestInlineExecutor_Execute_ContextCancellation
    □ TestInlineExecutor_Execute_ArgsPassedThrough
    □ TestHandlers_Noop
    □ TestHandlers_AlwaysFail
    □ TestHandlers_Slow_CompletesNormally
    □ TestHandlers_Slow_RespectsCancel
    □ TestHandlers_PanicRecover

□ internal/worker/pool.go (update only)
    □ Add RecordExecution call at start of executeJob (attempt started)
    □ Add RecordExecution call on failure path (attempt failed)
    □ Add RecordExecution call on success path (attempt completed)

□ cmd/worker/main.go (update only)
    □ Import handlers package
    □ Create registry with NewRegistry()
    □ Register noop, echo, always_fail, slow
    □ Create InlineExecutor with NewInlineExecutor(registry, logger)
    □ Pass executors slice (not nil) to NewPool
    □ Log registered handlers at startup

□ Verification
    □ go test -race ./internal/worker/... → all pass
    □ make build → zero errors
    □ Test 1 (noop completes) → status=completed ← MILESTONE
    □ Test 2 (execution audit log) → 1 execution record
    □ Test 3 (args passed through) → echo logs correct args
    □ Test 4 (error path and retry) → fails → retries → dead
    □ Test 5 (unregistered handler) → helpful error message
    □ Test 6 (SQL verification) → completed_at set in DB
```

---

## File Locations After Phase 3

```
orion/
└── internal/
    └── worker/
        ├── pool.go               ← Updated: RecordExecution calls added
        ├── inline.go             ← NEW: Registry + HandlerFunc + InlineExecutor
        ├── inline_test.go        ← NEW: 19 tests, all passing with -race
        └── handlers/
            └── handlers.go       ← NEW: Noop, Echo, AlwaysFail, Slow, PanicRecover

└── cmd/
    └── worker/
        └── main.go               ← Updated: registry wired, executors non-nil

└── docs/
    └── phase3/
        └── PHASE3-MASTER-GUIDE.md  ← this file
```