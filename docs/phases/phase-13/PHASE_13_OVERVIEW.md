# Phase 13 — Production Hardening

> **Status:** Planned  
> **Predecessor:** Phase 12 (Documentation & Docs Integration)  
> **Goal:** Make Orion safe and auditable for shared or multi-tenant production deployments

---

## What Phase 13 is

Phases 1–12 built a fully functional, observable ML job orchestrator. Phase 13
makes it **production-ready for shared environments**. Every item addresses
a concrete gap that becomes a real problem the moment Orion is deployed outside
a single developer's laptop.

The four work items are independent. They share a config file and a final
verification step but can be merged and deployed one at a time.

---

## The four items

### 1. Authentication & Authorization

**Document:** [`AUTHENTICATION_AND_AUTHORIZATION.md`](./AUTHENTICATION_AND_AUTHORIZATION.md)

Orion's HTTP and gRPC surfaces are currently open. Any process that can reach
the API server can submit, cancel, or delete jobs. This adds a Bearer token
middleware to the HTTP chain and gRPC interceptors to both the unary and
streaming RPC servers. A single `ORION_API_KEY` environment variable enables
it; leaving it empty keeps the local dev experience unchanged.

**New files:**
- `internal/api/handler/auth.go`
- `internal/api/handler/auth_test.go`

**Modified files:**
- `internal/config/config.go`
- `cmd/api/main.go`
- `deploy/helm/values.yaml`
- `.env.example`

---

### 2. HTTP Request Body Size Limit

**Document:** [`HTTP_BODY_SIZE_LIMIT.md`](./HTTP_BODY_SIZE_LIMIT.md)

An unlimited body size means a single POST request with a multi-gigabyte
payload can exhaust the API server's memory. Two lines of middleware using
`http.MaxBytesReader` prevent this. The default limit is 10 MiB, configurable
via `ORION_MAX_REQUEST_BODY_BYTES`. The gRPC server gains a matching
`MaxRecvMsgSize` option.

**New files:** None.

**Modified files:**
- `internal/api/handler/middleware.go`
- `internal/config/config.go`
- `cmd/api/main.go`

---

### 3. Execution Log Shipping

**Document:** [`EXECUTION_LOG_SHIPPING.md`](./EXECUTION_LOG_SHIPPING.md)

`domain.JobExecution.LogsRef` is defined and stored in PostgreSQL but has
always been an empty string. This wires it up. Execution logs from inline
handlers and Kubernetes pod stdout/stderr are streamed to S3-compatible
object storage (AWS S3, MinIO, Cloudflare R2) and the URI is stored in
`logs_ref`. A `LogSink` interface with a `NilSink` default means no
configuration is required for development.

**New files:**
- `internal/worker/logs/sink.go`
- `internal/worker/logs/s3_sink.go`
- `internal/worker/logs/sink_test.go`
- `docs/adr/ADR-011-execution-log-storage.md`

**Modified files:**
- `internal/worker/pool.go`
- `internal/worker/inline.go`
- `internal/worker/k8s/executor.go`
- `internal/config/config.go`
- `cmd/worker/main.go`
- `go.mod`

---

### 4. Live Stack Smoke Test

**Document:** [`LIVE_STACK_SMOKE_TEST.md`](./LIVE_STACK_SMOKE_TEST.md)

A `//go:build e2e` test suite that runs against the real Docker Compose stack
with all three Orion binaries running as separate OS processes. It asserts the
full job lifecycle (submit → scheduled → running → completed), Prometheus
metrics increment, a Jaeger trace is present with a `worker.execute_job` span,
and the Grafana dashboard is provisioned. The `make smoke-test` target
handles infrastructure startup, service startup, test execution, and cleanup.

**New files:**
- `tests/e2e/smoke_test.go`
- `tests/e2e/helpers_test.go`

**Modified files:**
- `Makefile`
- `docs/LOCAL_EXECUTION_AND_END_PRODUCT.md`

---

## Recommended implementation order

```
Step 1 (together — same files):
  Authentication & Authorization  +  HTTP Body Size Limit
  Both touch cmd/api/main.go and config.go simultaneously.
  Estimated: 4–5 hours.

Step 2 (independent):
  Execution Log Shipping
  Touches worker internals only. No API or scheduler changes.
  Estimated: 1–2 days.

Step 3 (last — validation gate):
  Live Stack Smoke Test
  Proves steps 1 and 2 work together in a real deployment.
  Estimated: 2–3 hours.
```

---

## Dependency map

```
Authentication & Authorization
        │
        ├── config.go (adds APIKey field)
        └── cmd/api/main.go (wires middleware + interceptors)
                │
                ▼
HTTP Body Size Limit
        │
        ├── config.go (adds MaxRequestBodyBytes field)
        └── cmd/api/main.go (wires MaxBodySizeMiddleware)

Execution Log Shipping
        │
        ├── internal/worker/logs/ (new package, no deps on auth or body limit)
        ├── internal/worker/pool.go (accepts LogSink)
        ├── internal/worker/inline.go (accepts logWriter)
        ├── internal/worker/k8s/executor.go (streams pod logs)
        └── cmd/worker/main.go (constructs LogSink from config)

Live Stack Smoke Test
        │
        └── depends on all three above being deployed
            (or runs as a baseline before them)
```

---

## What Phase 13 does NOT include

These are deliberately deferred to avoid scope creep:

**Role-based access control (RBAC)** — Phase 13 uses a single shared API key.
Per-team or per-queue tokens, and fine-grained operation permissions, are a
separate project that requires a token registry and a claims model. The
`AuthMiddleware` is designed to accommodate this without changing handler code.

**Log presigned URL endpoint** — `GET /jobs/{id}/executions/{execId}/logs`
returning a time-limited presigned URL that the dashboard can use to fetch log
content directly from object storage. This is a three-line handler once log
shipping is wired.

**mTLS** — The right long-term control inside a service mesh (Istio, Linkerd).
Deferred until Orion is running inside a mesh where certificate management is
handled by the platform.

**Secrets rotation** — `ORION_API_KEY` is a static secret. Automatic rotation
(SOPS, Vault, AWS Secrets Manager) is an operational concern addressed at the
infrastructure layer, not in Orion itself.

---

## Completion checklist

```
Authentication & Authorization
  □ internal/api/handler/auth.go created
  □ HTTP middleware validates Bearer token
  □ gRPC UnaryInterceptor validates Bearer token
  □ gRPC StreamInterceptor validates Bearer token
  □ /healthz, /readyz, /metrics are exempt
  □ Empty ORION_API_KEY disables auth (local dev unchanged)
  □ auth_test.go: all pass/fail cases covered
  □ Helm secret includes ORION_API_KEY
  □ .env.example documents ORION_API_KEY

HTTP Body Size Limit
  □ MaxBodySizeMiddleware added to middleware.go
  □ Default limit: 10 MiB (ORION_MAX_REQUEST_BODY_BYTES)
  □ Middleware position: after AuthMiddleware, before MetricsMiddleware
  □ grpc.MaxRecvMsgSize(10 MiB) added to gRPC server
  □ Oversized request returns 400 (not 413; see middleware doc for why)
  □ Limit=0 disables restriction (dev default)
  □ Handler tests include oversized body test case

Execution Log Shipping
  □ internal/worker/logs/sink.go: LogSink interface + NilSink
  □ internal/worker/logs/s3_sink.go: S3Sink with streaming upload
  □ LogSink injected into Pool via NewPool
  □ executeJob calls Upload; passes URI to RecordExecution
  □ Inline executor writes to logWriter io.Pipe
  □ K8s executor streams pod logs to logWriter
  □ NilSink is default (no config needed for dev)
  □ config.go: LogStorage section
  □ cmd/worker/main.go constructs LogSink from config
  □ go.mod: aws-sdk-go-v2 added
  □ sink_test.go: NilSink + S3Sink unit tested with mock transport
  □ ADR-011 written and committed

Live Stack Smoke Test
  □ tests/e2e/smoke_test.go created with //go:build e2e
  □ TestSmoke_InfrastructureHealth: healthz, readyz, metrics, Prometheus
  □ TestSmoke_InlineJobLifecycle: submit → completed in < 30s
  □ TestSmoke_PipelineLifecycle: 2-node pipeline → completed in < 60s
  □ TestSmoke_PrometheusMetrics: orion_jobs_completed_total > 0
  □ TestSmoke_JaegerTraces: worker.execute_job span present
  □ TestSmoke_Authentication: skips when ORION_API_KEY unset
  □ TestSmoke_GrafanaDashboard: dashboard has ≥ 9 panels
  □ make smoke-test target added to Makefile
  □ LOCAL_EXECUTION_AND_END_PRODUCT.md updated
```
