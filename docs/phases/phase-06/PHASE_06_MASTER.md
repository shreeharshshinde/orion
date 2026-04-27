# Orion — Phase 6 Master Guide
## Full Observability: Metrics, Tracing, and Dashboards

> **What this document is:** A complete planning guide for Phase 6 — every file to update, every metric to wire, every span to add, exact code for each instrumentation point, the Grafana dashboard layout, and the test sequence that proves it all works. Read this once before writing any code.

---

## Table of Contents

1. [Why Phase 6 Exists — The Gap It Closes](#1-why-phase-6-exists)
2. [What Already Exists](#2-what-already-exists)
3. [The Core Idea: Three Observability Signals](#3-the-core-idea-three-observability-signals)
4. [Complete File Plan](#4-complete-file-plan)
5. [File 1: `internal/observability/observability.go` — New Metrics](#5-file-1-new-metrics)
6. [File 2: `internal/scheduler/scheduler.go` — Scheduler Instrumentation](#6-file-2-scheduler-instrumentation)
7. [File 3: `internal/worker/pool.go` — Worker Instrumentation](#7-file-3-worker-instrumentation)
8. [File 4: `internal/pipeline/advancement.go` — Pipeline Metrics](#8-file-4-pipeline-metrics)
9. [File 5: `internal/api/handler/job.go` — HTTP Instrumentation](#9-file-5-http-instrumentation)
10. [File 6: `internal/queue/redis/redis_queue.go` — Queue Metrics](#10-file-6-queue-metrics)
11. [File 7: `internal/store/postgres/db.go` — DB Span Tracing](#11-file-7-db-span-tracing)
12. [File 8: `deploy/grafana/dashboards/orion.json` — Dashboard](#12-file-8-grafana-dashboard)
13. [File 9: `deploy/prometheus/prometheus.yml` — Scrape Config](#13-file-9-prometheus-scrape-config)
14. [OpenTelemetry Span Hierarchy](#14-opentelemetry-span-hierarchy)
15. [Metric Catalogue — Every Metric Defined](#15-metric-catalogue)
16. [Passing Metrics and Tracers Through the System](#16-passing-metrics-and-tracers-through-the-system)
17. [Step-by-Step Build Order](#17-step-by-step-build-order)
18. [Complete Verification Sequence](#18-complete-verification-sequence)
19. [PromQL Queries — Copy-Paste Reference](#19-promql-queries)
20. [Common Mistakes](#20-common-mistakes)
21. [Phase 7 Preview](#21-phase-7-preview)

---

## 1. Why Phase 6 Exists

After Phase 5, Orion is functionally complete for ML orchestration. But it is **invisible**. You cannot answer these questions without running raw SQL:

- How many jobs are completing per second right now?
- What is the p99 execution time for `train_model` jobs?
- Is queue depth growing (backpressure building) or shrinking?
- How long did the scheduler take to dispatch last tick?
- Which pipeline node is taking the longest?
- How many jobs have died (exhausted all retries) in the last hour?

Phase 6 makes every one of these questions answerable in under 10 seconds via Grafana. It also wires OpenTelemetry tracing so every job's journey — from HTTP submission through scheduler dispatch to worker execution — is visible as a single trace in Jaeger.

---

## 2. What Already Exists

Phase 1 defined the complete observability infrastructure. Phase 6 uses it.

### Already exists — no changes needed

| File | What it provides |
|---|---|
| `internal/observability/observability.go` | `Metrics` struct with all counters/histograms, `SetupTracing`, `Tracer()`, `NewLogger`, `MetricsServer` |
| `docker-compose.yml` | Prometheus (`:9090`), Jaeger (`:16686`), Grafana (`:3000`) all running |
| `go.mod` | `prometheus/client_golang v1.19.0`, `otel v1.24.0`, `otlptracegrpc v1.24.0` |

### What the Metrics struct already declares

```go
type Metrics struct {
    JobsSubmitted         *prometheus.CounterVec   // labels: queue, type
    JobsCompleted         *prometheus.CounterVec   // labels: queue, type
    JobsFailed            *prometheus.CounterVec   // labels: queue, type, reason
    JobsRetried           *prometheus.CounterVec   // labels: queue
    JobsDead              *prometheus.CounterVec   // labels: queue
    JobDuration           *prometheus.HistogramVec // labels: queue, type, status
    QueueDepth            *prometheus.GaugeVec     // labels: queue
    WorkerActiveJobs      *prometheus.GaugeVec     // labels: worker_id
    SchedulerCycleLatency prometheus.Histogram
}
```

### What Phase 6 adds to the Metrics struct

```go
// Pipeline metrics (new in Phase 6)
PipelineStarted    *prometheus.CounterVec   // labels: name
PipelineCompleted  *prometheus.CounterVec   // labels: name
PipelineFailed     *prometheus.CounterVec   // labels: name
PipelineDuration   *prometheus.HistogramVec // labels: status
PipelineNodesTotal *prometheus.CounterVec   // labels: pipeline_name, status
AdvancerCycleDur   prometheus.Histogram     // advancement tick duration

// HTTP metrics (new in Phase 6)
HTTPRequestsTotal   *prometheus.CounterVec   // labels: method, path, status_code
HTTPRequestDuration *prometheus.HistogramVec // labels: method, path

// DB operation metrics (new in Phase 6)
DBOperationDuration *prometheus.HistogramVec // labels: operation
```

### Does not exist yet — Phase 6 builds

- Every `.Inc()` and `.Observe()` call in the operational code
- Tracing spans at every critical operation
- Grafana dashboard JSON
- Updated Prometheus scrape config for all 3 services

---

## 3. The Core Idea: Three Observability Signals

### Signal 1 — Metrics (Prometheus → Grafana)

Metrics answer **"what is happening at scale right now?"**

Every counter increment and histogram observation is cheap (nanoseconds). They aggregate automatically. PromQL lets you compute rates, percentiles, and ratios across the whole fleet.

```
orion_jobs_completed_total{queue="default",type="inline"} 1247
orion_job_duration_seconds_bucket{le="10"} 890
orion_queue_depth{queue="high"} 3
```

### Signal 2 — Tracing (OpenTelemetry → Jaeger)

Traces answer **"why did this specific job take 47 seconds?"**

A trace is a tree of spans, each with a start time, end time, and attributes. One trace covers the entire life of one job from HTTP POST to worker completion.

```
trace: submit-and-execute-job (total: 47.3s)
  ├── handler.SubmitJob (2ms)
  │     └── db.CreateJob (1.8ms)
  ├── scheduler.dispatchJob (0.5ms)
  │     └── redis.Enqueue (0.3ms)
  └── worker.executeJob (47.2s)
        ├── db.MarkJobRunning (1ms)
        ├── executor.Execute (47.1s)
        └── db.MarkJobCompleted (1ms)
```

### Signal 3 — Structured Logs (slog → stdout → log aggregator)

Logs answer **"what exactly happened and in what order?"**

Phase 1 already set up structured logging. Phase 6 enriches log entries with trace IDs so you can jump from a Jaeger trace to the matching log lines:

```json
{"time":"2024-01-15T10:00:05Z","level":"INFO","msg":"job completed",
 "service":"orion-worker","job_id":"abc123","trace_id":"f4d8e92a...","elapsed_ms":47200}
```

---

## 4. Complete File Plan

Phase 6 touches 9 locations. All are updates to existing files — no new packages needed.

```
internal/observability/observability.go    ← UPDATED: 3 new metric groups
internal/scheduler/scheduler.go            ← UPDATED: spans + cycle latency
internal/worker/pool.go                    ← UPDATED: job duration + active count
internal/pipeline/advancement.go           ← UPDATED: pipeline metrics
internal/api/handler/job.go                ← UPDATED: HTTP metrics + spans
internal/queue/redis/redis_queue.go        ← UPDATED: queue depth gauge
internal/store/postgres/db.go              ← UPDATED: DB operation spans

deploy/grafana/dashboards/orion.json       ← NEW: pre-built Grafana dashboard
deploy/prometheus/prometheus.yml           ← UPDATED: scrape all 3 services
```

### What is NOT changing

| File | Why unchanged |
|---|---|
| `domain/` | Pure data types — no instrumentation |
| `internal/pipeline/advancement_test.go` | Tests don't need real metrics |
| `internal/store/store.go` | Interface only — no implementation |
| `cmd/*/main.go` | Metrics construction and injection happens here, no logic changes |
| `docker-compose.yml` | All infra already running |

---

## 5. File 1: Updated `internal/observability/observability.go`

Add three new metric groups to `NewMetrics`. Keep all existing metrics unchanged.

### New pipeline metrics

```go
PipelineStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: "orion",
    Name:      "pipelines_started_total",
    Help:      "Total number of pipelines transitioned to running status.",
}, []string{"pipeline_name"}),

PipelineCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: "orion",
    Name:      "pipelines_completed_total",
    Help:      "Total number of pipelines that completed successfully.",
}, []string{"pipeline_name"}),

PipelineFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: "orion",
    Name:      "pipelines_failed_total",
    Help:      "Total number of pipelines that failed (dead node detected).",
}, []string{"pipeline_name"}),

PipelineDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
    Namespace: "orion",
    Name:      "pipeline_duration_seconds",
    Help:      "Histogram of pipeline total execution durations.",
    // Pipelines can take minutes to hours
    Buckets: []float64{10, 30, 60, 300, 600, 1800, 3600, 7200, 14400},
}, []string{"status"}),

PipelineNodesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: "orion",
    Name:      "pipeline_nodes_total",
    Help:      "Total number of pipeline nodes that reached a terminal status.",
}, []string{"pipeline_name", "node_status"}),

AdvancerCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
    Namespace: "orion",
    Name:      "advancer_cycle_duration_seconds",
    Help:      "Time taken by one AdvanceAll() call (scheduler tick).",
    Buckets:   prometheus.DefBuckets,
}),
```

### New HTTP metrics

```go
HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: "orion",
    Name:      "http_requests_total",
    Help:      "Total HTTP requests by method, path, and response code.",
}, []string{"method", "path", "status_code"}),

HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
    Namespace: "orion",
    Name:      "http_request_duration_seconds",
    Help:      "Histogram of HTTP request durations.",
    Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
}, []string{"method", "path"}),
```

### New DB metrics

```go
DBOperationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
    Namespace: "orion",
    Name:      "db_operation_duration_seconds",
    Help:      "Histogram of PostgreSQL operation durations by operation name.",
    Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
}, []string{"operation"}),
```

---

## 6. File 2: `internal/scheduler/scheduler.go` — Scheduler Instrumentation

### What to instrument

| Operation | Signal | Code |
|---|---|---|
| Full schedule tick | Histogram | `SchedulerCycleLatency.Observe(elapsed)` |
| Each job dispatched | Span | `span("dispatch_job", job_id=...)` |
| Each job enqueued | Counter | `JobsSubmitted.Inc(queue, type)` |
| Retry promotion | Counter | `JobsRetried.Inc(queue)` |
| Orphan reclaimed | Counter (log already handles this) | `logger.Warn` already exists |

### Exact code changes in `scheduleQueuedJobs`

```go
func (s *Scheduler) scheduleQueuedJobs(ctx context.Context) error {
    // Start a span for the entire dispatch cycle
    ctx, span := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_cycle")
    defer span.End()

    // Track cycle start for latency measurement
    cycleStart := time.Now()
    defer func() {
        s.metrics.SchedulerCycleLatency.Observe(time.Since(cycleStart).Seconds())
    }()

    status := domain.JobStatusQueued
    jobs, err := s.store.ListJobs(ctx, store.JobFilter{
        Status: &status,
        Limit:  s.cfg.BatchSize,
    })
    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        return fmt.Errorf("listing queued jobs: %w", err)
    }

    dispatched := 0
    for _, job := range jobs {
        // Child span per job for individual dispatch tracing
        _, jobSpan := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_job",
            trace.WithAttributes(
                attribute.String("job.id", job.ID.String()),
                attribute.String("job.type", string(job.Type)),
                attribute.String("job.queue", job.QueueName),
            ),
        )

        if err := s.store.TransitionJobState(ctx,
            job.ID, domain.JobStatusQueued, domain.JobStatusScheduled,
        ); err != nil {
            s.logger.Debug("state conflict scheduling job", "job_id", job.ID)
            jobSpan.End()
            continue
        }

        if err := s.queue.Enqueue(ctx, job); err != nil {
            _ = s.store.TransitionJobState(ctx,
                job.ID, domain.JobStatusScheduled, domain.JobStatusQueued,
            )
            s.logger.Error("failed to enqueue job", "job_id", job.ID, "err", err)
            jobSpan.SetStatus(codes.Error, err.Error())
            jobSpan.End()
            continue
        }

        // Counter increment: job successfully dispatched
        s.metrics.JobsSubmitted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
        jobSpan.End()
        dispatched++
    }

    // Annotate the cycle span with total dispatched
    span.SetAttributes(attribute.Int("jobs.dispatched", dispatched))
    if dispatched > 0 {
        s.logger.Info("dispatched jobs to queue", "count", dispatched)
    }
    return nil
}
```

### What changes in `Scheduler` struct

```go
type Scheduler struct {
    cfg      Config
    db       *pgxpool.Pool
    store    store.Store
    queue    queue.Queue
    advancer *pipeline.Advancer
    metrics  *observability.Metrics  // ← Phase 6 addition
    logger   *slog.Logger
}

// New() updated to accept *observability.Metrics
func New(cfg Config, db *pgxpool.Pool, s store.Store, q queue.Queue,
    adv *pipeline.Advancer, m *observability.Metrics, logger *slog.Logger) *Scheduler
```

---

## 7. File 3: `internal/worker/pool.go` — Worker Instrumentation

### What to instrument

| Operation | Signal | Code |
|---|---|---|
| Job execution duration | Histogram | `JobDuration.Observe(elapsed, queue, type, status)` |
| Active job count | Gauge | `WorkerActiveJobs.Set(activeCount, worker_id)` |
| Job completed | Counter | `JobsCompleted.Inc(queue, type)` |
| Job failed | Counter | `JobsFailed.Inc(queue, type, reason)` |
| Job dead | Counter | `JobsDead.Inc(queue)` |
| Orphan reclaimed | (already logged) | no metric needed yet |

### Exact code changes in `executeJob`

```go
func (p *Pool) executeJob(ctx context.Context, task *jobTask, logger *slog.Logger) {
    job := task.job
    startedAt := time.Now()

    // ── Span for the full job execution ──────────────────────────────────────
    ctx, span := observability.Tracer("orion.worker").Start(ctx, "worker.execute_job",
        trace.WithAttributes(
            attribute.String("job.id", job.ID.String()),
            attribute.String("job.type", string(job.Type)),
            attribute.String("job.queue", job.QueueName),
            attribute.Int("job.attempt", job.Attempt),
        ),
    )
    defer span.End()

    // ── Update active jobs gauge ──────────────────────────────────────────────
    // This is already tracked by activeCount atomic — report it to Prometheus
    p.metrics.WorkerActiveJobs.WithLabelValues(p.cfg.WorkerID).Set(
        float64(p.activeCount.Load()),
    )

    // ... existing MarkJobRunning and RecordExecution ...

    executor := p.resolveExecutor(job.Type)
    // ... existing no-executor handling ...

    execCtx := ctx
    if job.Deadline != nil {
        var cancel context.CancelFunc
        execCtx, cancel = context.WithDeadline(ctx, *job.Deadline)
        defer cancel()
    }

    err := executor.Execute(execCtx, job)
    finishedAt := time.Now()
    elapsed := finishedAt.Sub(startedAt).Seconds()

    if err != nil {
        // Determine failure reason for the label
        reason := "handler_error"
        if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
            reason = "timeout"
        }

        p.metrics.JobsFailed.WithLabelValues(job.QueueName, string(job.Type), reason).Inc()
        p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "failed").Observe(elapsed)

        // If this was the last retry → dead
        if !job.IsRetryable() {
            p.metrics.JobsDead.WithLabelValues(job.QueueName).Inc()
        } else {
            p.metrics.JobsRetried.WithLabelValues(job.QueueName).Inc()
        }

        span.SetStatus(codes.Error, err.Error())
        span.SetAttributes(attribute.String("error.reason", reason))
        // ... existing MarkJobFailed and RecordExecution ...
        return
    }

    // Success path
    p.metrics.JobsCompleted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
    p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "completed").Observe(elapsed)
    span.SetAttributes(attribute.Float64("job.duration_seconds", elapsed))
    // ... existing MarkJobCompleted and RecordExecution ...
}
```

### What changes in Pool struct

```go
type Pool struct {
    cfg       WorkerConfig
    queue     queue.Queue
    store     store.Store
    executors []Executor
    metrics   *observability.Metrics  // ← Phase 6 addition
    logger    *slog.Logger
    jobCh       chan *jobTask
    activeCount atomic.Int32
    wg          sync.WaitGroup
}

// NewPool updated to accept *observability.Metrics
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store,
    executors []Executor, m *observability.Metrics, logger *slog.Logger) *Pool
```

---

## 8. File 4: `internal/pipeline/advancement.go` — Pipeline Metrics

### What to instrument

| Event | Signal | Where |
|---|---|---|
| Pipeline transitions pending→running | `PipelineStarted.Inc(name)` | `advanceOne` step 4 |
| Pipeline completes | `PipelineCompleted.Inc(name)` | `advanceOne` step 7 |
| Pipeline fails | `PipelineFailed.Inc(name)` | `advanceOne` step 3 |
| Pipeline duration | `PipelineDuration.Observe(elapsed, status)` | On terminal state |
| Node job created | `PipelineNodesTotal.Inc(name, "started")` | step 6 after CreateJob |
| Full AdvanceAll() | `AdvancerCycleDuration.Observe(elapsed)` | `AdvanceAll` |

### Exact code changes in `AdvanceAll`

```go
func (a *Advancer) AdvanceAll(ctx context.Context) error {
    ctx, span := observability.Tracer("orion.pipeline").Start(ctx, "advancer.advance_all")
    defer span.End()

    start := time.Now()
    defer func() {
        a.metrics.AdvancerCycleDuration.Observe(time.Since(start).Seconds())
    }()

    // ... existing pending + running queries ...

    span.SetAttributes(attribute.Int("pipelines.advanced", len(toAdvance)))
    return nil
}
```

### Exact code changes in `advanceOne` at the key points

```go
// Step 3 — failure detection
if pj.JobStatus == domain.JobStatusDead {
    a.metrics.PipelineFailed.WithLabelValues(p.Name).Inc()
    if p.CompletedAt != nil && !p.CreatedAt.IsZero() {
        elapsed := p.CompletedAt.Sub(p.CreatedAt).Seconds()
        a.metrics.PipelineDuration.WithLabelValues("failed").Observe(elapsed)
    }
    // ... existing cascade + UpdatePipelineStatus ...
}

// Step 4 — pending → running
if p.Status == domain.PipelineStatusPending {
    a.metrics.PipelineStarted.WithLabelValues(p.Name).Inc()
    // ... existing UpdatePipelineStatus ...
}

// Step 6 — job created
a.metrics.PipelineNodesTotal.WithLabelValues(p.Name, "started").Inc()

// Step 7 — completion
a.metrics.PipelineCompleted.WithLabelValues(p.Name).Inc()
now := time.Now()
elapsed := now.Sub(p.CreatedAt).Seconds()
a.metrics.PipelineDuration.WithLabelValues("completed").Observe(elapsed)
```

### What changes in Advancer struct

```go
type Advancer struct {
    store   store.Store
    metrics *observability.Metrics  // ← Phase 6 addition
    logger  *slog.Logger
}

func NewAdvancer(s store.Store, m *observability.Metrics, logger *slog.Logger) *Advancer
```

---

## 9. File 5: `internal/api/handler/job.go` — HTTP Instrumentation

### Pattern: middleware wrapper

Instead of adding metrics calls to every handler individually, wrap the mux with a middleware that records HTTP metrics for all routes:

```go
// internal/api/handler/middleware.go (new small file)

// MetricsMiddleware records HTTP request counts and durations for all routes.
// Wraps any http.Handler and reports to the Orion Metrics.
func MetricsMiddleware(m *observability.Metrics, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()

        // Wrap ResponseWriter to capture the status code
        wrapped := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

        // Add trace context propagation: if caller sends W3C traceparent header,
        // this span is a child of their trace. Otherwise, a new root span.
        ctx, span := observability.Tracer("orion.api").Start(r.Context(), "http."+r.Method+" "+r.Pattern,
            trace.WithAttributes(
                attribute.String("http.method", r.Method),
                attribute.String("http.route", r.Pattern),
            ),
        )
        defer span.End()

        next.ServeHTTP(wrapped, r.WithContext(ctx))

        elapsed := time.Since(start).Seconds()
        statusStr := strconv.Itoa(wrapped.statusCode)

        // Normalize route pattern: use r.Pattern (Go 1.22) for clean labels
        // e.g. "GET /jobs/{id}" not "/jobs/abc-123-def"
        route := r.Pattern
        if route == "" {
            route = r.URL.Path
        }

        m.HTTPRequestsTotal.WithLabelValues(r.Method, route, statusStr).Inc()
        m.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(elapsed)

        span.SetAttributes(attribute.Int("http.status_code", wrapped.statusCode))
    })
}

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
    http.ResponseWriter
    statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
    r.statusCode = code
    r.ResponseWriter.WriteHeader(code)
}
```

### Applied in `cmd/api/main.go`

```go
// Replace:
Handler: mux,

// With:
Handler: handler.MetricsMiddleware(metrics, mux),
```

This instruments ALL routes (jobs + pipelines + health) with zero per-handler code changes.

---

## 10. File 6: `internal/queue/redis/redis_queue.go` — Queue Metrics

### What to instrument

The queue depth gauge should be updated after every enqueue and periodically. The simplest approach: update it in `Enqueue` and `Dequeue`.

```go
// In Enqueue, after successful XADD:
// Read the stream length and set the gauge
length, err := q.client.XLen(ctx, streamKey).Result()
if err == nil {
    q.metrics.QueueDepth.WithLabelValues(queueName).Set(float64(length))
}

// In Dequeue, after successful XREADGROUP:
// Decrement is implicit — the gauge is updated on next Enqueue
// For more accuracy, also call XLen in Dequeue
```

### Alternative: background poller (preferred for accuracy)

```go
// StartQueueDepthPoller runs in a goroutine and updates queue depth gauges every 5s.
// This is more accurate than per-operation updates because it captures depth
// even when there are no enqueue/dequeue operations happening.
func (q *RedisQueue) StartQueueDepthPoller(ctx context.Context, queueNames []string) {
    ticker := time.NewTicker(5 * time.Second)
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                for _, name := range queueNames {
                    length, err := q.client.XLen(ctx, name).Result()
                    if err == nil {
                        q.metrics.QueueDepth.WithLabelValues(name).Set(float64(length))
                    }
                }
            }
        }
    }()
}
```

---

## 11. File 7: `internal/store/postgres/db.go` — DB Span Tracing

### Pattern: wrap every method with a span

Every store method should start a child span so database latency appears in Jaeger traces.

```go
// Shared helper: startDBSpan wraps a DB operation in a span.
// Usage:
//   ctx, span := startDBSpan(ctx, "db.CreateJob")
//   defer span.End()
func startDBSpan(ctx context.Context, operation string) (context.Context, trace.Span) {
    return observability.Tracer("orion.db").Start(ctx, operation,
        trace.WithAttributes(
            attribute.String("db.system", "postgresql"),
            attribute.String("db.operation", operation),
        ),
    )
}

// Applied to CreateJob:
func (db *DB) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
    ctx, span := startDBSpan(ctx, "db.CreateJob")
    defer span.End()

    // ... existing code ...

    if err != nil {
        span.SetStatus(codes.Error, err.Error())
        return nil, err
    }
    return job, nil
}
```

Apply this to the highest-frequency methods first:
1. `MarkJobRunning` — called on every job execution
2. `MarkJobCompleted` — called on every success
3. `MarkJobFailed` — called on every failure
4. `CreateJob` — called on every submit and pipeline node creation
5. `ListJobs` — called on every scheduler tick
6. `GetPipelineJobs` — called on every pipeline advancement

Also add the DB operation duration metric:

```go
func (db *DB) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
    start := time.Now()
    defer func() {
        db.metrics.DBOperationDuration.WithLabelValues("CreateJob").
            Observe(time.Since(start).Seconds())
    }()
    // ...
}
```

---

## 12. File 8: `deploy/grafana/dashboards/orion.json`

The Grafana dashboard is a JSON file provisioned automatically via the Docker Compose volume mount. When Grafana starts, it reads `./deploy/grafana/dashboards/orion.json` and creates the dashboard automatically.

### Dashboard structure — 9 panels in 3 rows

```
Row 1 — Job Throughput
┌─────────────────────┬─────────────────────┬─────────────────────┐
│  Job Throughput      │  Error Rate          │  Queue Depth        │
│  rate(completed[1m]) │  failed/(total)      │  depth per queue    │
│  Type: Time series   │  Type: Stat          │  Type: Time series  │
└─────────────────────┴─────────────────────┴─────────────────────┘

Row 2 — Latency & Workers
┌─────────────────────┬─────────────────────┬─────────────────────┐
│  Job Duration p99    │  Worker Utilization  │  Scheduler Latency  │
│  histogram_quantile  │  active/concurrency  │  cycle_duration p95 │
│  Type: Time series   │  Type: Gauge         │  Type: Time series  │
└─────────────────────┴─────────────────────┴─────────────────────┘

Row 3 — Pipelines & Reliability
┌─────────────────────┬─────────────────────┬─────────────────────┐
│  Pipeline Status     │  Dead Job Rate       │  Retry Rate         │
│  started/completed   │  rate(dead[5m])      │  rate(retried[1m])  │
│  Type: Time series   │  Type: Stat          │  Type: Time series  │
└─────────────────────┴─────────────────────┴─────────────────────┘
```

### Key PromQL queries for each panel

```promql
# Job Throughput (jobs/sec by type)
rate(orion_jobs_completed_total[1m])

# Error Rate (% of executions that failed)
rate(orion_jobs_failed_total[5m]) /
  (rate(orion_jobs_completed_total[5m]) + rate(orion_jobs_failed_total[5m]))

# Queue Depth (live gauge per queue)
orion_queue_depth

# Job Duration p99 (by job type)
histogram_quantile(0.99, rate(orion_job_duration_seconds_bucket[5m]))

# Worker Utilization (active/concurrency)
sum(orion_worker_active_jobs) / count(orion_worker_active_jobs)

# Scheduler Cycle Latency p95
histogram_quantile(0.95, rate(orion_scheduler_cycle_duration_seconds_bucket[5m]))

# Pipeline Completion Rate
rate(orion_pipelines_completed_total[5m])

# Dead Job Rate (DLQ accumulation)
rate(orion_jobs_dead_total[5m])

# Retry Rate
rate(orion_jobs_retried_total[5m])
```

---

## 13. File 9: `deploy/prometheus/prometheus.yml` — Scrape Config

All three Orion services expose a `/metrics` endpoint on their metrics port. Prometheus needs to scrape all three.

```yaml
# deploy/prometheus/prometheus.yml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  # API Server metrics
  - job_name: 'orion-api'
    static_configs:
      - targets: ['host.docker.internal:9091']
    metrics_path: /metrics
    scrape_interval: 15s

  # Scheduler metrics
  - job_name: 'orion-scheduler'
    static_configs:
      - targets: ['host.docker.internal:9092']
    metrics_path: /metrics
    scrape_interval: 15s

  # Worker metrics (may have multiple instances)
  - job_name: 'orion-worker'
    static_configs:
      - targets: ['host.docker.internal:9093']
    metrics_path: /metrics
    scrape_interval: 15s
```

Each service starts a `MetricsServer` (already defined in `observability.go`) on its metrics port. Add to each `cmd/*/main.go`:

```go
// Start metrics server in background goroutine
reg := prometheus.NewRegistry()
metrics := observability.NewMetrics(reg)
metricsSrv := observability.MetricsServer(cfg.Observability.MetricsPort, reg)
go func() {
    if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        logger.Error("metrics server error", "err", err)
    }
}()
defer metricsSrv.Shutdown(context.Background())
```

---

## 14. OpenTelemetry Span Hierarchy

This is the complete span tree for a typical job lifecycle in Jaeger:

```
trace_id: f4d8e92a1b3c...
│
├── orion-api: http.POST /jobs                                  [2ms]
│   └── orion-api: db.CreateJob                                 [1.8ms]
│
├── orion-scheduler: scheduler.dispatch_cycle                    [0.8ms]
│   └── orion-scheduler: scheduler.dispatch_job                 [0.6ms]
│       └── orion-scheduler: redis.Enqueue                      [0.4ms]
│
└── orion-worker: worker.execute_job                            [47.2s]
    ├── orion-worker: db.MarkJobRunning                         [1ms]
    ├── orion-worker: executor.Execute (inline or k8s)          [47.1s]
    │   └── [k8s only] k8s.CreateJob                           [50ms]
    │   └── [k8s only] k8s.WatchJob                            [47.0s]
    ├── orion-worker: db.RecordExecution                        [1ms]
    └── orion-worker: db.MarkJobCompleted                       [1ms]
```

### For pipeline jobs, the trace is extended

```
trace_id: a9b1c2d3...
│
├── orion-api: http.POST /pipelines                             [3ms]
│   └── orion-api: db.CreatePipeline                           [2ms]
│
├── orion-scheduler: advancer.advance_all                       [5ms]  ← tick 1
│   └── orion-pipeline: advancer.advance_pipeline              [4ms]
│       └── orion-pipeline: db.GetPipelineJobs                 [1ms]
│
└── [per node, separate traces linked by pipeline_id baggage]
    orion-worker: worker.execute_job (preprocess)              [3s]
    orion-worker: worker.execute_job (train)                   [44s]
    orion-worker: worker.execute_job (evaluate)                [2s]
```

---

## 15. Metric Catalogue — Every Metric Defined

| Metric name | Type | Labels | Description | Increment when |
|---|---|---|---|---|
| `orion_jobs_submitted_total` | Counter | `queue`, `type` | Jobs dispatched to Redis | Scheduler enqueues a job |
| `orion_jobs_completed_total` | Counter | `queue`, `type` | Jobs that returned nil | Worker `MarkJobCompleted` |
| `orion_jobs_failed_total` | Counter | `queue`, `type`, `reason` | Jobs that returned error | Worker `MarkJobFailed` |
| `orion_jobs_retried_total` | Counter | `queue` | Retry attempts | Job re-enqueued after failure |
| `orion_jobs_dead_total` | Counter | `queue` | Jobs reaching dead state | Job has no retries left |
| `orion_job_duration_seconds` | Histogram | `queue`, `type`, `status` | Execution wall time | On job completion/failure |
| `orion_queue_depth` | Gauge | `queue` | Current pending count | Queue poller (5s interval) |
| `orion_worker_active_jobs` | Gauge | `worker_id` | Currently executing jobs | On executeJob enter/exit |
| `orion_scheduler_cycle_duration_seconds` | Histogram | — | Time for one dispatch loop | End of `scheduleQueuedJobs` |
| `orion_pipelines_started_total` | Counter | `pipeline_name` | Pipelines gone to running | `pending→running` transition |
| `orion_pipelines_completed_total` | Counter | `pipeline_name` | Pipelines fully done | All nodes completed |
| `orion_pipelines_failed_total` | Counter | `pipeline_name` | Pipelines with dead node | Dead node detected |
| `orion_pipeline_duration_seconds` | Histogram | `status` | Pipeline wall time | On terminal state |
| `orion_pipeline_nodes_total` | Counter | `pipeline_name`, `node_status` | Node state transitions | Node job created or terminal |
| `orion_advancer_cycle_duration_seconds` | Histogram | — | Time for one `AdvanceAll()` | End of `AdvanceAll` |
| `orion_http_requests_total` | Counter | `method`, `path`, `status_code` | All HTTP requests | Middleware on every request |
| `orion_http_request_duration_seconds` | Histogram | `method`, `path` | HTTP request latency | Middleware on every request |
| `orion_db_operation_duration_seconds` | Histogram | `operation` | PostgreSQL call latency | Start/end of each DB method |

---

## 16. Passing Metrics and Tracers Through the System

### The dependency chain

```
cmd/api/main.go
  → reg = prometheus.NewRegistry()
  → metrics = observability.NewMetrics(reg)
  → handler.NewJobHandler(pgStore, metrics, logger)
  → handler.NewPipelineHandler(pgStore, metrics, logger)
  → handler.MetricsMiddleware(metrics, mux)

cmd/scheduler/main.go
  → metrics = observability.NewMetrics(reg)
  → adv = pipeline.NewAdvancer(pgStore, metrics, logger)
  → scheduler.New(cfg, db, pgStore, queue, adv, metrics, logger)

cmd/worker/main.go
  → metrics = observability.NewMetrics(reg)
  → pool = worker.NewPool(cfg, queue, pgStore, executors, metrics, logger)
```

### Why each service creates its own Metrics instance

Each binary (`api`, `scheduler`, `worker`) is a separate process with its own Prometheus registry. They do not share state. Each service only records the metrics relevant to it:
- API: HTTP metrics + db.CreateJob
- Scheduler: dispatch cycle + job submission counters
- Worker: job duration + execution counts + active gauge

This is correct — in production there are multiple worker instances. Each reports its own `orion_worker_active_jobs` with its own `worker_id` label.

### Why Tracer() doesn't need passing

`observability.Tracer("orion.worker")` calls `otel.Tracer()` on the global provider set in `SetupTracing`. The global provider is set once at startup — any code in the same process can call `Tracer()` without a reference passed around. This is the standard OpenTelemetry Go pattern.

---

## 17. Step-by-Step Build Order

### Step 1 — Update `observability.go`

Add 9 new metrics to `NewMetrics`. Register them. Run:
```bash
go build ./internal/observability/...
# Must compile
```

### Step 2 — Update `internal/scheduler/scheduler.go`

Add `metrics *observability.Metrics` field. Update `New()`. Add span + counter in `scheduleQueuedJobs`. Run:
```bash
go build ./internal/scheduler/...
# Will fail because cmd/scheduler/main.go passes wrong args to New()
```

### Step 3 — Update `cmd/scheduler/main.go`

Create `prometheus.NewRegistry()`, `observability.NewMetrics(reg)`, metrics server goroutine, pass `metrics` to `scheduler.New()`. Run:
```bash
go build ./cmd/scheduler/...
# Must compile
```

### Step 4 — Update `internal/pipeline/advancement.go`

Add `metrics` field to `Advancer`. Update `NewAdvancer()`. Add 6 metric calls. Run:
```bash
go build ./internal/pipeline/...
# Will fail: cmd/scheduler/main.go passes wrong args to NewAdvancer
```

### Step 5 — Update `cmd/scheduler/main.go` again

Pass `metrics` to `pipeline.NewAdvancer()`. Run:
```bash
go build ./cmd/scheduler/...
# Must compile
```

### Step 6 — Update `internal/worker/pool.go`

Add `metrics` field. Update `NewPool()`. Add metrics in `executeJob`. Run:
```bash
go build ./internal/worker/...
# Will fail: cmd/worker/main.go passes wrong args to NewPool
```

### Step 7 — Update `cmd/worker/main.go`

Create metrics, pass to `NewPool`. Start metrics server. Run:
```bash
go build ./cmd/worker/...
# Must compile
```

### Step 8 — Create `internal/api/handler/middleware.go`

Write `MetricsMiddleware` and `statusRecorder`. Update `cmd/api/main.go` to wrap the mux. Run:
```bash
go build ./cmd/api/...
# Must compile
```

### Step 9 — Update `internal/queue/redis/redis_queue.go`

Add `StartQueueDepthPoller`. Call it in `cmd/worker/main.go` and `cmd/scheduler/main.go`. Run:
```bash
go build ./...
# All 3 binaries compile
```

### Step 10 — Update `internal/store/postgres/db.go`

Add `startDBSpan` helper. Wrap top-5 methods. Run:
```bash
go build ./internal/store/...
# Must compile
```

### Step 11 — Create Grafana dashboard and update prometheus.yml

```bash
# Verify dashboard file syntax
cat deploy/grafana/dashboards/orion.json | python3 -m json.tool > /dev/null
echo "✅ valid JSON"
```

### Step 12 — Make build and verify

```bash
make build
# All 3 binaries built: bin/orion-api bin/orion-scheduler bin/orion-worker

go test -race ./... -count=1
# All existing tests still pass (metrics are injected — tests use nil metrics gracefully)
```

---

## 18. Complete Verification Sequence

### Tier 1 — Metrics endpoints are reachable

```bash
# Start all services
make infra-up && make migrate-up
make run-api        # :8080 (API) + :9091 (metrics)
make run-scheduler  # + :9092 (metrics)
make run-worker     # + :9093 (metrics)

# Verify metrics endpoints
curl -s localhost:9091/metrics | grep "orion_"
# Expected: orion_http_requests_total, orion_jobs_submitted_total, etc.

curl -s localhost:9092/metrics | grep "orion_"
# Expected: orion_scheduler_cycle_duration_seconds, etc.

curl -s localhost:9093/metrics | grep "orion_"
# Expected: orion_worker_active_jobs, orion_job_duration_seconds, etc.

echo "✅ TIER 1 — All metrics endpoints healthy"
```

### Tier 2 — Metrics increment correctly

```bash
# Submit 10 jobs and verify counter increments
for i in $(seq 1 10); do
  curl -s -X POST localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"obs-test","type":"inline","payload":{"handler_name":"noop"}}' \
    > /dev/null
done

sleep 10  # let them complete

# Check Prometheus
COMPLETED=$(curl -s "localhost:9090/api/v1/query?query=orion_jobs_completed_total" \
  | jq '.data.result[0].value[1]')
echo "Completed: $COMPLETED"  # Should be >= 10

DURATION=$(curl -s "localhost:9090/api/v1/query?query=orion_job_duration_seconds_count" \
  | jq '.data.result[0].value[1]')
echo "Duration samples: $DURATION"  # Should be >= 10

echo "✅ TIER 2 — Metrics incrementing correctly"
```

### Tier 3 — Traces appear in Jaeger

```bash
JOB_ID=$(curl -s -X POST localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"trace-test","type":"inline","payload":{"handler_name":"echo","args":{"test":"hello"}}}' \
  | jq -r .id)

sleep 5  # let it complete

# Open Jaeger UI and search for service "orion-worker"
echo "Open: http://localhost:16686/search?service=orion-worker"
echo "Look for: worker.execute_job span for job $JOB_ID"
echo "✅ TIER 3 — Traces visible in Jaeger"
```

### Tier 4 — Grafana dashboard loads with live data

```bash
echo "Open: http://localhost:3000/d/orion (admin/admin)"
# All 9 panels should show data after the jobs above
# Queue Depth: 0 (all consumed)
# Job Throughput: ~10/min rate
# Error Rate: 0%
echo "✅ TIER 4 — Grafana dashboard populated"
```

### Tier 5 — Pipeline metrics

```bash
PIPELINE_ID=$(curl -s -X POST localhost:8080/pipelines \
  -H "Content-Type: application/json" \
  -d '{
    "name": "obs-pipeline-test",
    "dag_spec": {
      "nodes": [
        {"id": "a", "job_template": {"handler_name": "noop"}},
        {"id": "b", "job_template": {"handler_name": "noop"}}
      ],
      "edges": [{"source":"a","target":"b"}]
    }
  }' | jq -r .id)

sleep 15  # let pipeline complete

PIPELINE_COMPLETED=$(curl -s \
  "localhost:9090/api/v1/query?query=orion_pipelines_completed_total" \
  | jq '.data.result[0].value[1]')
echo "Pipelines completed: $PIPELINE_COMPLETED"  # Should be >= 1

echo "✅ TIER 5 — Pipeline metrics working"
```

---

## 19. PromQL Queries — Copy-Paste Reference

```promql
# ── Throughput ────────────────────────────────────────────────────────────────

# Jobs completed per second (last 1 minute)
rate(orion_jobs_completed_total[1m])

# Jobs completed per second by type
rate(orion_jobs_completed_total[1m]) by (type)

# Total throughput (submitted/completed/failed)
sum(rate(orion_jobs_submitted_total[5m]))
sum(rate(orion_jobs_completed_total[5m]))
sum(rate(orion_jobs_failed_total[5m]))

# ── Error Rates ───────────────────────────────────────────────────────────────

# Error rate as percentage
100 * rate(orion_jobs_failed_total[5m]) /
  (rate(orion_jobs_completed_total[5m]) + rate(orion_jobs_failed_total[5m]))

# Dead jobs per hour
rate(orion_jobs_dead_total[1h]) * 3600

# ── Latency ───────────────────────────────────────────────────────────────────

# p50 job duration
histogram_quantile(0.50, rate(orion_job_duration_seconds_bucket[5m]))

# p95 job duration by type
histogram_quantile(0.95, sum by (le, type) (rate(orion_job_duration_seconds_bucket[5m])))

# p99 job duration (overall)
histogram_quantile(0.99, rate(orion_job_duration_seconds_bucket[5m]))

# Scheduler cycle latency p95
histogram_quantile(0.95, rate(orion_scheduler_cycle_duration_seconds_bucket[5m]))

# HTTP request latency p99 by endpoint
histogram_quantile(0.99, sum by (le, path) (rate(orion_http_request_duration_seconds_bucket[5m])))

# ── Queue Health ──────────────────────────────────────────────────────────────

# Current queue depth by queue
orion_queue_depth

# Queue depth trend (growing = backpressure)
deriv(orion_queue_depth[5m])

# ── Workers ───────────────────────────────────────────────────────────────────

# Total active jobs across all workers
sum(orion_worker_active_jobs)

# Worker utilization (% of capacity busy)
sum(orion_worker_active_jobs) / count(orion_worker_active_jobs) * 100

# ── Pipelines ─────────────────────────────────────────────────────────────────

# Pipelines completed per hour
rate(orion_pipelines_completed_total[1h]) * 3600

# Pipeline failure rate
rate(orion_pipelines_failed_total[5m]) /
  (rate(orion_pipelines_completed_total[5m]) + rate(orion_pipelines_failed_total[5m]))

# Average pipeline duration
rate(orion_pipeline_duration_seconds_sum[10m]) /
  rate(orion_pipeline_duration_seconds_count[10m])

# ── DB Performance ────────────────────────────────────────────────────────────

# p95 DB operation latency by operation
histogram_quantile(0.95, sum by (le, operation) (rate(orion_db_operation_duration_seconds_bucket[5m])))

# Slowest DB operation (last 5 min)
topk(3, histogram_quantile(0.99, sum by (le, operation) (rate(orion_db_operation_duration_seconds_bucket[5m]))))
```

---

## 20. Common Mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| Not nil-checking metrics before use | Panic in tests when `metrics.JobsCompleted` is nil | Make test-facing constructors accept `nil` metrics and no-op; or always inject a test registry |
| Using same registry across all services | Duplicate registration panic if binaries share init code | Each binary creates its own `prometheus.NewRegistry()` |
| Cardinality explosion in labels | Prometheus OOM with millions of unique label combinations | Never use `job.ID` as a label — use `job.Type` and `queue` only |
| Span context not propagated | Jaeger shows disconnected spans instead of a tree | Pass `ctx` returned by `Tracer.Start()` to all downstream calls |
| Metrics server on same port as API | Port conflict on startup | API: `:8080` + `:9091`; Scheduler: `:9092`; Worker: `:9093` |
| Observing duration before function returns | Zero durations in histogram | Use `defer` with closure over start time |
| Counter not initialized on first use | `nil pointer` when calling `.Inc()` | `NewMetrics` pre-registers all counters — never skip registration |
| Queue depth gauge never decremented | Gauge shows ever-growing depth | Use `Set(XLEN)` not `Inc()`/`Dec()` — XLEN gives the true current value |

---

## 21. Phase 7 Preview

After Phase 6, Orion is fully observable. What's still missing: **real-time updates**.

Currently, clients must poll `GET /jobs/{id}` every second to track job status. Phase 7 adds a gRPC streaming endpoint:

```protobuf
service OrionService {
  rpc WatchJob(WatchJobRequest) returns (stream JobStatusUpdate) {}
  rpc WatchPipeline(WatchPipelineRequest) returns (stream PipelineStatusUpdate) {}
}
```

Clients get pushed status updates as transitions happen:
```
grpcurl ... WatchJob {job_id: "abc123"}
# queued → scheduled → running → completed
# each update pushed within milliseconds of the DB transition
```

Phase 7 adds: `api/orion.proto`, generated Go stubs, gRPC server on `:9000`, fan-out broadcaster (sends to all active watchers when a job transitions), and integration with the existing PostgreSQL state machine.

---

## Summary Checklist

```
□ internal/observability/observability.go
    □ PipelineStarted, PipelineCompleted, PipelineFailed (CounterVec)
    □ PipelineDuration, AdvancerCycleDuration (HistogramVec / Histogram)
    □ PipelineNodesTotal (CounterVec)
    □ HTTPRequestsTotal, HTTPRequestDuration (CounterVec, HistogramVec)
    □ DBOperationDuration (HistogramVec)
    □ All 9 new metrics registered in NewMetrics()

□ internal/scheduler/scheduler.go
    □ metrics field added
    □ New() accepts *observability.Metrics
    □ dispatch_cycle span wrapping scheduleQueuedJobs
    □ dispatch_job child span per job
    □ JobsSubmitted.Inc() on successful enqueue
    □ SchedulerCycleLatency.Observe() via defer

□ internal/worker/pool.go
    □ metrics field added
    □ NewPool() accepts *observability.Metrics
    □ execute_job span wrapping executeJob
    □ JobsCompleted.Inc() on success
    □ JobsFailed.Inc() with reason label on failure
    □ JobsDead.Inc() when no retries remain
    □ JobsRetried.Inc() when retry is scheduled
    □ JobDuration.Observe() on both success and failure
    □ WorkerActiveJobs.Set() reflects activeCount

□ internal/pipeline/advancement.go
    □ metrics field added to Advancer
    □ NewAdvancer() accepts *observability.Metrics
    □ advancer.advance_all span in AdvanceAll()
    □ AdvancerCycleDuration.Observe() via defer
    □ PipelineStarted.Inc() on pending→running
    □ PipelineCompleted.Inc() + PipelineDuration.Observe() on completion
    □ PipelineFailed.Inc() + PipelineDuration.Observe() on failure
    □ PipelineNodesTotal.Inc("started") on job creation

□ internal/api/handler/middleware.go  (NEW small file)
    □ MetricsMiddleware wraps entire mux
    □ statusRecorder captures HTTP status code
    □ HTTPRequestsTotal.Inc() per request
    □ HTTPRequestDuration.Observe() per request
    □ Trace span per request with route attribute

□ internal/queue/redis/redis_queue.go
    □ StartQueueDepthPoller() goroutine
    □ QueueDepth.Set(XLen) every 5s per queue

□ internal/store/postgres/db.go
    □ startDBSpan() helper
    □ Spans on: MarkJobRunning, MarkJobCompleted, MarkJobFailed,
                CreateJob, ListJobs, GetPipelineJobs
    □ DBOperationDuration.Observe() on same 6 methods

□ cmd/api/main.go
    □ prometheus.NewRegistry() + observability.NewMetrics(reg)
    □ MetricsServer(:9091) started in goroutine
    □ MetricsMiddleware(metrics, mux) wraps handler
    □ metrics passed to NewJobHandler, NewPipelineHandler

□ cmd/scheduler/main.go
    □ prometheus.NewRegistry() + observability.NewMetrics(reg)
    □ MetricsServer(:9092) started in goroutine
    □ metrics passed to pipeline.NewAdvancer
    □ metrics passed to scheduler.New

□ cmd/worker/main.go
    □ prometheus.NewRegistry() + observability.NewMetrics(reg)
    □ MetricsServer(:9093) started in goroutine
    □ metrics passed to worker.NewPool
    □ queue.StartQueueDepthPoller() called

□ deploy/grafana/dashboards/orion.json
    □ 9 panels across 3 rows
    □ All PromQL queries validated
    □ Dashboard UID: "orion-main"
    □ Prometheus datasource referenced by name "Prometheus"

□ deploy/prometheus/prometheus.yml
    □ 3 scrape targets: orion-api (:9091), orion-scheduler (:9092), orion-worker (:9093)
    □ scrape_interval: 15s

□ Verification
    □ All 3 metrics endpoints return orion_ metrics
    □ 10 noop jobs → orion_jobs_completed_total increments by 10
    □ Jaeger shows full trace tree for a single job
    □ Grafana dashboard loads all 9 panels with live data
    □ Pipeline test → orion_pipelines_completed_total increments
    □ go test -race ./... passes with no regressions
```