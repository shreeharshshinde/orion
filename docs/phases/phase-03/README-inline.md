# `internal/worker/inline.go`
## InlineExecutor — Running Go Functions as Jobs

---

## What this file does

This file adds the ability for Orion to run Go functions as jobs. It is the core of Phase 3 and contains exactly three things:

| Type | Role |
|---|---|
| `HandlerFunc` | The function signature every inline handler must implement |
| `Registry` | A thread-safe map from handler name → HandlerFunc |
| `InlineExecutor` | Implements `worker.Executor`, bridges the pool to the registry |

---

## How it connects to the rest of the system

```
cmd/worker/main.go
    creates Registry
    registers "noop", "echo", etc.
    creates InlineExecutor(registry)
    passes []Executor{inlineExecutor} to NewPool()

pool.go (unchanged)
    resolveExecutor("inline") → loops executors → InlineExecutor.CanExecute("inline") = true
    InlineExecutor.Execute(ctx, job) called

inline.go (this file)
    registry.Get(job.Payload.HandlerName) → HandlerFunc
    fn(ctx, job) called
    error / nil returned to pool
```

---

## HandlerFunc

```go
type HandlerFunc func(ctx context.Context, job *domain.Job) error
```

Every handler you register must match this signature. The two parameters give you everything you need:

**`ctx`** — the execution context. It will be cancelled when:
- The job's `deadline` field expires (context.WithDeadline set by pool)
- The worker receives SIGTERM (cancel() called on parent context)

You **must** check `ctx.Done()` in any long loop or blocking wait. See the `Slow` handler in `handlers/handlers.go` for the correct pattern.

**`job`** — the full job record. The most useful fields for handlers:
```go
job.Payload.HandlerName  // "preprocess_dataset" — which handler is being called
job.Payload.Args         // map[string]any{"epochs": 10.0, "model": "resnet50"}
job.Attempt              // 0 on first run, 1 on first retry, etc.
job.ID                   // UUID — useful for namespacing output files, logs
job.Name                 // human-readable name from submission
```

**Return values:**
- `nil` → job marked `completed`, Redis message ACKed (removed from PEL forever)
- `error` → job marked `failed`, retry scheduled with backoff, Redis message NACKed

---

## Registry

```go
registry := worker.NewRegistry()
registry.Register("my_handler", myHandlerFunc)
fn, ok := registry.Get("my_handler")
names := registry.List()   // ["my_handler"]
count := registry.Len()    // 1
```

### Thread safety

The registry uses `sync.RWMutex`:
- **Write** (`Register`): exclusive lock — blocks all readers and writers
- **Read** (`Get`, `List`, `Len`): shared lock — unlimited concurrent readers

At runtime, workers only call `Get` (read path). Register is only called at startup. Zero lock contention under normal operation.

### Naming convention

Handler names must exactly match `job.Payload.HandlerName` in submitted jobs. They are **case-sensitive**:
```
registered: "preprocess_dataset"
submitted:  "preprocess_Dataset"  ← WRONG — handler not found
submitted:  "preprocess_dataset"  ← correct
```

Use `snake_case` consistently.

### Panic behaviour

```go
registry.Register("", myFn)   // panics: "handler name must not be empty"
registry.Register("h", nil)   // panics: "handler function must not be nil"
```

These are startup-time programming errors. Panicking immediately gives a clear stack trace pointing to the misconfiguration, rather than failing silently at job execution time.

---

## InlineExecutor

```go
executor := worker.NewInlineExecutor(registry, logger)

// pool calls these:
executor.CanExecute("inline")   // → true  (handles it)
executor.CanExecute("k8s_job")  // → false (KubernetesExecutor handles that)
executor.Execute(ctx, job)      // → calls registry.Get(job.Payload.HandlerName), then fn(ctx, job)
```

### What Execute does step by step

```
1. Read job.Payload.HandlerName
   → if empty: return error

2. registry.Get(handlerName)
   → if not found: return descriptive error (lists registered names for debugging)

3. Log debug: "calling inline handler" (job_id, handler, attempt, arg_keys)

4. fn(ctx, job)  ← your code runs here

5. Log result (success with elapsed_ms, or warn with error)

6. Return fn's return value to pool
```

The executor never touches PostgreSQL, Redis, or job status. The pool owns all state transitions. The executor only calls the function and returns the result.

### Why log arg keys but not values?

```go
e.logger.Debug("calling inline handler", "arg_keys", argKeys(job.Payload.Args))
```

`argKeys()` returns `["dataset", "epochs", "model"]`, not `{"dataset":"imagenet","epochs":10}`.

Job args may contain API tokens, S3 credentials, or training data paths. Logging keys gives useful debug context (did the args arrive?) without risking credential leaks in log aggregators.

---

## Adding a new handler

```go
// 1. Write the handler (in handlers/ or your own package)
func MyHandler(ctx context.Context, job *domain.Job) error {
    // Read args
    inputPath, _ := job.Payload.Args["input_path"].(string)
    outputPath, _ := job.Payload.Args["output_path"].(string)

    // Check cancellation before long work
    if err := ctx.Err(); err != nil {
        return err
    }

    // Do the work
    return processData(ctx, inputPath, outputPath)
}

// 2. Register in cmd/worker/main.go
registry.Register("my_handler", MyHandler)

// 3. Submit jobs that use it
curl -X POST /jobs -d '{
  "type": "inline",
  "payload": {
    "handler_name": "my_handler",
    "args": {"input_path": "s3://bucket/data.csv", "output_path": "s3://bucket/out/"}
  }
}'
```

---

## File location

```
orion/
└── internal/
    └── worker/
        ├── pool.go        ← unchanged — already handles []Executor correctly
        ├── inline.go      ← you are here
        ├── inline_test.go ← 22 tests, run with: go test -race ./internal/worker/...
        └── handlers/
            └── handlers.go
```