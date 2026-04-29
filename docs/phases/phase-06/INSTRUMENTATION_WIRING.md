# Phase 6 — Instrumentation Across Pool, Pipeline, Scheduler, Queue, and Cmd
## `pool.go` · `advancement.go` · `scheduler.go` · `redis_queue.go` · `cmd/` · `prometheus.yml` · `orion.json`

---

## `internal/worker/pool.go` — Job Execution Instrumentation

### What changed

Two additions to the `Pool` struct and `NewPool` signature:

```go
// Before (Phase 5):
type Pool struct {
    cfg       WorkerConfig
    queue     queue.Queue
    store     store.Store
    executors []Executor
    logger    *slog.Logger
    ...
}
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store,
    executors []Executor, logger *slog.Logger) *Pool

// After (Phase 6):
type Pool struct {
    ...
    metrics *observability.Metrics  // ← added (nil-safe)
    ...
}
func NewPool(cfg WorkerConfig, q queue.Queue, s store.Store,
    executors []Executor, m *observability.Metrics, logger *slog.Logger) *Pool
```

### The five instrumentation points in `executeJob`

**1. Span — wraps the entire job execution:**
```go
ctx, span := observability.Tracer("orion.worker").Start(ctx, "worker.execute_job",
    trace.WithAttributes(
        attribute.String("job.id", job.ID.String()),
        attribute.String("job.type", string(job.Type)),
        attribute.String("job.queue", job.QueueName),
        attribute.Int("job.attempt", job.Attempt),
    ),
)
defer span.End()
```
This span appears in Jaeger under service `orion-worker`. Its parent is the `scheduler.dispatch_job` span created when the job was enqueued (trace context propagated via the job payload).

**2. Active jobs gauge — updated on goroutine enter/exit:**
```go
p.activeCount.Add(1)
p.setActiveJobsGauge()        // WorkerActiveJobs.Set(activeCount)
// ... do work ...
p.activeCount.Add(-1)
p.setActiveJobsGauge()
```
The gauge is driven by the existing `activeCount` atomic — `setActiveJobsGauge` simply syncs it to Prometheus on every change.

**3. Failure counters — reason label for diagnosis:**
```go
reason := "handler_error"
if errors.Is(err, context.Canceled)      { reason = "cancelled" }
if errors.Is(err, context.DeadlineExceeded) { reason = "deadline_exceeded" }

p.metrics.JobsFailed.WithLabelValues(job.QueueName, string(job.Type), reason).Inc()
p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "failed").Observe(elapsed)
```
Three possible `reason` values (low-cardinality): `handler_error`, `cancelled`, `deadline_exceeded`. This lets you distinguish "my handler has a bug" from "jobs are timing out" in PromQL without touching logs.

**4. Dead vs retry distinction:**
```go
if !job.IsRetryable() {
    p.metrics.JobsDead.WithLabelValues(job.QueueName).Inc()
} else {
    p.metrics.JobsRetried.WithLabelValues(job.QueueName).Inc()
}
```
`JobsDead` increments only when a job has truly exhausted all retries. This is the signal to alert on — every dead job requires human investigation.

**5. Success counters + histogram:**
```go
p.metrics.JobsCompleted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
p.metrics.JobDuration.WithLabelValues(job.QueueName, string(job.Type), "completed").Observe(elapsed)
```
`elapsed` is measured from `startedAt` (set before `MarkJobRunning`) to `finishedAt` (set after `executor.Execute` returns). This captures total wall-clock time including DB calls, not just the handler's execution time.

---

## `internal/pipeline/advancement.go` — Pipeline Metrics

### What changed

`Advancer` gains a `metrics` field; `NewAdvancer` gains a third parameter:

```go
// Before: NewAdvancer(s store.Store, logger *slog.Logger)
// After:  NewAdvancer(s store.Store, m *observability.Metrics, logger *slog.Logger)
```

### Six instrumentation points

| Step | Event | Metric call |
|---|---|---|
| `AdvanceAll` entry | Measure full cycle | `AdvancerCycleDuration.Observe(elapsed)` via defer |
| Step 3 (failure) | Pipeline failed | `PipelineFailed.Inc(name)` + `PipelineDuration.Observe(elapsed, "failed")` |
| Step 3 (failure) | Node dead | `PipelineNodesTotal.Inc(name, "dead")` |
| Step 3 (cascade) | Node cancelled | `PipelineNodesTotal.Inc(name, "cancelled")` |
| Step 4 (pending→running) | Pipeline started | `PipelineStarted.Inc(name)` |
| Step 6 (job created) | Node started | `PipelineNodesTotal.Inc(name, "started")` |
| Step 7 (all done) | Pipeline completed | `PipelineCompleted.Inc(name)` + `PipelineDuration.Observe(elapsed, "completed")` |

### Duration measurement

Pipeline duration is measured from `p.CreatedAt` (set at `CreatePipeline` time) to `time.Now()` at the terminal state transition. This captures the full wall-clock time including scheduler tick lag, queue wait, and all node execution times.

```go
if !p.CreatedAt.IsZero() {
    a.metrics.PipelineDuration.WithLabelValues("completed").
        Observe(time.Since(p.CreatedAt).Seconds())
}
```

The `!p.CreatedAt.IsZero()` guard prevents a panic if `CreatedAt` was not populated by the store scan (defensive coding).

---

## `internal/scheduler/scheduler.go` — Cycle Latency + Dispatch Spans

### What changed

`Scheduler` struct gains `metrics *observability.Metrics`; `New()` gains the sixth parameter.

### Two instrumentation points in `scheduleQueuedJobs`

**Cycle span — root span for the entire dispatch tick:**
```go
ctx, span := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_cycle")
defer span.End()
```
Annotated with `jobs.found` and `jobs.dispatched` attributes so you can see dispatch efficiency in Jaeger without opening logs.

**Cycle latency histogram:**
```go
cycleStart := time.Now()
defer func() {
    if s.metrics != nil {
        s.metrics.SchedulerCycleLatency.Observe(time.Since(cycleStart).Seconds())
    }
}()
```
`defer` captures latency even if the function returns early on error. The histogram is the basis for the "Scheduler Cycle Latency p95" Grafana panel.

**Per-job child span:**
```go
_, jobSpan := observability.Tracer("orion.scheduler").Start(ctx, "scheduler.dispatch_job")
jobSpan.SetAttributes(
    attribute.String("job.id", job.ID.String()),
    attribute.String("job.type", string(job.Type)),
    attribute.String("job.queue", job.QueueName),
)
// ... enqueue ...
jobSpan.End()
```
Each job dispatch appears as a child span of `dispatch_cycle` in Jaeger. If enqueue fails, `jobSpan.SetStatus(codes.Error, ...)` marks it red.

---

## `internal/queue/redis/redis_queue.go` — Queue Depth Poller

### What changed

`RedisQueue` gains `metrics *observability.Metrics`; `New()` gains it as a parameter.

### `StartQueueDepthPoller`

```go
func (r *RedisQueue) StartQueueDepthPoller(ctx context.Context, queueNames []string) {
    if r.metrics == nil { return }   // no-op without metrics

    ticker := time.NewTicker(5 * time.Second)
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done(): return
            case <-ticker.C:
                for _, name := range queueNames {
                    length, err := r.client.XLen(ctx, r.streamForQueue(name)).Result()
                    if err == nil {
                        r.metrics.QueueDepth.WithLabelValues(name).Set(float64(length))
                    }
                }
            }
        }
    }()
}
```

Called in both `cmd/scheduler/main.go` and `cmd/worker/main.go` after queue initialisation:

```go
queue.StartQueueDepthPoller(ctx, []string{
    "orion:queue:high",
    "orion:queue:default",
    "orion:queue:low",
})
```

**Why `XLen` not `Inc/Dec`:**
`XLen` returns the true stream length including messages in the PEL (pending entry list — claimed but not yet ACKed). `Inc/Dec` would undercount because un-ACKed messages are still in the stream and represent real backlog. Polling every 5 seconds is accurate and costs three Redis commands per tick — negligible.

**Why both scheduler and worker poll:**
Both services consume from the queue but from different angles. The scheduler sees depth before dispatch; the worker sees it after claiming. Running both gives redundancy — if one service restarts, the other continues reporting depth. Prometheus deduplicates via labels.

---

## `cmd/` entrypoints — The Three Wiring Changes

Each binary follows the same pattern:

```go
// 1. Create isolated registry
reg := prometheus.NewRegistry()
metrics := observability.NewMetrics(reg)

// 2. Start metrics server in background goroutine
metricsSrv := observability.MetricsServer(cfg.Observability.MetricsPort, reg)
go func() { metricsSrv.ListenAndServe() }()
defer metricsSrv.Shutdown(context.Background())

// 3. Pass metrics to every constructor that needs it
```

**Port assignments:**

| Binary | API port | Metrics port |
|---|---|---|
| `orion-api` | `:8080` | `:9091` |
| `orion-scheduler` | — | `:9092` |
| `orion-worker` | — | `:9093` |

**`cmd/api/main.go` specific change — middleware layering:**
```go
instrumentedHandler := handler.TracingMiddleware(
    handler.MetricsMiddleware(metrics, mux),
)
srv := &http.Server{Handler: instrumentedHandler}
```
`TracingMiddleware` is the outermost layer — it creates the root span before `MetricsMiddleware` runs, so the span is available in the request context for downstream handlers to create child spans. `MetricsMiddleware` records duration *after* the inner handler returns (via `statusRecorder`), capturing the true final status code.

---

## `deploy/prometheus/prometheus.yml` — Scrape Configuration

Three scrape targets, one per binary:

```
orion-api       → host.docker.internal:9091
orion-scheduler → host.docker.internal:9092
orion-worker    → host.docker.internal:9093
```

`host.docker.internal` resolves to the Docker host machine from inside the Prometheus container. This works on Mac and Windows. On Linux, use the host's actual IP or `172.17.0.1` (default Docker bridge gateway).

Verify all targets are UP: **http://localhost:9090/targets**

---

## `deploy/grafana/dashboards/orion.json` — Pre-built Dashboard

The JSON is provisioned automatically via the Grafana volume mount in `docker-compose.yml`. When Grafana starts, it reads the file and creates the dashboard without manual import.

Add to `docker-compose.yml` under the `grafana` service:
```yaml
grafana:
  volumes:
    - ./deploy/grafana/dashboards:/etc/grafana/provisioning/dashboards
    - ./deploy/grafana/provisioning.yml:/etc/grafana/provisioning/dashboards/orion.yml
```

And create `deploy/grafana/provisioning.yml`:
```yaml
apiVersion: 1
providers:
  - name: orion
    folder: Orion
    type: file
    options:
      path: /etc/grafana/provisioning/dashboards
```

**Nine panels in 3×3 grid:**

| Panel | Type | Key query |
|---|---|---|
| Job Throughput | Time series | `rate(orion_jobs_completed_total[1m])` by queue+type |
| Error Rate | Stat (%) | failed/(completed+failed) |
| Queue Depth | Time series | `orion_queue_depth` by queue |
| Job Duration p50/p95/p99 | Time series | `histogram_quantile` by type |
| Worker Utilization | Gauge (%) | active/count × 100 |
| Scheduler + Advancer Latency | Time series | `histogram_quantile` p95 |
| Pipeline Throughput | Time series | started/completed/failed per min |
| Dead Jobs/Hour | Stat | `rate(dead[1h]) × 3600` |
| DB Operation Latency | Time series | `histogram_quantile` p95 by operation |

Open dashboard: **http://localhost:3000/d/orion-main** (admin/admin)

---

## Complete test sequence after Phase 6

```bash
# 1. Rebuild all binaries
make build

# 2. Start infra + services
make infra-up && make migrate-up
make run-api        # :8080 + :9091
make run-scheduler  # :9092
make run-worker     # :9093

# 3. Verify metrics endpoints
curl -s localhost:9091/metrics | grep "^orion_http"
# orion_http_requests_total{...} 0
curl -s localhost:9092/metrics | grep "^orion_scheduler"
# orion_scheduler_cycle_duration_seconds_count 0
curl -s localhost:9093/metrics | grep "^orion_worker"
# orion_worker_active_jobs{worker_id="..."} 0

# 4. Submit 10 jobs, verify counters increment
for i in $(seq 1 10); do
  curl -s -X POST localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"obs-test","type":"inline","payload":{"handler_name":"noop"}}' > /dev/null
done
sleep 10
curl -s "localhost:9090/api/v1/query?query=orion_jobs_completed_total" | jq '.data.result[0].value[1]'
# "10"  ✅

# 5. Verify Jaeger has traces
echo "Open: http://localhost:16686 → service: orion-worker → find worker.execute_job spans"

# 6. Verify Grafana dashboard
echo "Open: http://localhost:3000/d/orion-main (admin/admin)"

# 7. Existing tests still pass
go test -race ./... -count=1
# All 171 tests pass (metrics are nil-safe — existing tests unaffected)
```

---

## File locations after Phase 6

```
orion/
├── internal/
│   ├── observability/
│   │   └── observability.go        ← UPDATED: 9 new metrics
│   ├── api/handler/
│   │   └── middleware.go           ← NEW: MetricsMiddleware + TracingMiddleware
│   ├── worker/
│   │   └── pool.go                 ← UPDATED: metrics field + 5 instrumentation points
│   ├── pipeline/
│   │   └── advancement.go          ← UPDATED: metrics field + 6 instrumentation points
│   ├── scheduler/
│   │   └── scheduler.go            ← UPDATED: metrics field + cycle span + latency
│   └── queue/redis/
│       └── redis_queue.go          ← UPDATED: metrics field + StartQueueDepthPoller
├── cmd/
│   ├── api/main.go                 ← UPDATED: registry + metrics + middleware
│   ├── scheduler/main.go           ← UPDATED: registry + metrics + poller
│   └── worker/main.go              ← UPDATED: registry + metrics + poller
└── deploy/
    ├── prometheus/
    │   └── prometheus.yml          ← UPDATED: 3 scrape targets
    └── grafana/dashboards/
        └── orion.json              ← NEW: 9-panel pre-built dashboard
```