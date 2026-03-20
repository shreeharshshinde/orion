# Step 08 — Observability (`internal/observability/observability.go`)

## What is this file?

This file sets up the **three pillars of observability** — the tools that let you understand what your system is doing at runtime:

1. **Metrics** (Prometheus) — numbers over time: how many jobs ran, how fast, how many failed
2. **Traces** (OpenTelemetry + Jaeger) — the journey of a single request across all services
3. **Logs** (slog) — timestamped text events with structured context

Without these, production debugging is: *"something is slow, I have no idea why."*
With these, it's: *"job abc123 took 4.2s in the K8s executor, 3.8s of that was pod scheduling."*

## Part 1: Metrics (Prometheus)

### What Prometheus does
Prometheus is a time-series database. Your application exposes a `/metrics` HTTP endpoint, and Prometheus scrapes it every 15 seconds, storing the numbers. Grafana then queries Prometheus to draw charts.

```
Your app → /metrics endpoint → Prometheus scrapes → stores in TSDB → Grafana queries → dashboard
```

### The Metrics struct
```go
type Metrics struct {
    JobsSubmitted   *prometheus.CounterVec    // total jobs received
    JobsCompleted   *prometheus.CounterVec    // total jobs finished successfully
    JobsFailed      *prometheus.CounterVec    // total jobs that failed
    JobsRetried     *prometheus.CounterVec    // total retry attempts
    JobsDead        *prometheus.CounterVec    // total jobs moved to DLQ
    JobDuration     *prometheus.HistogramVec  // how long jobs take (distribution)
    QueueDepth      *prometheus.GaugeVec      // current jobs waiting in each queue
    WorkerActiveJobs *prometheus.GaugeVec     // jobs running right now per worker
    SchedulerCycleLatency prometheus.Histogram // how long each scheduler loop takes
}
```

### Metric types explained

**Counter** — only goes up, never resets (except on restart):
```go
m.JobsCompleted.WithLabelValues("default", "inline").Inc()
// orion_jobs_completed_total{queue="default",type="inline"} 1
```
Good for: "how many total times did X happen?"

**Gauge** — can go up and down:
```go
m.QueueDepth.WithLabelValues("high").Set(float64(depth))
// orion_queue_depth{queue="high"} 42
```
Good for: "what is the current value of X?"

**Histogram** — records distribution of values in buckets:
```go
m.JobDuration.WithLabelValues("default", "k8s_job", "completed").Observe(duration.Seconds())
// orion_job_duration_seconds_bucket{le="60"} 847   ← 847 jobs took under 60s
// orion_job_duration_seconds_bucket{le="300"} 923  ← 923 jobs took under 300s
// orion_job_duration_seconds_sum 45821             ← total seconds across all jobs
// orion_job_duration_seconds_count 1024            ← total jobs measured
```
Good for: "what is the distribution of X? What's the p50/p95/p99?"

### Why labels matter
```go
// Bad — one counter for all jobs
m.JobsCompleted.Inc()

// Good — one counter PER queue AND type combination
m.JobsCompleted.WithLabelValues("high", "k8s_job").Inc()
m.JobsCompleted.WithLabelValues("default", "inline").Inc()
```

With labels, you can answer: *"how many k8s_job type jobs completed on the high-priority queue?"* Without labels, you only know the total.

### Histogram buckets tuned for ML
```go
Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600, 1800, 3600},
//                 100ms 500ms 1s  5s  10s 30s 1min 5min 10min 30min 1hr
```

ML workloads range from sub-second preprocessing to hour-long training runs. Standard Prometheus buckets (which top out at 10s) would be useless here.

## Part 2: Tracing (OpenTelemetry + Jaeger)

### What tracing does
A **trace** is the complete record of one request's journey through your system:

```
POST /jobs (12ms total)
├── idempotency check (2ms)      ← span
├── INSERT into PostgreSQL (8ms) ← span
└── return 201 Created (2ms)

Later, in the scheduler:
dispatch cycle (15ms total)
├── SELECT queued jobs (5ms)     ← span
├── CAS transition job (3ms)     ← span
└── XADD to Redis (7ms)          ← span

Later, in the worker:
job execution (4.2s total)
├── XREADGROUP (1ms)             ← span
├── mark_running in PG (3ms)     ← span
├── K8s executor (4.1s)          ← span
│   ├── create K8s Job (200ms)   ← span
│   └── wait for completion (3.9s) ← span
└── XACK + mark_completed (5ms)  ← span
```

All these spans share the same `trace_id`, so in Jaeger you can see the entire journey of job "abc123" in one waterfall view.

### Setup explained
```go
func SetupTracing(ctx, serviceName, serviceVersion, otlpEndpoint string, sampleRate float64)
```

1. **Resource**: tells Jaeger which service these spans come from
2. **Exporter**: sends spans to Jaeger via gRPC (OTLP protocol) — `otlpEndpoint` is `http://localhost:4317`
3. **TracerProvider**: batches spans and sends them every 5 seconds
4. **Sampler**: `TraceIDRatioBased(1.0)` = sample 100% of traces in dev. In production, use 0.1 (10%) to reduce overhead.
5. **Propagator**: W3C TraceContext headers — when the API server calls PostgreSQL, it passes the `traceparent` header so the DB trace becomes a child span of the HTTP trace

### Using the tracer in your code (preview of Phase 6)
```go
tracer := observability.Tracer("orion.scheduler")

ctx, span := tracer.Start(ctx, "dispatch_cycle")
defer span.End()

span.SetAttributes(attribute.Int("jobs.dispatched", count))
```

## Part 3: Structured Logging (slog)

### What slog does
`slog` is Go's standard library structured logger (added in Go 1.21). Unlike `fmt.Printf`, it produces machine-readable logs:

**fmt.Printf** (bad for production):
```
2024-01-15 10:23:11 job abc123 completed in 2341ms
```
You can't filter by job_id. You can't search for all events related to one job.

**slog JSON** (good for production):
```json
{"time":"2024-01-15T10:23:11Z","level":"INFO","service":"orion-worker","env":"production",
 "worker_id":"worker-prod-3","job_id":"550e8400","trace_id":"3d4e5f6a",
 "msg":"job completed successfully","duration_ms":2341}
```

You can now: filter `job_id = "550e8400"` to see everything that happened to one job. Correlate with Jaeger using `trace_id`. Alert on `level = "ERROR"`.

### Logger setup
```go
logger := observability.NewLogger("info", "orion-worker", "production")
// Returns a *slog.Logger pre-loaded with service and env fields

logger.Info("job completed", "job_id", job.ID, "duration_ms", 2341)
// Every log line automatically includes: service, env, plus whatever you add
```

### Log levels
- `DEBUG`: verbose, for development only. Don't enable in production (too noisy)
- `INFO`: normal operations. "Job submitted", "Worker started"
- `WARN`: something unexpected but recoverable. "Heartbeat failed, retrying"
- `ERROR`: something failed. "Failed to mark job running"

### MetricsServer
```go
func MetricsServer(port int, reg *prometheus.Registry) *http.Server
```

Starts an HTTP server on `:9091` with two routes:
- `/metrics` — Prometheus scrapes this
- `/healthz` — Kubernetes probes this to know the service is alive

## File location
```
orion/
└── internal/
    └── observability/
        └── observability.go   ← you are here
```

## Next step
With the observability tools ready, we build the **API handler** — the HTTP layer that receives job submissions from clients.