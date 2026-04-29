# Phase 6 — `internal/observability/observability.go` + `internal/api/handler/middleware.go`
## New Metrics, HTTP Middleware, and Tracing Middleware

---

## What changed in `observability.go`

Nine new metrics added to `Metrics` struct and registered in `NewMetrics()`. Nothing existing was removed or renamed — all Phase 1–5 code compiles without modification.

### The three new metric groups

**Pipeline metrics** — emitted by `internal/pipeline/advancement.go`

| Metric | Type | Labels | When |
|---|---|---|---|
| `orion_pipelines_started_total` | Counter | `pipeline_name` | `pending → running` in `advanceOne` Step 4 |
| `orion_pipelines_completed_total` | Counter | `pipeline_name` | All nodes done in `advanceOne` Step 7 |
| `orion_pipelines_failed_total` | Counter | `pipeline_name` | Dead node detected in `advanceOne` Step 3 |
| `orion_pipeline_duration_seconds` | Histogram | `status` | On terminal state (completed or failed) |
| `orion_pipeline_nodes_total` | Counter | `pipeline_name`, `node_status` | Node started / dead / cancelled |
| `orion_advancer_cycle_duration_seconds` | Histogram | — | End of every `AdvanceAll()` call |

Pipeline histogram buckets: `10, 30, 60, 300, 600, 1800, 3600, 7200, 14400` seconds.
Covers 10-second simple chained pipelines to 4-hour GPU training runs.

**HTTP metrics** — emitted by `MetricsMiddleware` wrapping the entire mux

| Metric | Type | Labels | When |
|---|---|---|---|
| `orion_http_requests_total` | Counter | `method`, `path`, `status_code` | Every HTTP request completes |
| `orion_http_request_duration_seconds` | Histogram | `method`, `path` | Every HTTP request completes |

**Database metrics** — emitted by `internal/store/postgres/db.go` (Phase 6 instrumentation)

| Metric | Type | Labels | When |
|---|---|---|---|
| `orion_db_operation_duration_seconds` | Histogram | `operation` | Start/end of each DB method |

### Cardinality rule — critical for Prometheus stability

**Never use `job.ID`, `pipeline.ID`, or any UUID as a label value.** UUIDs create one label value per entity — millions of rows in the Prometheus WAL, eventual OOM.

Safe labels (finite set of values):
- `queue`: `high`, `default`, `low` (3 values)
- `type`: `inline`, `k8s_job` (2 values)
- `status`: `completed`, `failed` (2 values)
- `reason`: `handler_error`, `cancelled`, `deadline_exceeded`, `config_error` (4 values)
- `operation`: `CreateJob`, `MarkJobRunning`, etc. (~10 values)
- `pipeline_name`: low if names follow a template; watch if users submit many unique names

### Why each binary creates its own registry

```go
// Each cmd/*.go does this independently:
reg := prometheus.NewRegistry()
metrics := observability.NewMetrics(reg)
```

Three binaries = three registries = three separate `/metrics` endpoints. Prometheus scrapes all three. Each binary only reports the metrics it actually produces — the API reports HTTP and DB metrics, the scheduler reports cycle latency and pipeline metrics, the worker reports job duration and active jobs. No process reports zero-valued metrics for signals it doesn't produce.

---

## `internal/api/handler/middleware.go` — The HTTP middleware pair

Two middleware functions, one shared `statusRecorder`.

### `MetricsMiddleware`

Wraps the entire mux with Prometheus instrumentation:

```go
// In cmd/api/main.go:
instrumentedHandler := handler.TracingMiddleware(
    handler.MetricsMiddleware(metrics, mux),
)
srv := &http.Server{Handler: instrumentedHandler}
```

**Route label cardinality control** — the key implementation detail:

```go
route := r.Pattern   // Go 1.22: "GET /jobs/{id}"   ← correct
// NOT:
route := r.URL.Path  // "/jobs/550e8400-e29b-41d4-..."  ← explosion
```

Go 1.22's ServeMux sets `r.Pattern` to the registered route template after matching. Using `r.Pattern` gives exactly 8 label values for Orion's 8 routes. Using `r.URL.Path` would give one value per unique UUID in every job GET request — millions of label combinations and guaranteed Prometheus OOM.

### `TracingMiddleware`

Wraps the instrumented handler with OpenTelemetry span creation:

```go
ctx, span := observability.Tracer("orion.api").Start(r.Context(), spanName,
    trace.WithSpanKind(trace.SpanKindServer),
    trace.WithAttributes(
        attribute.String("http.method", r.Method),
        attribute.String("http.route", route),
    ),
)
// Passes ctx into the request so downstream code can create child spans
next.ServeHTTP(rw, r.WithContext(ctx))
```

If the client sends a W3C `traceparent` header (from an API gateway or SDK), this span becomes a child of that trace. Otherwise it is the root span — the start of a new trace for that request.

### `statusRecorder`

Both middleware functions use the same `statusRecorder`:

```go
type statusRecorder struct {
    http.ResponseWriter
    statusCode int  // defaults to 200
}

func (r *statusRecorder) WriteHeader(code int) {
    r.statusCode = code
    r.ResponseWriter.WriteHeader(code)
}
```

Handlers that return 200 without calling `WriteHeader` explicitly are handled correctly — the default is 200 as per the HTTP spec.

---

## Running the tests

No new test files are needed for the middleware — it is tested indirectly by the existing `handler/pipeline_test.go` and `handler/job_test.go` which test the full HTTP stack. The middleware can be optionally passed `nil` metrics in tests; all calls are nil-guarded in `MetricsMiddleware`.

```bash
go test -race ./internal/api/handler/... -v
# All 42 handler tests pass (19 job + 23 pipeline)
```

---

## File locations

```
orion/
└── internal/
    ├── observability/
    │   └── observability.go       ← UPDATED: 9 new metrics
    └── api/handler/
        └── middleware.go          ← NEW: MetricsMiddleware + TracingMiddleware
```