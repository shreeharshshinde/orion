# Phase 10 — Observability Hardening

**Status:** Planned  
**Prerequisite:** Phase 9 (Helm chart, Docker multi-stage builds)

---

## Current State Assessment

### What is instrumented ✅

| Area | Status | Detail |
|---|---|---|
| Prometheus metrics registration | ✅ | 20+ metrics across jobs, queues, scheduler, worker, HTTP, DB, pipelines, Phase 8 queue limits |
| OTel tracing setup | ✅ | `SetupTracing` with OTLP/gRPC exporter, `TraceIDRatioBased` sampler wired in all 3 binaries |
| Tracing spans | ✅ | Spans on scheduler dispatch cycle, per-job dispatch, worker `executeJob`, pipeline `AdvanceAll`, HTTP middleware |
| `slog` structured logging | ✅ | JSON in prod/staging, text in dev; every log line gets `service` and `env` |
| Prometheus scrape config | ✅ | `prometheus.yml` scrapes api (:9091), scheduler (:9092), worker (:9093) |
| Grafana dashboard | ✅ | 9 panels: job throughput, error rate, queue depth, job duration p50/p95/p99, worker utilisation, scheduler/advancer latency, pipeline throughput, dead jobs/hour, DB latency p95 |
| HTTP metrics middleware | ✅ | `MetricsMiddleware` wraps entire mux; uses `r.Pattern` for low-cardinality route labels |
| Queue depth poller | ✅ | `StartQueueDepthPoller` ticks every 5s via Redis `XLEN` |
| Pipeline metrics | ✅ | `PipelineStarted`, `PipelineCompleted`, `PipelineFailed`, `AdvancerCycleDuration` all called in `advancement.go` |
| Phase 8 queue metrics | ✅ | `QueueRateLimited`, `QueueConcurrentJobs`, `QueueConcurrencyLimit`, `QueueDispatchWeight` |

---

### Gaps Found (What Is Missing or Broken) ❌

#### Gap 1 — `DBOperationDuration` metric is never called (HIGH)

`Metrics.DBOperationDuration` is defined and registered, and the Grafana "DB Operation Latency p95" panel queries it — but **no code in `internal/store/postgres/` ever calls it**. The panel shows no data. The postgres store has no reference to `*observability.Metrics` at all.

##### ✅ Solution

Added `metrics *observability.Metrics` field to `DB`, a nil-safe `observe(op, start)` helper, and a `WithMetrics(m) *DB` chainable option so existing test callsites don't change.

```go
// internal/store/postgres/db.go

func (db *DB) WithMetrics(m *observability.Metrics) *DB {
    db.metrics = m
    return db
}

func (db *DB) observe(op string, start time.Time) {
    if db.metrics != nil {
        db.metrics.DBOperationDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
    }
}
```

Every public method in `db.go` (21 methods) and `pipeline.go` (7 methods) now starts with:

```go
defer db.observe("MethodName", time.Now())
```

All three binaries wire it at construction:

```go
// cmd/{api,scheduler,worker}/main.go
pgStore := postgres.New(db).WithMetrics(metrics)
```

**Files changed:** `internal/store/postgres/db.go`, `internal/store/postgres/pipeline.go`, `cmd/api/main.go`, `cmd/scheduler/main.go`, `cmd/worker/main.go`

#### Gap 2 — Grafana has no Jaeger/Tempo datasource configured (HIGH)

`deploy/grafana/datasources/` is **empty**. The Prometheus datasource is also missing (relying on Grafana default). There is no Jaeger datasource configured, so the Grafana "Explore" tab cannot query traces. The Helm chart references `jaeger-collector:4317` as the OTLP endpoint but provides no Jaeger UI datasource for Grafana to link traces to spans.

##### ✅ Solution

Created `deploy/grafana/datasources/datasources.yml` — Grafana provisions this at startup from the volume mount already defined in `docker-compose.yml`. A fresh `make infra-up` now produces a fully working Grafana with no manual setup.

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    uid: prometheus
    url: http://prometheus:9090
    isDefault: true
    access: proxy

  - name: Jaeger
    type: jaeger
    uid: jaeger
    url: http://jaeger:16686
    access: proxy
    jsonData:
      tracesToLogsV2:
        datasourceUid: prometheus
```

**Files changed:** `deploy/grafana/datasources/datasources.yml` (created)

#### Gap 3 — gRPC server has no OTel interceptors (MEDIUM)

`cmd/api/main.go` calls `grpc.NewServer()` with no options. There are no `otelgrpc.UnaryServerInterceptor()` or `otelgrpc.StreamServerInterceptor()` interceptors. gRPC calls (`SubmitJob`, `WatchJob`, etc.) produce no spans — they are invisible in Jaeger. The HTTP side is fully traced; the gRPC side is dark.

##### ✅ Solution

Added `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` and wired `otelgrpc.NewServerHandler()` (the modern stats-handler API, supersedes the deprecated per-interceptor approach):

```go
// cmd/api/main.go
grpcSrv := grpc.NewServer(
    grpc.StatsHandler(otelgrpc.NewServerHandler()),
)
```

`SubmitJob`, `GetJob`, `WatchJob`, and `WatchPipeline` now produce spans in Jaeger automatically.

**Files changed:** `cmd/api/main.go`, `go.mod`, `go.sum`

#### Gap 4 — `SetupTracing` does not handle empty `otlpEndpoint` (MEDIUM)

If `ORION_OTLP_ENDPOINT` is unset (empty string), `otlptracegrpc.New` dials an empty address and returns an error. The three `cmd/` binaries log a warning and continue (`logger.Warn("tracing setup failed, continuing without traces")`). This is correct behaviour, but the `SetupTracing` function should explicitly return a no-op provider when the endpoint is empty rather than attempting a dial, to avoid a 5s connection timeout at startup on every deploy that doesn't use tracing.

##### ✅ Solution

Added an early return at the top of `SetupTracing` that installs a no-op provider and returns immediately — no dial, no timeout, no warning log:

```go
// internal/observability/observability.go
func SetupTracing(...) (func(context.Context) error, error) {
    if otlpEndpoint == "" {
        otel.SetTracerProvider(trace.NewNoopTracerProvider())
        return func(context.Context) error { return nil }, nil
    }
    // ... normal OTLP setup
}
```

Also updated `TestSetupTracing_EmptyEndpoint` which previously asserted the old (error) behaviour.

**Files changed:** `internal/observability/observability.go`, `internal/observability/observability_test.go`

#### Gap 5 — No Prometheus alerting rules (MEDIUM)

Metrics exist but there are no Prometheus alerting rules. There is no `rules.yml` file and `prometheus.yml` has no `rule_files:` section. Operators have no automated notification for:
- Dead job rate spike (`orion_jobs_dead_total`)
- Queue depth saturation (`orion_queue_depth`)
- Scheduler cycle latency regression (`orion_scheduler_cycle_duration_seconds`)
- Worker utilisation ceiling (all workers busy → backpressure active)
- DB operation latency spike

#### Gap 6 — Grafana dashboard missing Phase 8 panels (LOW)

The dashboard was written in Phase 6. It has no panels for the Phase 8 metrics: `QueueRateLimited`, `QueueConcurrentJobs` vs `QueueConcurrencyLimit` (utilisation ratio), and `QueueDispatchWeight`. Queue saturation is invisible on the current dashboard.

#### Gap 7 — Grafana datasource provisioning file missing (HIGH)

`deploy/grafana/datasources/` is empty. Grafana provisions datasources at startup from YAML files in that directory. Without a `datasources.yml`, the Prometheus datasource only works if Grafana is configured with its default or the user manually adds it via UI. This means a fresh `make infra-up` has a non-functional dashboard until the datasource is manually wired.

#### Gap 8 — No trace-to-log correlation (LOW)

`slog` logs do not include `trace_id` or `span_id`. The INSPECTION.md claims "every log line carries `trace_id`, `span_id`, `job_id`, and `worker_id`" — but the `NewLogger` implementation adds only `service` and `env`. There is no OTel span context extraction injected into log calls.

#### Gap 9 — OTLP exporter uses `WithInsecure()` in all environments (LOW)

`observability.go` uses `otlptracegrpc.WithInsecure()` unconditionally. For production, traces containing ML job payloads are sent in plaintext. This is flagged in a code comment but not addressed.

---

## Phase 10 Plan

### 10.1 — Fix `DBOperationDuration` instrumentation

Wire `*observability.Metrics` into `postgres.DB` and record timing around every store method. Add a thin `observe` helper to keep call sites clean:

```go
func (db *DB) observe(m *observability.Metrics, op string, start time.Time) {
    if m != nil {
        m.DBOperationDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
    }
}
```

Call it in every method:
```go
func (db *DB) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
    defer db.observe(db.metrics, "CreateJob", time.Now())
    // ...
}
```

`postgres.New` gains an optional `metrics` parameter (or the caller sets it via a `WithMetrics` option after construction to avoid changing all test callsites).

**Scope:** `internal/store/postgres/db.go` — add metrics field + `observe` helper + `defer observe(...)` on all ~15 public methods.

---

### 10.2 — Add Grafana datasource provisioning files

Create `deploy/grafana/datasources/datasources.yml`:

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    uid: prometheus
    url: http://prometheus:9090
    isDefault: true
    access: proxy

  - name: Jaeger
    type: jaeger
    uid: jaeger
    url: http://jaeger:16686
    access: proxy
    jsonData:
      tracesToLogsV2:
        datasourceUid: prometheus
```

This makes `make infra-up` produce a fully working Grafana instance with no manual setup.

---

### 10.3 — Add OTel interceptors to gRPC server

In `cmd/api/main.go`, replace:
```go
grpcSrv := grpc.NewServer()
```
with:
```go
grpcSrv := grpc.NewServer(
    grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
    grpc.StreamInterceptor(otelgrpc.StreamServerInterceptor()),
)
```

Add `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` to `go.mod`. This makes `SubmitJob`, `GetJob`, `WatchJob`, and `WatchPipeline` produce spans in Jaeger.

---

### 10.4 — No-op tracer when OTLP endpoint is empty

In `SetupTracing`, short-circuit before dialling:

```go
if otlpEndpoint == "" {
    otel.SetTracerProvider(trace.NewNoopTracerProvider())
    return func(context.Context) error { return nil }, nil
}
```

Eliminates the 5s timeout at startup and the misleading "tracing setup failed" warning on deploys without Jaeger.

---

### 10.5 — Prometheus alerting rules

Create `deploy/prometheus/rules.yml`:

```yaml
groups:
  - name: orion
    rules:
      - alert: HighDeadJobRate
        expr: rate(orion_jobs_dead_total[5m]) > 0.1
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Dead job rate > 0.1/s for 5 minutes"

      - alert: QueueDepthSaturated
        expr: orion_queue_depth{queue="orion:queue:high"} > 1000
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "High-priority queue depth > 1000 for 10 minutes"

      - alert: SchedulerCycleLatencyHigh
        expr: histogram_quantile(0.95, rate(orion_scheduler_cycle_duration_seconds_bucket[5m])) > 1
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Scheduler p95 cycle latency > 1s"

      - alert: AllWorkersBusy
        expr: sum(orion_worker_active_jobs) >= sum(orion_queue_concurrency_limit)
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "All worker slots occupied — backpressure active"

      - alert: DBLatencyHigh
        expr: histogram_quantile(0.95, rate(orion_db_operation_duration_seconds_bucket[5m])) > 0.5
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "DB p95 latency > 500ms — check PostgreSQL"
```

Add `rule_files: [rules.yml]` to `prometheus.yml`.

---

### 10.6 — Phase 8 Grafana panels

Add three panels to `orion.json` (row 4, y=24):

| Panel | Query |
|---|---|
| Queue Rate-Limited (per min) | `sum by (queue)(rate(orion_queue_rate_limited_total[1m]))*60` |
| Queue Concurrency Utilisation (%) | `100 * sum by(queue)(orion_queue_concurrent_jobs) / clamp_min(sum by(queue)(orion_queue_concurrency_limit),1)` |
| Queue Dispatch Weight | `orion_queue_dispatch_weight` (stat panel showing current config) |

---

### 10.7 — Trace-log correlation via slog middleware

Add `TraceLogger` helper that extracts the current span from context and adds `trace_id`/`span_id` to every log call:

```go
func WithTrace(ctx context.Context, logger *slog.Logger) *slog.Logger {
    span := trace.SpanFromContext(ctx)
    if !span.SpanContext().IsValid() {
        return logger
    }
    sc := span.SpanContext()
    return logger.With(
        "trace_id", sc.TraceID().String(),
        "span_id",  sc.SpanID().String(),
    )
}
```

Usage in `executeJob`, `scheduleQueuedJobs`, HTTP handlers:
```go
log := observability.WithTrace(ctx, p.logger)
log.Info("executing job", "job_id", job.ID)
```

This aligns the code with the INSPECTION.md claim and enables log-to-trace correlation in Grafana.

---

### 10.8 — TLS for OTLP exporter (production only)

Make `SetupTracing` accept a `tlsEnabled bool` flag (driven by `ORION_TRACING_TLS=true`):

```go
opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(otlpEndpoint)}
if !tlsEnabled {
    opts = append(opts, otlptracegrpc.WithInsecure())
}
```

Default `false` for development. Set `true` in Helm `values.yaml` production defaults.

---

## Summary

| Gap | Fix | Priority |
|---|---|---|
| `DBOperationDuration` never called | Wire metrics into `postgres.DB`, add `observe` helper | HIGH |
| Grafana datasource files missing | Add `deploy/grafana/datasources/datasources.yml` | HIGH |
| gRPC server no OTel interceptors | Add `otelgrpc` unary + stream interceptors | MEDIUM |
| `SetupTracing` dials on empty endpoint | Short-circuit to no-op provider | MEDIUM |
| No Prometheus alerting rules | Add `rules.yml` with 5 core alerts | MEDIUM |
| Phase 8 panels missing in Grafana | Add 3 panels to `orion.json` | LOW |
| Logs missing `trace_id`/`span_id` | Add `WithTrace(ctx, logger)` helper | LOW |
| OTLP insecure in all environments | Add `tlsEnabled` flag to `SetupTracing` | LOW |

All 8 fixes are self-contained. They can be implemented and reviewed in any order. No domain model changes, no migration, no breaking API changes.
