# `internal/worker/handlers/handlers.go`
## Built-in Handlers

---

## What this package is

Four ready-to-use `HandlerFunc` implementations plus one decorator. They serve as smoke-test utilities and code examples — not production ML logic.

---

## The four handlers

### `Noop` — the smoke test

```go
registry.Register("noop", handlers.Noop)
```

Does nothing. Always returns `nil`. Use it to verify the entire pipeline is working before writing a single line of business logic.

```bash
curl -X POST /jobs -d '{"type":"inline","payload":{"handler_name":"noop"}}'
# Watch status become "completed" — proves every piece of infrastructure works
```

If a noop job reaches `status=completed`, you know:
- PostgreSQL schema and migrations are correct
- Redis Streams consumer group is set up
- Scheduler dispatch loop is running
- Worker pool is dequeuing and executing
- State transitions (queued→scheduled→running→completed) all work
- Audit log (`job_executions`) is written correctly

### `Echo` — the args inspector

```go
registry.Register("echo", handlers.Echo)
```

Logs all job fields including `payload.args`. Always returns `nil`.

```bash
curl -X POST /jobs -d '{
  "type": "inline",
  "payload": {"handler_name": "echo", "args": {"model": "resnet50", "epochs": 10}}
}'
# Worker log: INFO msg="echo handler" args=map[epochs:10 model:resnet50]
```

Use when you need to verify args are correctly passed from HTTP submission through to handler execution.

### `AlwaysFail` — the retry tester

```go
registry.Register("always_fail", handlers.AlwaysFail)
```

Always returns an error containing the attempt number. Use to exercise the full retry cycle.

```bash
curl -X POST /jobs -d '{
  "type": "inline",
  "payload": {"handler_name": "always_fail"},
  "max_retries": 2
}'
# watch -n1 'curl -s /jobs/$ID | jq "{status,attempt}"'
# status=failed  attempt=1
# status=retrying attempt=1  → status=queued → status=failed attempt=2
# status=retrying attempt=2  → status=queued → status=failed attempt=3
# status=dead    attempt=3   ← max_retries=2, dies after 3rd failure
```

Verify the audit log has 3 rows:
```bash
curl /jobs/$ID/executions | jq .count
# 3
```

### `Slow` — the context-cancellation reference

```go
registry.Register("slow", handlers.Slow)
```

Sleeps for `duration_seconds` (default 5s) while correctly checking `ctx.Done()`.

```bash
# Test graceful shutdown
curl -X POST /jobs -d '{
  "type": "inline",
  "payload": {"handler_name": "slow", "args": {"duration_seconds": 30}}
}'
# Then send SIGTERM to the worker
# Worker should wait up to ShutdownTimeout (30s) before force-exiting
```

```bash
# Test job deadline
curl -X POST /jobs -d '{
  "type": "inline",
  "payload": {"handler_name": "slow", "args": {"duration_seconds": 60}},
  "deadline": "2024-01-15T10:00:10Z"
}'
# Job should fail with context.DeadlineExceeded and be retried
```

**The pattern to copy into your own handlers:**

```go
func MyLongHandler(ctx context.Context, job *domain.Job) error {
    // For a blocking wait:
    select {
    case <-time.After(longDuration):
        return nil
    case <-ctx.Done():
        return ctx.Err()  // always return ctx.Err(), not a custom error
    }

    // For a loop over items:
    for _, item := range items {
        if err := ctx.Err(); err != nil {
            return err  // stop the loop if context is cancelled
        }
        process(item)
    }
}
```

---

## `PanicRecover` — the safety decorator

```go
registry.Register("risky_handler", handlers.PanicRecover(myRiskyHandler))
```

Wraps any `HandlerFunc` and catches panics, converting them to errors.

**Without it:** a panicking handler crashes the worker goroutine permanently. The job stays in the Redis PEL and is reclaimed after 90s, but the goroutine is gone.

**With it:** panic is caught, logged with the panic value and job ID, and returned as an error. The pool marks the job failed and retries it normally.

```go
// Example: calling a third-party library you don't fully trust
registry.Register("unstable_parser", handlers.PanicRecover(func(ctx context.Context, job *domain.Job) error {
    data := job.Payload.Args["data"].(string)
    result := unstableThirdPartyParse(data) // might panic on unexpected input
    return saveResult(ctx, result)
}))
```

**When to use:** third-party libraries, unsafe type assertions on user-controlled data, CGo calls.

**When NOT to use:** your own handlers — fix the root cause instead of masking it.

---

## Writing production handlers — the pattern

Every real ML handler follows this skeleton:

```go
// internal/worker/handlers/ml_handlers.go
package handlers

import (
    "context"
    "fmt"

    "github.com/shreeharsh-a/orion/internal/domain"
)

func PreprocessDataset(ctx context.Context, job *domain.Job) error {
    // 1. Extract and validate args
    inputBucket, ok := job.Payload.Args["input_bucket"].(string)
    if !ok || inputBucket == "" {
        return fmt.Errorf("input_bucket arg is required and must be a string")
    }
    outputBucket, _ := job.Payload.Args["output_bucket"].(string)

    // 2. Log start (include attempt for retry debugging)
    slog.Info("preprocess_dataset starting",
        "job_id", job.ID,
        "attempt", job.Attempt,
        "input_bucket", inputBucket,
    )

    // 3. Do work, checking ctx.Done() in any long loops
    files, err := listFiles(ctx, inputBucket)
    if err != nil {
        return fmt.Errorf("listing input files: %w", err)
    }

    for i, file := range files {
        // Check cancellation before each file
        if err := ctx.Err(); err != nil {
            return fmt.Errorf("cancelled after %d/%d files: %w", i, len(files), err)
        }
        if err := processFile(ctx, file, outputBucket); err != nil {
            return fmt.Errorf("processing file %s: %w", file, err)
        }
    }

    // 4. Return nil on success — pool handles the rest
    return nil
}
```

---

## File location

```
orion/
└── internal/
    └── worker/
        └── handlers/
            └── handlers.go   ← you are here
```

Register in `cmd/worker/main.go`:
```go
registry.Register("noop",        handlers.Noop)
registry.Register("echo",        handlers.Echo)
registry.Register("always_fail", handlers.AlwaysFail)
registry.Register("slow",        handlers.Slow)
```