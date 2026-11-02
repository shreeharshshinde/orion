# Phase 10 — Observability Hardening

## W3C Trace Context Propagation Across the Redis Streams Queue Boundary

### Problem

Orion's distributed tracing worked end-to-end within HTTP and gRPC, but
**broke at the Redis Streams queue boundary**. Every job enqueued by the
scheduler or API started a new root trace on the worker side, producing two
disconnected traces in Jaeger:

```
Trace 1 (Scheduler/API)
└── scheduler.dispatch_job
      └── (enqueues job to Redis — context lost)

Trace 2 (Worker — orphaned root)
└── worker.execute_job
      └── ...
```

To find both sides of a single job execution you had to manually search by
`job.id`. There was no causal link between dispatch and execution.

---

### Solution

We propagate the [W3C Trace Context](https://www.w3.org/TR/trace-context/)
(`traceparent` + `tracestate`) through the Redis stream message itself, using
OpenTelemetry's standard `TextMapPropagator` API. The result is a **single,
continuous trace tree** visible in Jaeger:

```
Trace 1 (spans from both scheduler and worker, same trace ID)
└── scheduler.dispatch_job           (SpanKindProducer)
      └── worker.execute_job         (SpanKindConsumer, child of dispatch_job)
            ├── store.MarkJobRunning
            └── executor.Execute
```

---

### Implementation

#### 1. `Enqueue` — inject trace context

`internal/queue/redis/redis_queue.go`, `Enqueue`:

```go
carrier := propagation.MapCarrier{}
otel.GetTextMapPropagator().Inject(ctx, carrier)
for k, v := range carrier {
    values[k] = v   // written as plain Redis stream fields
}
```

`propagation.MapCarrier` is a `map[string]string`. The global W3C propagator
(`TraceContext{} + Baggage{}`) writes at most two fields:

| Redis field     | W3C header      | Example value                                           |
|-----------------|-----------------|--------------------------------------------------------|
| `traceparent`   | `traceparent`   | `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01` |
| `tracestate`    | `tracestate`    | *(empty unless baggage is set)*                         |

These are stored alongside `job_id` and `payload` as ordinary string fields in
the Redis stream entry. They are human-readable in `redis-cli XRANGE` output
and add no serialization overhead beyond a few bytes.

#### 2. `Dequeue` — extract trace context and return enriched context

`internal/queue/redis/redis_queue.go`, `Dequeue`:

```go
carrier := propagation.MapCarrier{}
for k, v := range msg.Values {
    if s, ok := v.(string); ok {
        carrier[k] = s
    }
}
jobCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)
// ...
return jobCtx, &job, ackFn, nil
```

The method signature changed from:

```go
Dequeue(ctx, queueName, timeout) (*domain.Job, AckFunc, error)
```

to:

```go
Dequeue(ctx, queueName, timeout) (context.Context, *domain.Job, AckFunc, error)
```

The returned `context.Context` carries a **remote span context** whose
`TraceID` and `SpanID` match the enqueuing side's active span. It is not a
live span — it is the W3C parent pointer that the worker uses to create a
child span.

#### 3. Worker pool — start child span from extracted context

`internal/worker/pool.go`:

`jobTask` stores the extracted context:

```go
type jobTask struct {
    job      *domain.Job
    ackFn    queue.AckFunc
    traceCtx context.Context // W3C trace context from the Redis stream message
}
```

`dequeueLoop` stores the returned context:

```go
traceCtx, job, ackFn, err := p.queue.Dequeue(ctx, qName, p.cfg.VisibilityTimeout)
// ...
p.jobCh <- &jobTask{job: job, ackFn: ackFn, traceCtx: traceCtx}
```

`executeJob` starts the span from `task.traceCtx` instead of the local worker
context, making `worker.execute_job` a child of the enqueuing span:

```go
ctx, span := observability.Tracer("orion.worker").Start(task.traceCtx, "worker.execute_job",
    trace.WithSpanKind(trace.SpanKindConsumer),
    // ...
)
```

---

### What you see in Jaeger

Before this change: two unrelated root spans for the same job.

After this change: one trace tree. Click any `worker.execute_job` span and you
will see its parent `scheduler.dispatch_job` (or the HTTP handler span if the
job was submitted via the API directly).

The `trace_id` logged on every worker log line (`observability.WithTrace`) now
matches the trace ID that started in the scheduler or API, making cross-service
log correlation trivial.

---

### Fallback behaviour

When no active trace context exists on the enqueuing side (e.g. a job
submitted by a script without OTel instrumentation), `Inject` writes nothing
into the carrier. `Extract` on the worker side returns the original `ctx`
unchanged, so `worker.execute_job` starts as a new root span — identical to the
previous behaviour. There is no regression for un-instrumented callers.

---

### Testing

`TestDequeue_PropagatesTraceContext` in
`internal/queue/redis/redis_queue_test.go` verifies the contract end-to-end:

1. Registers an in-memory `sdktrace.TracerProvider` with a `SpanRecorder`.
2. Creates a root span on the enqueuing side and calls `Enqueue`.
3. Calls `Dequeue` and inspects the returned `context.Context`.
4. Asserts that `trace.SpanContextFromContext(gotCtx).TraceID()` equals the
   enqueuing span's `TraceID`, and that the `SpanID` equals the enqueuing
   span's `SpanID` (i.e. the worker's span will be created as a child of that
   span).

The test uses `miniredis` for a real in-process Redis stream and requires no
external infrastructure.

---

### Files changed

| File | Change |
|------|--------|
| `internal/queue/queue.go` | `Dequeue` now returns `(context.Context, *domain.Job, AckFunc, error)` |
| `internal/queue/redis/redis_queue.go` | `Enqueue` injects W3C carrier; `Dequeue` extracts it and returns enriched context |
| `internal/worker/pool.go` | `jobTask` gains `traceCtx`; `executeJob` uses it as parent span |
| `internal/queue/redis/redis_queue_test.go` | All `Dequeue` call sites updated; `TestDequeue_PropagatesTraceContext` added |
| `internal/scheduler/scheduler_test.go` | `fakeQueue.Dequeue` updated to new 4-return signature |
| `internal/api/handler/job_test.go` | `fakeQueue.Dequeue` updated to new 4-return signature |
