# Phase 10 — Observability Hardening

This document records every production-hardening change made in Phase 10 to
Orion's observability stack. Each section is self-contained: the problem it
solves, the implementation, and how to verify it.

---

## Table of Contents

1. [DBOperationDuration metric wiring](#1-dboperationduration-metric-wiring)
2. [Grafana datasource provisioning](#2-grafana-datasource-provisioning)
3. [Prometheus alerting rules](#3-prometheus-alerting-rules)
4. [Trace–log correlation](#4-tracelog-correlation)
5. [gRPC OpenTelemetry server handler](#5-grpc-opentelemetry-server-handler)
6. [Cross-queue trace context propagation — context lifecycle fix](#6-cross-queue-trace-context-propagation--context-lifecycle-fix)
7. [Architectural note: scheduled jobs trace boundary](#7-architectural-note-scheduled-jobs-trace-boundary)
8. [Observability matrix](#8-observability-matrix)
9. [How to verify](#9-how-to-verify)

---

## 1. DBOperationDuration metric wiring

### Problem

Phase 6 declared the `orion_db_operation_duration_seconds` histogram in
`Metrics` and created the `db.observe()` helper, but the store methods never
called it. Every DB span was wired for tracing, but the Prometheus histogram
showed zero samples — the "DB Operation Latency" Grafana panel was always
empty.

### Implementation

`internal/store/postgres/db.go` — `observe` helper and `WithMetrics`:

```go
// WithMetrics attaches a Metrics instance so the DB records DBOperationDuration
// for every store method. Call this after New() in production binaries.
func (db *DB) WithMetrics(m *observability.Metrics) *DB {
    db.metrics = m
    return db
}

// observe records the elapsed time for a DB operation if metrics are configured.
// Usage: defer db.observe("MethodName", time.Now())
func (db *DB) observe(op string, start time.Time) {
    if db.metrics != nil {
        db.metrics.DBOperationDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
    }
}
```

`defer db.observe(...)` is added at the top of every high-frequency store
method:

| Method | Metric label |
|---|---|
| `CreateJob` | `CreateJob` |
| `GetJob` | `GetJob` |
| `ListJobs` | `ListJobs` |
| `MarkJobRunning` | `MarkJobRunning` |
| `MarkJobCompleted` | `MarkJobCompleted` |
| `MarkJobFailed` | `MarkJobFailed` |
| `TransitionJobState` | `TransitionJobState` |
| `GetPipelineJobs` | `GetPipelineJobs` |
| `RecordExecution` | `RecordExecution` |

Wiring in each binary (`cmd/api/main.go`, `cmd/scheduler/main.go`,
`cmd/worker/main.go`):

```go
pgStore := postgres.New(db).WithMetrics(metrics)
```

`WithMetrics` returns `*DB` so the call chains with `New()` without an
intermediate variable.

### What you see in Grafana

The "DB Operation Latency" panel (query:
`histogram_quantile(0.95, rate(orion_db_operation_duration_seconds_bucket[5m])) by (operation)`)
now shows one line per operation. Under sustained load, `MarkJobRunning` and
`ListJobs` are the hottest paths — typically 1–5 ms at p95.

---

## 2. Grafana datasource provisioning

### Problem

The Prometheus and Jaeger datasources existed in the running Grafana instance
but were created manually. Any `docker compose down -v` destroyed them, forcing
manual recreation. The exemplar→trace link and trace→metrics cross-navigation
were not configured.

### Implementation

`deploy/grafana/datasources/datasources.yml` is provisioned automatically via
the Docker Compose volume mount:

```yaml
apiVersion: 1

datasources:
  # ── Prometheus ──────────────────────────────────────────────────────────────
  - name: Prometheus
    type: prometheus
    uid: prometheus
    url: http://prometheus:9090
    isDefault: true
    access: proxy
    jsonData:
      timeInterval: "15s"
      httpMethod: POST
      exemplarTraceIdDestinations:
        - name: trace_id
          datasourceUid: jaeger    # exemplar click → Jaeger trace

  # ── Jaeger ───────────────────────────────────────────────────────────────────
  - name: Jaeger
    type: jaeger
    uid: jaeger
    url: http://jaeger:16686
    access: proxy
    jsonData:
      tracesToLogsV2:
        datasourceUid: prometheus
        spanStartTimeShift: "-1m"
        spanEndTimeShift: "1m"
        filterByTraceID: false
        filterBySpanID: false
        customQuery: false
      tracesToMetrics:
        datasourceUid: prometheus
        spanStartTimeShift: "-1m"
        spanEndTimeShift: "1m"
        queries:
          - name: "Job Duration"
            query: "histogram_quantile(0.95, rate(orion_job_duration_seconds_bucket[5m]))"
          - name: "DB Latency p95"
            query: "histogram_quantile(0.95, rate(orion_db_operation_duration_seconds_bucket[5m]))"
      nodeGraph:
        enabled: true
```

Two navigation paths this enables:

- **Exemplar → trace**: click any data point with an exemplar icon on a
  Prometheus histogram panel → jumps to the matching Jaeger trace.
- **Trace span → metrics**: in Jaeger, the "Metrics" tab on a span shows the
  correlated PromQL panels defined above.

### How to reload without restart

```bash
curl -u admin:admin -X POST \
  http://localhost:3000/api/admin/provisioning/datasources/reload
```

---

## 3. Prometheus alerting rules

### Problem

No alerts existed. A queue could grow unboundedly or workers could go idle with
pending jobs and no one would be paged.

### Implementation

`deploy/prometheus/rules.yml` — 14 rules across 6 groups. Prometheus loads this
via the `rule_files: [rules.yml]` directive in `prometheus.yml`.

#### Group 1 — `orion.jobs`

| Alert | Condition | Severity |
|---|---|---|
| `HighDeadJobRate` | `rate(orion_jobs_dead_total[5m]) > 0.1` for 5 min | warning |
| `JobFailureRateSpike` | failure rate > 50% of completions for 2 min | warning |
| `NoJobCompletions` | zero completions for 10 min AND queue depth > 0 | critical |

#### Group 2 — `orion.queues`

| Alert | Condition | Severity |
|---|---|---|
| `HighPriorityQueueDepthHigh` | `orion:queue:high` depth > 1000 for 10 min | warning |
| `QueueDepthGrowing` | `deriv(depth[10m]) > 10` AND depth > 500 for 10 min | warning |
| `QueueRateLimiterThrottling` | rate-limited count > 5/s for 5 min | info |

#### Group 3 — `orion.workers`

| Alert | Condition | Severity |
|---|---|---|
| `AllWorkerSlotsBusy` | `active_jobs >= concurrency_limit` for 5 min | warning |
| `WorkerIdleWithPendingJobs` | zero active jobs AND queue depth > 10 for 5 min | critical |

#### Group 4 — `orion.scheduler`

| Alert | Condition | Severity |
|---|---|---|
| `SchedulerCycleLatencyHigh` | p95 cycle duration > 1 s for 5 min | warning |
| `PipelineAdvancerLatencyHigh` | p95 advancer duration > 2 s for 5 min | warning |

#### Group 5 — `orion.database`

| Alert | Condition | Severity |
|---|---|---|
| `DBLatencyCritical` | p95 overall DB latency > 500 ms for 5 min | critical |
| `DBOperationLatencyHigh` | p95 per-operation latency > 250 ms for 5 min | warning |

#### Group 6 — `orion.api` / `orion.pipelines`

| Alert | Condition | Severity |
|---|---|---|
| `PipelineFailureRateHigh` | pipeline failure rate > 0.05/s for 10 min | warning |
| `APIHighErrorRate` | HTTP 5xx > 5% of requests for 5 min | warning |
| `APILatencyHigh` | p95 HTTP latency > 1 s for 5 min | warning |

### Verify alerts load

```bash
# Prometheus UI → Status → Rules
open http://localhost:9090/rules

# Or query via API
curl -s localhost:9090/api/v1/rules | jq '.data.groups | length'
# Expected: 6
```

---

## 4. Trace–log correlation

### Problem

Worker log lines had `job_id` but no `trace_id`. Finding the Jaeger trace for a
specific log error required manually searching Jaeger by `job.id` attribute.
Going the other direction — from a slow Jaeger span to its log lines — was
impossible without timestamp-range grep.

### Implementation

`observability.WithTrace` in `internal/observability/observability.go`:

```go
// WithTrace returns a logger enriched with trace_id and span_id extracted from
// the active OTel span in ctx. If no valid span is present the original logger
// is returned unchanged.
func WithTrace(ctx context.Context, logger *slog.Logger) *slog.Logger {
    sc := trace.SpanFromContext(ctx).SpanContext()
    if !sc.IsValid() {
        return logger
    }
    return logger.With(
        "trace_id", sc.TraceID().String(),
        "span_id",  sc.SpanID().String(),
    )
}
```

Called in `executeJob` immediately after the span is started:

```go
ctx, span := observability.Tracer("orion.worker").Start(parentCtx, "worker.execute_job", ...)
defer span.End()

logger = observability.WithTrace(ctx, logger)
// all subsequent logger calls in this job carry trace_id and span_id
```

And in the gRPC server for every RPC:

```go
logger := observability.WithTrace(ctx, s.logger)
```

### What you see in logs

```json
{
  "time": "2026-11-02T21:46:27Z",
  "level": "INFO",
  "msg": "job completed successfully",
  "service": "orion-worker",
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id":  "00f067aa0ba902b7",
  "elapsed_ms": 312
}
```

Copy `trace_id` and paste it into Jaeger search — it opens the exact execution
trace. Copy the Jaeger trace ID and grep logs with `jq 'select(.trace_id == "...")'`.

---

## 5. gRPC OpenTelemetry server handler

### Problem

HTTP requests produced root-level OTel spans (via `TracingMiddleware`), but
gRPC calls — `SubmitJob`, `GetJob`, `WatchJob` — produced no spans. gRPC-
submitted jobs were invisible in Jaeger, and the `grpc.SubmitJob` → DB → Redis
chain had no trace tree.

### Implementation

`go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc`
provides a `StatsHandler` that instruments the gRPC server automatically. It:

- Creates a server-side span for every incoming RPC.
- Extracts the W3C `traceparent` header from gRPC metadata if the caller is
  instrumented.
- Records the RPC method, status code, and peer address as span attributes.

Wired in `cmd/api/main.go`:

```go
import "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"

grpcSrv := grpc.NewServer(
    grpc.StatsHandler(otelgrpc.NewServerHandler()),
)
```

The gRPC service methods also create child spans manually for DB calls:

```go
// internal/api/grpc/server.go
func (s *Server) SubmitJob(ctx context.Context, req *orionv1.SubmitJobRequest) (*orionv1.Job, error) {
    ctx, span := observability.Tracer("orion.grpc").Start(ctx, "grpc.SubmitJob")
    defer span.End()
    // ...
}
```

### What you see in Jaeger

A `grpcurl` call to `SubmitJob` now appears as:

```
orion-api: grpc.SubmitJob          (server interceptor span)
  └── orion-api: grpc.SubmitJob    (manual span in handler)
        └── orion-api: db.CreateJob
```

`WatchJob` streams remain open; the server interceptor span stays open until
the stream closes, giving accurate duration for long-lived watch connections.

### Module dependency

```go
// go.mod
require (
    go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.49.0
)
```

---

## 6. Cross-queue trace context propagation — context lifecycle fix

### Problem

Orion's distributed tracing connected HTTP/gRPC to the scheduler, but **broke
at the Redis Streams queue boundary**. Every job enqueued by the scheduler or
API started a new root trace on the worker side, producing two disconnected
traces in Jaeger:

```
Trace 1 (Scheduler/API)
└── scheduler.dispatch_job
      └── (enqueues job to Redis — context lost)

Trace 2 (Worker — orphaned root)
└── worker.execute_job
      └── ...
```

There were also two lifecycle-coupling bugs in the initial implementation:

**Bug A — Graceful drain fails on worker shutdown**

`runWorker` handles `<-ctx.Done()` and drains in-flight jobs by calling
`p.executeJob(context.Background(), task, logger)`. But `executeJob` started
its span from `task.traceCtx`, which was derived from `Dequeue`'s `ctx`:

```go
// Dequeue (before fix)
jobCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)
```

When the worker shuts down, `ctx` is cancelled. `jobCtx` inherits the
cancellation. `executeJob` calls `store.MarkJobRunning(ctx, ...)` using the
span's context, which is already cancelled — every drain-path store call
returns `context.Canceled` immediately.

**Bug B — Dequeue timeout leaks into job execution**

A caller passing `context.WithTimeout(ctx, 30*time.Second)` to `Dequeue`
would have that 30 s deadline inherited by `jobCtx`. Long-running jobs would
be prematurely cancelled.

**Bug C — Nil panic on traceCtx**

`executeJob` called `observability.Tracer(...).Start(task.traceCtx, ...)`.
If `task.traceCtx` is nil (mocks, unit tests without OTel setup), this panics.

---

### Solution

The fix has two parts: one in `Dequeue`, one in `executeJob`.

#### Part 1 — Extract onto `context.Background()` in Dequeue

`internal/queue/redis/redis_queue.go`, `Dequeue`:

```go
// Extract into context.Background() — NOT ctx — so the returned jobCtx is
// decoupled from Dequeue's own cancellation / timeout lifecycle.
carrier := propagation.MapCarrier{}
for k, v := range msg.Values {
    if s, ok := v.(string); ok {
        carrier[k] = s
    }
}
jobCtx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
```

`jobCtx` now carries only the remote span context (the W3C `traceparent`
propagated from the enqueuing side). It has no cancellation signal and no
deadline — it cannot be cancelled by the dequeue loop's parent context.

#### Part 2 — Nil-guard and `ContextWithRemoteSpanContext` in executeJob

`internal/worker/pool.go`, `executeJob`:

```go
// Graft the remote span context (from the Redis message) onto the live
// execution ctx using trace.ContextWithRemoteSpanContext. This ensures:
//   1. worker.execute_job is a child of the enqueuing side's span in Jaeger.
//   2. The live ctx (with its deadline/cancellation) is used for all DB calls.
//   3. If task.traceCtx is nil (mocks/tests), we fall back to plain ctx safely.
parentCtx := ctx
if task.traceCtx != nil {
    if sc := trace.SpanContextFromContext(task.traceCtx); sc.IsValid() {
        parentCtx = trace.ContextWithRemoteSpanContext(ctx, sc)
    }
}
ctx, span := observability.Tracer("orion.worker").Start(parentCtx, "worker.execute_job",
    trace.WithSpanKind(trace.SpanKindConsumer),
    trace.WithAttributes(
        attribute.String("job.id", job.ID.String()),
        attribute.String("job.name", job.Name),
        attribute.String("job.type", string(job.Type)),
        attribute.String("job.queue", job.QueueName),
        attribute.Int("job.attempt", job.Attempt),
        semconv.MessagingSystemKey.String("redis"),
    ),
)
defer span.End()
```

`trace.ContextWithRemoteSpanContext` sets the remote span context as the
parent pointer inside `ctx` without replacing `ctx` itself. The OTel SDK uses
it when `Start()` is called — `worker.execute_job` becomes a child of the
enqueuing span in Jaeger. But all subsequent DB and executor calls use the
returned `ctx` (which has the correct cancellation, deadline, and baggage from
the caller).

---

### Enqueue side (unchanged, shown for reference)

`internal/queue/redis/redis_queue.go`, `Enqueue`:

```go
carrier := propagation.MapCarrier{}
otel.GetTextMapPropagator().Inject(ctx, carrier)
for k, v := range carrier {
    values[k] = v   // written as plain Redis stream fields
}
```

The W3C propagator writes at most two fields:

| Redis field   | W3C header    | Example value                                              |
|---------------|---------------|-----------------------------------------------------------|
| `traceparent` | `traceparent` | `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01` |
| `tracestate`  | `tracestate`  | *(empty unless baggage is set)*                           |

---

### What you see in Jaeger

Before this change: two unrelated root spans for the same job.

After this change: one trace tree. Click any `worker.execute_job` span and you
will see its parent `scheduler.dispatch_job` (or the HTTP handler span if the
job was submitted via the API).

```
Trace (spans from scheduler and worker, same trace ID)
└── scheduler.dispatch_job          (SpanKindProducer)
      └── worker.execute_job        (SpanKindConsumer, child of dispatch_job)
            ├── store.MarkJobRunning
            └── executor.Execute
```

---

### Fallback behaviour

When no active trace context exists on the enqueuing side (e.g. a script
without OTel instrumentation), `Inject` writes nothing. `Extract` on the
worker side finds an empty carrier. `trace.SpanContextFromContext(task.traceCtx).IsValid()`
returns false, so `parentCtx` remains plain `ctx` and `worker.execute_job`
starts as a new root span — identical to previous behaviour. No regression for
uninstrumented callers.

---

### Testing

`TestDequeue_PropagatesTraceContext` in
`internal/queue/redis/redis_queue_test.go` verifies the contract end-to-end:

1. Registers an in-memory `sdktrace.TracerProvider` with a `SpanRecorder`.
2. Creates a root span on the enqueuing side and calls `Enqueue`.
3. Calls `Dequeue` and inspects the returned `context.Context`.
4. Asserts that `trace.SpanContextFromContext(gotCtx).TraceID()` equals the
   enqueuing span's `TraceID`, and that `SpanID` equals the enqueuing span's
   `SpanID` (the worker span will be created as a child of that span).

The test uses `miniredis` for a real in-process Redis stream.

---

### Files changed

| File | Change |
|------|--------|
| `internal/queue/queue.go` | `Dequeue` now returns `(context.Context, *domain.Job, AckFunc, error)` |
| `internal/queue/redis/redis_queue.go` | `Enqueue` injects W3C carrier; `Dequeue` extracts onto `context.Background()` |
| `internal/worker/pool.go` | `jobTask` gains `traceCtx`; `executeJob` uses nil-guard + `ContextWithRemoteSpanContext` |
| `internal/queue/redis/redis_queue_test.go` | All `Dequeue` call sites updated; `TestDequeue_PropagatesTraceContext` added |
| `internal/scheduler/scheduler_test.go` | `fakeQueue.Dequeue` updated to new 4-return signature |
| `internal/api/handler/job_test.go` | `fakeQueue.Dequeue` updated to new 4-return signature |

---

## 7. Architectural note: scheduled jobs trace boundary

In `redis_queue.go`, future-scheduled jobs (`job.ScheduledAt.After(time.Now())`)
are stored in the sorted set `orion:queue:scheduled` as raw JSON:

```go
r.client.ZAdd(ctx, queue.QueueScheduled, redis.Z{
    Score:  float64(job.ScheduledAt.Unix()),
    Member: string(body),
})
```

When `sweepScheduled` promotes them into the stream, it uses `msg.Values` from
the raw JSON, which does **not** carry `traceparent`. The Lua `zpopByScore`
script pops members atomically; there is no carrier injection step on promotion.

This means scheduled jobs that run hours or days in the future start a fresh
trace on the worker side. This is intentional:

- Old trace IDs expire in Jaeger (default retention: 7 days for the in-process
  store). Linking a 3-day-old scheduling span to a worker span that runs today
  would produce a broken trace reference.
- The submission-time trace context is preserved in the `payload` JSONB if
  needed for audit (the full job is stored there).

If end-to-end trace continuity for scheduled jobs is required in the future,
`sweepScheduled` could re-inject the `traceparent` from the job's stored
payload when doing `XAdd`.

---

## 8. Observability matrix

The complete current state of every signal Orion emits.

### Metrics (Prometheus)

| Metric | Type | Labels | Emitted by |
|---|---|---|---|
| `orion_jobs_submitted_total` | Counter | `queue`, `type` | Scheduler |
| `orion_jobs_completed_total` | Counter | `queue`, `type` | Worker |
| `orion_jobs_failed_total` | Counter | `queue`, `type`, `reason` | Worker |
| `orion_jobs_retried_total` | Counter | `queue` | Worker |
| `orion_jobs_dead_total` | Counter | `queue` | Worker |
| `orion_job_duration_seconds` | Histogram | `queue`, `type`, `status` | Worker |
| `orion_queue_depth` | Gauge | `queue` | Worker + Scheduler (poller) |
| `orion_worker_active_jobs` | Gauge | `worker_id` | Worker |
| `orion_scheduler_cycle_duration_seconds` | Histogram | — | Scheduler |
| `orion_pipelines_started_total` | Counter | `pipeline_name` | Scheduler |
| `orion_pipelines_completed_total` | Counter | `pipeline_name` | Scheduler |
| `orion_pipelines_failed_total` | Counter | `pipeline_name` | Scheduler |
| `orion_pipeline_duration_seconds` | Histogram | `status` | Scheduler |
| `orion_pipeline_nodes_total` | Counter | `pipeline_name`, `node_status` | Scheduler |
| `orion_advancer_cycle_duration_seconds` | Histogram | — | Scheduler |
| `orion_http_requests_total` | Counter | `method`, `path`, `status_code` | API |
| `orion_http_request_duration_seconds` | Histogram | `method`, `path` | API |
| `orion_db_operation_duration_seconds` | Histogram | `operation` | All three ✅ |
| `orion_queue_rate_limited_total` | Counter | `queue` | Scheduler |
| `orion_queue_concurrent_jobs` | Gauge | `queue` | Scheduler |
| `orion_queue_concurrency_limit` | Gauge | `queue` | Scheduler |
| `orion_queue_dispatch_weight` | Gauge | `queue` | Scheduler |

### Traces (OpenTelemetry → Jaeger)

| Span | Service | Kind | Parent |
|---|---|---|---|
| `http.{METHOD} {route}` | `orion-api` | Server | gRPC/HTTP caller |
| `grpc.SubmitJob` | `orion-api` | Server | gRPC client |
| `grpc.GetJob` | `orion-api` | Server | gRPC client |
| `db.CreateJob` | `orion-api` | Internal | HTTP/gRPC handler span |
| `scheduler.dispatch_cycle` | `orion-scheduler` | Internal | root |
| `scheduler.dispatch_job` | `orion-scheduler` | Producer | `dispatch_cycle` |
| `advancer.advance_all` | `orion-scheduler` | Internal | root |
| `worker.execute_job` | `orion-worker` | Consumer ✅ | `scheduler.dispatch_job` |
| `db.MarkJobRunning` | `orion-worker` | Internal | `execute_job` |
| `db.MarkJobCompleted` | `orion-worker` | Internal | `execute_job` |
| `db.MarkJobFailed` | `orion-worker` | Internal | `execute_job` |

### Logs (slog JSON)

Every log line from Phase 6 onward carries:

| Field | Source |
|---|---|
| `trace_id` | `observability.WithTrace(ctx, logger)` ✅ |
| `span_id` | `observability.WithTrace(ctx, logger)` ✅ |
| `job_id` | `logger.With("job_id", job.ID)` in `executeJob` |
| `worker_id` | `logger.With("worker_id", cfg.WorkerID)` in pool startup |
| `service` | `observability.NewLogger("orion-worker", ...)` |
| `env` | `observability.NewLogger(..., cfg.Environment)` |

### Alerting

| Group | Rules | Severities |
|---|---|---|
| `orion.jobs` | 3 | critical, warning |
| `orion.queues` | 3 | warning, info |
| `orion.workers` | 2 | critical, warning |
| `orion.scheduler` | 2 | warning |
| `orion.database` | 2 | critical, warning |
| `orion.pipelines` + `orion.api` | 3 | warning |

---

## 9. How to verify

```bash
# Start infra and all three services
make infra-up && make migrate-up
make run-api        # :8080 HTTP  :9090 gRPC  :9091 metrics
make run-scheduler  #                          :9092 metrics
make run-worker     #                          :9093 metrics

# ── Section 1: DBOperationDuration ──────────────────────────────────────────
curl -sX POST http://localhost:8080/jobs \
  -H 'Content-Type: application/json' \
  -d '{"name":"obs-test","type":"inline","payload":{"handler_name":"noop"}}'
sleep 3
curl -s 'localhost:9090/api/v1/query?query=orion_db_operation_duration_seconds_count' \
  | jq '.data.result[] | {op: .metric.operation, count: .value[1]}'
# Expected: CreateJob, MarkJobRunning, MarkJobCompleted, RecordExecution all > 0

# ── Section 2: Grafana datasources ──────────────────────────────────────────
curl -s -u admin:admin http://localhost:3000/api/datasources \
  | jq '.[].name'
# Expected: "Prometheus", "Jaeger"

# ── Section 3: Prometheus alerting rules ────────────────────────────────────
curl -s http://localhost:9090/api/v1/rules | jq '.data.groups | length'
# Expected: 6

curl -s http://localhost:9090/api/v1/rules \
  | jq '[.data.groups[].rules[].name]'
# Expected: 14 alert names

# ── Section 4: Trace-log correlation ────────────────────────────────────────
make run-worker 2>&1 | grep '"trace_id"' | head -3
# Expected: log lines with trace_id and span_id fields

# ── Section 5: gRPC OTel handler ────────────────────────────────────────────
grpcurl -plaintext -d '{"name":"grpc-test","type":"inline","payload":{"handler_name":"noop"}}' \
  localhost:9090 orion.v1.JobService/SubmitJob
sleep 3
# Open http://localhost:16686 → service: orion-api → find "grpc.SubmitJob"

# ── Section 6: Cross-queue trace propagation ────────────────────────────────
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H 'Content-Type: application/json' \
  -d '{"name":"trace-test","type":"inline","payload":{"handler_name":"noop"}}' \
  | jq -r .id)
sleep 5
# Open Jaeger: http://localhost:16686
# Search service=orion-worker, tag job.id=<JOB_ID>
# Verify: worker.execute_job has scheduler.dispatch_job as parent
echo "Verify trace at http://localhost:16686 for job $JOB_ID"

# ── Full test suite ─────────────────────────────────────────────────────────
go test -race ./... -count=1
# All tests pass (nil-guarded metrics, no panics on nil traceCtx)
```
