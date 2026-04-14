# Orion — Complete Project Roadmap

> **Orion** is a production-grade distributed ML job orchestrator built in Go.
> It accepts job submissions via HTTP, stores them durably in PostgreSQL, dispatches them through Redis Streams, executes them on Kubernetes or inline, and self-heals from any failure.
> This roadmap covers every phase from skeleton to production-ready platform.

---

## Table of Contents

- [Project Vision](#project-vision)
- [Architecture Overview](#architecture-overview)
- [Technology Stack](#technology-stack)
- [Phase Summary Table](#phase-summary-table)
- [Phase 1 — Foundation Skeleton](#phase-1--foundation-skeleton)
- [Phase 2 — PostgreSQL Store Implementation](#phase-2--postgresql-store-implementation)
- [Phase 3 — Inline Executor](#phase-3--inline-executor)
- [Phase 4 — Kubernetes Executor](#phase-4--kubernetes-executor)
- [Phase 5 — Pipeline Orchestration (DAG)](#phase-5--pipeline-orchestration-dag)
- [Phase 6 — Observability Integration](#phase-6--observability-integration)
- [Phase 7 — gRPC Streaming API](#phase-7--grpc-streaming-api)
- [Phase 8 — Rate Limiting and Priority Queues](#phase-8--rate-limiting-and-priority-queues)
- [Phase 9 — Helm Chart and Kubernetes Deployment](#phase-9--helm-chart-and-kubernetes-deployment)
- [End State — What Orion Looks Like When Complete](#end-state--what-orion-looks-like-when-complete)
- [File Growth by Phase](#file-growth-by-phase)
- [Decision Log](#decision-log)

---

## Project Vision

Orion solves one problem precisely: **running ML workloads reliably at scale**.

A data scientist submits a training job. Orion guarantees:
- The job runs **exactly once** (no duplicates, even under concurrent submissions)
- The job **retries automatically** with exponential backoff on transient failure
- The job **self-heals** if the worker running it crashes mid-execution
- The job's **entire history** is preserved — every attempt, every error, every timestamp
- The job can be a **Go function** (for lightweight preprocessing) or a **Kubernetes pod** (for GPU-intensive training)
- Multiple jobs can be **chained as a DAG** — train depends on preprocess, evaluate depends on train

By the end of all phases, Orion is a system you could deploy at a real ML company to replace ad-hoc `kubectl apply` scripts and fragile cron jobs.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│  CLIENTS                                                            │
│  curl · Python SDK · Go SDK · CI/CD pipeline · Kubeflow            │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ HTTP  /  gRPC (Phase 7)
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│  API SERVER  (:8080)                                                │
│  POST /jobs  ·  GET /jobs/{id}  ·  GET /jobs/{id}/executions       │
│  POST /pipelines  ·  GET /pipelines/{id}  (Phase 5)                │
│  gRPC streaming  (Phase 7)                                          │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ INSERT / SELECT
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│  POSTGRESQL  (source of truth)                                      │
│  jobs · job_executions · workers · pipelines · pipeline_jobs        │
└──────────┬──────────────────────────────────────────────────────────┘
           │ SELECT queued ORDER BY priority (every 2s)
           │
┌──────────▼──────────────────────────────────────────────────────────┐
│  SCHEDULER  (leader-elected via PG advisory lock)                   │
│  Dispatch loop  ·  Retry promotion  ·  Orphan reclaim               │
│  DAG advancement  (Phase 5)                                         │
└──────────┬──────────────────────────────────────────────────────────┘
           │ XADD (Redis Streams)
           ▼
┌─────────────────────────────────────────────────────────────────────┐
│  REDIS STREAMS                                                      │
│  orion:queue:high  ·  orion:queue:default  ·  orion:queue:low       │
│  orion:queue:dead  ·  orion:queue:scheduled (sorted set)            │
└──────────┬──────────────────────────────────────────────────────────┘
           │ XREADGROUP (blocking)
           ▼
┌─────────────────────────────────────────────────────────────────────┐
│  WORKER POOL  (N goroutines, backpressure via buffered channel)     │
│  InlineExecutor   →  runs registered Go functions   (Phase 3)      │
│  KubernetesExecutor  →  launches K8s Jobs           (Phase 4)      │
└──────────┬──────────────────────────────────────────────────────────┘
           │ kubectl / K8s API
           ▼
┌─────────────────────────────────────────────────────────────────────┐
│  KUBERNETES  (ML workloads)                                         │
│  training pods  ·  preprocessing pods  ·  evaluation pods          │
└─────────────────────────────────────────────────────────────────────┘

OBSERVABILITY (all phases)
  Prometheus :9091  ·  Jaeger :16686  ·  Grafana :3000
```

---

## Technology Stack

| Layer | Technology | Why |
|---|---|---|
| Language | Go 1.22 | Native concurrency, single binary, fast compile, strong stdlib |
| Database | PostgreSQL 16 | CAS via `UPDATE WHERE status = expected`, advisory locks for leader election, JSONB for flexible payloads |
| Queue | Redis 7 Streams | At-least-once delivery via PEL + XACK, consumer groups for horizontal scaling, XAUTOCLAIM for crash recovery |
| Kubernetes client | `k8s.io/client-go` | Official Go SDK, used by Kubeflow — familiar territory for the target audience |
| Tracing | OpenTelemetry → Jaeger | Industry standard, vendor-neutral, OTLP gRPC export |
| Metrics | Prometheus + Grafana | Pull-based scraping, rich query language (PromQL), proven at scale |
| Logging | `log/slog` (stdlib) | JSON structured logging, zero dependencies, Go 1.21+ standard |
| gRPC | `google.golang.org/grpc` | High-throughput streaming for Phase 7 job event feeds |
| Retry math | `pkg/retry` (internal) | Full-jitter backoff — prevents thundering herd |
| Container | Docker + Compose | Local dev infrastructure in one command |
| Deployment | Helm | Kubernetes packaging standard |

---

## Phase Summary Table

| Phase | Name | Status | What it delivers | Completion signal |
|---|---|---|---|---|
| **1** | Foundation Skeleton | ✅ Complete | All interfaces, domain types, goroutine patterns, infrastructure | `make build` succeeds, all 3 services start |
| **2** | PostgreSQL Store | ✅ Complete | Real DB layer, full job lifecycle, all SQL written | `POST /jobs` returns 201, scheduler dispatches |
| **3** | Inline Executor | 🔲 Next | Go function jobs execute and reach `completed` | First `status: completed` in the DB |
| **4** | Kubernetes Executor | 🔲 Planned | Real K8s Jobs launch, GPU workloads run | K8s pod created, job reaches `completed` |
| **5** | Pipeline DAG | 🔲 Planned | Chained jobs — train depends on preprocess | `POST /pipelines` executes all nodes in order |
| **6** | Observability | 🔲 Planned | Metrics on every operation, traces through all hops | Grafana dashboard shows job throughput |
| **7** | gRPC Streaming | 🔲 Planned | Real-time job event streams, SDK-ready API | `WatchJob` streams live status transitions |
| **8** | Rate Limiting & Priority | 🔲 Planned | Per-queue concurrency limits, fair scheduling | High-priority jobs preempt low-priority backlog |
| **9** | Helm + Production | 🔲 Planned | Kubernetes deployment, TLS, autoscaling | `helm install orion` deploys all services |

---

## Phase 1 — Foundation Skeleton

**Status: ✅ Complete**

### What we built

The complete architectural skeleton. Every interface, every domain type, every goroutine pattern — written and documented. The one thing deliberately left as `nil` is the PostgreSQL implementation (filled in Phase 2).

### Files delivered

```
go.mod                                    Module declaration + all dependencies
internal/domain/job.go                    Job struct, 8-state machine, ValidTransitions
internal/domain/worker.go                 Worker struct, heartbeat helpers
internal/domain/pipeline.go              Pipeline, DAGSpec, ReadyNodes()
internal/config/config.go                All ORION_* env vars with defaults
internal/store/store.go                   JobStore + ExecutionStore + WorkerStore interfaces
internal/queue/queue.go                   Queue interface, AckFunc, queue name constants
internal/queue/redis/redis_queue.go      Redis Streams implementation (XADD, XREADGROUP, XAUTOCLAIM)
internal/observability/observability.go  Prometheus metrics, OTel tracing, slog logger
internal/api/handler/job.go              HTTP handlers: POST/GET /jobs
internal/scheduler/scheduler.go          Leader election + 3 dispatch loops
internal/worker/pool.go                   Goroutine pool with backpressure, Executor interface, graceful drain
internal/store/migrations/001_*.sql      All 5 tables, 4 partial indexes, updated_at trigger
pkg/retry/retry.go                        FullJitterBackoff (thundering herd prevention)
cmd/api/main.go                           API server binary entry point
cmd/scheduler/main.go                     Scheduler binary entry point
cmd/worker/main.go                        Worker binary entry point
docker-compose.yml                        PostgreSQL + Redis + Jaeger + Prometheus + Grafana
Makefile                                  build, test, migrate, lint, run shortcuts
docs/adr/ADR-001-queue-design.md         Why Redis Streams not Redis Lists
docs/adr/ADR-002-leader-election.md      Why PostgreSQL advisory locks not ZooKeeper
```

### Key design decisions locked in Phase 1

| Decision | Choice | Rationale |
|---|---|---|
| Queue technology | Redis Streams | At-least-once delivery via PEL. Redis Lists lose messages on crash. |
| Leader election | PostgreSQL advisory locks | Zero extra infrastructure. Auto-release on crash. Session-scoped. |
| State transitions | CAS `UPDATE WHERE status = expected` | Atomic. No two schedulers can claim the same job simultaneously. |
| Concurrency model | Buffered channel as backpressure | Jobs stay in Redis until a worker slot is free. No in-memory accumulation. |
| Retry strategy | Full-jitter exponential backoff | Scatters retries across time. Prevents thundering herd. |
| Interface separation | `store.Store` / `queue.Queue` | Enables testing without real infrastructure. Enables future backend swaps. |

### Completion criteria

```bash
make infra-up        # all 5 containers start healthy
make migrate-up      # 5 tables + indexes created
make build           # all 3 binaries compile
make run-api         # INFO "API server listening" port=8080
make run-scheduler   # INFO "acquired scheduler leader lock"
make run-worker      # INFO "starting worker pool"
curl localhost:8080/healthz  # {"status":"ok"}
```

---

## Phase 2 — PostgreSQL Store Implementation

**Status: ✅ Complete**

### What we built

The only file that needed to exist for Phase 1's nil placeholders to become real: `internal/store/postgres/db.go` (725 lines). Plus wired all three `cmd/*/main.go` files, added `GetExecutions` handler, and wrote integration tests.

### Files delivered

```
internal/store/postgres/db.go                   The complete PostgreSQL store (all 12 methods)
internal/store/store.go                          Updated: IsNotFound/IsStateConflict/IsDuplicate helpers
internal/api/handler/job.go                      Updated: added GetExecutions handler
internal/store/postgres/postgres_integration_test.go  25 integration tests (requires DB)
internal/store/postgres/store_unit_test.go           12 unit tests (no DB needed)
cmd/api/main.go                                  Updated: fully wired with postgres.New(db)
cmd/scheduler/main.go                            Updated: fully wired with store + queue
cmd/worker/main.go                               Updated: fully wired with store + queue
docs/phase2/README-postgres-db.md               postgres/db.go deep-dive
docs/phase2/README-entrypoints.md               cmd/ wiring explanation
docs/phase2/README-handler.md                   handler updates + all 4 routes documented
docs/phase2/README-tests.md                     How to run tests, what each test verifies
```

### Key patterns implemented

**CAS (Compare-And-Swap):**
```sql
UPDATE jobs SET status = $new
WHERE id = $1 AND status = $expected   -- atomic guard
RETURNING id                            -- 0 rows = ErrStateConflict
```

**Idempotency with concurrent race handling:**
```
Request-A & Request-B arrive simultaneously with same idempotency_key
→ Both pass SELECT check (not found yet)
→ One INSERT wins, the other fails with 23505 unique_violation
→ The loser catches 23505, fetches the winner's row, returns it
→ Both callers receive the same job_id
```

**Orphan reclaim:**
```sql
WITH orphaned AS (
    SELECT j.id FROM jobs j WHERE j.status = 'running'
    AND NOT EXISTS (
        SELECT 1 FROM workers w WHERE w.id = j.worker_id
        AND w.last_heartbeat > NOW() - $1::interval
    )
)
UPDATE jobs SET status = 'queued', worker_id = NULL FROM orphaned ...
```

### New API route

```
GET /jobs/{id}/executions
→ Returns the append-only audit log of every execution attempt
→ 3 failures + 1 success = 4 rows with attempt numbers 1, 2, 3, 4
```

### Completion criteria

```bash
# Full end-to-end lifecycle works:
curl -X POST localhost:8080/jobs -d '{...}'
# → 201 {"id":"...", "status":"queued"}

# 2s later (scheduler dispatch):
# → status becomes "scheduled"

# Worker picks up, fails with "no executor for type inline":
# → status becomes "failed", attempt=1

# Retry promotion kicks in after backoff:
# → failed → retrying → queued → scheduled → failed(2) → ...
# → after max_retries: status="dead"

# Idempotency:
curl -X POST localhost:8080/jobs -d '{"idempotency_key":"same-key",...}'
curl -X POST localhost:8080/jobs -d '{"idempotency_key":"same-key",...}'
# → Both return same job_id, HTTP 200 on second call

# Integration tests:
go test -tags=integration ./internal/store/postgres/... -v
# → All 25 tests pass
```

---

## Phase 3 — Inline Executor

**Status: 🔲 Next**

### What this phase does

Adds the first real executor — the ability to run Go functions as jobs. After Phase 3, the entire happy path works: `status: queued → scheduled → running → completed` for the first time.

### Why this matters

Phase 2 gets jobs to workers, but workers fail immediately with `"no executor for job type inline"`. Phase 3 makes those jobs actually execute. It is the difference between a system that runs but produces only failures and one that successfully completes work.

### Files to build

```
internal/worker/inline.go              InlineExecutor implementing worker.Executor interface
internal/worker/inline_test.go         Unit tests for handler registry and execution
cmd/worker/main.go                     Updated: register handlers with registry
docs/phase3/README-inline-executor.md  How to register and call inline handlers
```

### Core design

```go
// HandlerFunc is the signature every inline handler must implement.
type HandlerFunc func(ctx context.Context, job *domain.Job) error

// Registry maps handler names to their functions.
// Handlers are registered at startup, before pool.Start().
type Registry struct {
    handlers map[string]HandlerFunc
    mu       sync.RWMutex
}

func (r *Registry) Register(name string, fn HandlerFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.handlers[name] = fn
}

func (r *Registry) Get(name string) (HandlerFunc, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    fn, ok := r.handlers[name]
    return fn, ok
}

// InlineExecutor implements worker.Executor.
// It resolves the handler name from job.Payload.HandlerName and calls it.
type InlineExecutor struct {
    registry *Registry
    logger   *slog.Logger
}

func (e *InlineExecutor) CanExecute(jobType domain.JobType) bool {
    return jobType == domain.JobTypeInline
}

func (e *InlineExecutor) Execute(ctx context.Context, job *domain.Job) error {
    fn, ok := e.registry.Get(job.Payload.HandlerName)
    if !ok {
        return fmt.Errorf("no handler registered for: %s", job.Payload.HandlerName)
    }
    return fn(ctx, job)
}
```

### How jobs are registered

```go
// In cmd/worker/main.go (Phase 3 update):
registry := inline.NewRegistry()

// Built-in handlers
registry.Register("noop", func(ctx context.Context, job *domain.Job) error {
    return nil // does nothing — used for smoke testing
})

// ML handlers (examples)
registry.Register("preprocess_dataset", handlers.PreprocessDataset)
registry.Register("train_model",        handlers.TrainModel)
registry.Register("evaluate_model",     handlers.EvaluateModel)
registry.Register("export_model",       handlers.ExportModel)

executors := []worker.Executor{
    inline.NewExecutor(registry),
    // k8s.NewExecutor(...) added in Phase 4
}

pool := worker.NewPool(cfg, queue, store, executors, logger)
```

### Completion criteria

```bash
# Submit a noop job
curl -X POST localhost:8080/jobs -d '{
  "name": "smoke-test",
  "type": "inline",
  "payload": {"handler_name": "noop"}
}'

# Watch it complete
watch -n1 'curl -s localhost:8080/jobs/$JOB_ID | jq .status'
# queued → scheduled → running → completed  ← FIRST EVER COMPLETION

# Verify in DB
psql -c "SELECT status, attempt, completed_at FROM jobs WHERE name='smoke-test';"
# status=completed  attempt=0  completed_at=<timestamp>

# Check execution audit log
curl localhost:8080/jobs/$JOB_ID/executions
# [{"attempt":1, "status":"completed", "started_at":"...", "finished_at":"..."}]
```

---

## Phase 4 — Kubernetes Executor

**Status: 🔲 Planned**

### What this phase does

Adds `KubernetesExecutor` — the ability to launch real Kubernetes Jobs for GPU-intensive ML workloads that need more resources than a Go function can provide.

### Why this matters

Real ML training (fine-tuning LLMs, training vision models) requires GPUs, large RAM, and specific container images like `pytorch/pytorch:2.0`. These cannot run as inline Go functions. The Kubernetes executor makes Orion a real ML platform, not just a Go function runner.

### Files to build

```
internal/worker/k8s/executor.go         KubernetesExecutor implementing worker.Executor
internal/worker/k8s/executor_test.go    Tests with fake K8s client
internal/worker/k8s/spec.go             KubernetesSpec → batchv1.Job translation
cmd/worker/main.go                       Updated: wire k8sClient + KubernetesExecutor
deploy/k8s/rbac.yaml                    ServiceAccount + ClusterRole for K8s Job creation
docs/phase4/README-k8s-executor.md      K8s executor setup and GPU configuration
```

### Core design

```go
type KubernetesExecutor struct {
    client    kubernetes.Interface // k8s.io/client-go — fake-able in tests
    namespace string
    logger    *slog.Logger
}

func (e *KubernetesExecutor) CanExecute(jobType domain.JobType) bool {
    return jobType == domain.JobTypeKubernetes
}

func (e *KubernetesExecutor) Execute(ctx context.Context, job *domain.Job) error {
    spec := job.Payload.KubernetesSpec
    if spec == nil {
        return fmt.Errorf("k8s_job requires kubernetes_spec in payload")
    }

    // 1. Translate domain.KubernetesSpec → batchv1.Job
    k8sJob := buildK8sJob(job, spec)

    // 2. Create the K8s Job via API
    _, err := e.client.BatchV1().Jobs(e.namespace).Create(ctx, k8sJob, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("creating k8s job: %w", err)
    }

    // 3. Watch the job until complete, failed, or ctx deadline
    return e.waitForCompletion(ctx, job.ID.String(), e.namespace)
}
```

### GPU job example

```bash
curl -X POST localhost:8080/jobs -d '{
  "name": "train-resnet50-v2",
  "type": "k8s_job",
  "queue_name": "high",
  "priority": 9,
  "payload": {
    "kubernetes_spec": {
      "image": "pytorch/pytorch:2.0-cuda11.7",
      "command": ["python", "train.py"],
      "args": ["--model=resnet50", "--epochs=50", "--dataset=imagenet"],
      "namespace": "ml-training",
      "resources": {
        "cpu": "4",
        "memory": "16Gi",
        "gpu": 2
      },
      "env_vars": {
        "WANDB_API_KEY": "...",
        "S3_BUCKET": "my-training-bucket"
      },
      "ttl_seconds": 3600
    }
  },
  "max_retries": 2,
  "deadline": "2024-01-16T00:00:00Z"
}'
```

### Completion criteria

```bash
# K8s Job appears in the cluster:
kubectl get jobs -n ml-training
# NAME                            COMPLETIONS   DURATION
# orion-job-550e8400-...          0/1           10s

# Pod runs the training container:
kubectl logs -n ml-training -l orion-job-id=550e8400-...

# Job reaches completed in Orion:
curl localhost:8080/jobs/$JOB_ID | jq .status
# "completed"
```

---

## Phase 5 — Pipeline Orchestration (DAG)

**Status: 🔲 Planned**

### What this phase does

Enables chaining jobs into pipelines — directed acyclic graphs where each node only starts when all its dependencies complete.

### Real ML pipeline example

```
preprocess_data (inline)
    ↓
    ├── train_vision_model (k8s_job, GPU)
    └── train_nlp_model   (k8s_job, GPU)
              ↓                 ↓
        evaluate_vision    evaluate_nlp
              └──────────────────┘
                        ↓
                    merge_results (inline)
                        ↓
                   deploy_to_staging (k8s_job)
```

### Files to build

```
internal/pipeline/scheduler.go         DAG advancement logic: ReadyNodes() → enqueue
internal/pipeline/scheduler_test.go    Tests for dependency resolution
internal/api/handler/pipeline.go       POST /pipelines, GET /pipelines/{id}
internal/store/store.go                Updated: PipelineStore interface methods
internal/store/postgres/db.go          Updated: pipeline SQL (CreatePipeline, AdvancePipeline)
internal/store/migrations/002_pipeline_indexes.up.sql  New indexes for pipeline queries
cmd/api/main.go                         Updated: pipeline routes
cmd/scheduler/main.go                  Updated: pipeline advancement loop
docs/phase5/README-pipelines.md        DAG concepts, API reference, execution model
```

### API

```bash
# Create a pipeline
curl -X POST localhost:8080/pipelines -d '{
  "name": "full-training-pipeline",
  "dag_spec": {
    "nodes": [
      {"id": "preprocess", "job_template": {"type": "inline", "payload": {"handler_name": "preprocess_dataset"}}},
      {"id": "train",      "job_template": {"type": "k8s_job",  "payload": {"kubernetes_spec": {...}}}},
      {"id": "evaluate",   "job_template": {"type": "k8s_job",  "payload": {"kubernetes_spec": {...}}}},
      {"id": "deploy",     "job_template": {"type": "inline",   "payload": {"handler_name": "deploy_model"}}}
    ],
    "edges": [
      {"source": "preprocess", "target": "train"},
      {"source": "train",      "target": "evaluate"},
      {"source": "evaluate",   "target": "deploy"}
    ]
  }
}'

# Watch pipeline status
curl localhost:8080/pipelines/$PIPELINE_ID | jq .status
# "running"

# Get all jobs in the pipeline
curl localhost:8080/pipelines/$PIPELINE_ID/jobs | jq '.[].status'
# "completed" (preprocess)
# "running"   (train)
# "pending"   (evaluate — waiting for train)
# "pending"   (deploy — waiting for evaluate)
```

### Pipeline state machine

```
pending → running → completed
                 ↘ failed (any node fails and exhausts retries)
                 ↘ cancelled
```

A failed node triggers automatic cancellation of all downstream nodes.

### Completion criteria

```bash
# Full pipeline executes all nodes in dependency order
# Each node only starts after its parent completes
# A failed node cancels downstream nodes
# Pipeline status reflects the aggregate state
```

---

## Phase 6 — Observability Integration

**Status: 🔲 Planned**

### What this phase does

Instruments every critical operation with Prometheus metrics and OpenTelemetry spans. The `Metrics` struct and `SetupTracing` function already exist from Phase 1 — Phase 6 actually *calls* them on every operation.

### What changes

Phase 1 defined the metrics and tracer. Phase 6 weaves them into actual code:

```go
// Before Phase 6 (scheduler dispatch loop):
for _, job := range jobs {
    if err := s.store.TransitionJobState(...); err != nil { continue }
    if err := s.queue.Enqueue(...); err != nil { continue }
    dispatched++
}

// After Phase 6:
start := time.Now()
ctx, span := observability.Tracer("orion.scheduler").Start(ctx, "dispatch_cycle")
defer span.End()

for _, job := range jobs {
    jobCtx, jobSpan := observability.Tracer("orion.scheduler").Start(ctx, "dispatch_job",
        trace.WithAttributes(attribute.String("job.id", job.ID.String())),
    )
    if err := s.store.TransitionJobState(jobCtx, ...); err != nil {
        jobSpan.SetStatus(codes.Error, err.Error())
        jobSpan.End()
        continue
    }
    s.metrics.JobsSubmitted.WithLabelValues(job.QueueName, string(job.Type)).Inc()
    s.queue.Enqueue(jobCtx, job)
    jobSpan.End()
    dispatched++
}

s.metrics.SchedulerCycleLatency.Observe(time.Since(start).Seconds())
span.SetAttributes(attribute.Int("jobs.dispatched", dispatched))
```

### Files to update

```
internal/scheduler/scheduler.go        Add spans + cycle latency metrics
internal/worker/pool.go                Add job duration histogram, active count gauge
internal/api/handler/job.go            Add HTTP span, request counter, latency histogram
internal/queue/redis/redis_queue.go    Add queue depth gauge, enqueue/dequeue counters
internal/store/postgres/db.go          Add DB operation duration spans
deploy/grafana/dashboards/orion.json   Pre-built Grafana dashboard definition
docs/phase6/README-observability.md    Metric catalog, PromQL query examples, Jaeger guide
```

### Grafana dashboard panels

```
┌─────────────────────┬─────────────────────┬─────────────────────┐
│  Job Throughput      │  Error Rate          │  Queue Depth        │
│  (jobs/sec by type) │  (% failed by queue) │  (per queue, gauge) │
├─────────────────────┼─────────────────────┼─────────────────────┤
│  Execution Duration  │  Worker Utilization  │  Retry Rate         │
│  (p50/p95/p99 hist) │  (active/total slots)│  (retries/sec)      │
├─────────────────────┼─────────────────────┼─────────────────────┤
│  Scheduler Latency   │  Dead Job Rate       │  P99 Latency        │
│  (dispatch cycle ms)│  (DLQ accumulation) │  (end-to-end)       │
└─────────────────────┴─────────────────────┴─────────────────────┘
```

### Completion criteria

```bash
# Submit 100 jobs and watch in Grafana:
for i in $(seq 1 100); do curl -X POST localhost:8080/jobs -d '{...}'; done

# Prometheus queries work:
curl "localhost:9090/api/v1/query?query=rate(orion_jobs_completed_total[1m])"
curl "localhost:9090/api/v1/query?query=orion_queue_depth{queue=\"default\"}"

# Jaeger shows full traces:
# Open http://localhost:16686, select "orion-scheduler"
# See: dispatch_cycle → dispatch_job → postgres.TransitionJobState → redis.XADD
```

---

## Phase 7 — gRPC Streaming API

**Status: 🔲 Planned**

### What this phase does

Adds a gRPC API alongside the existing HTTP API. The primary new capability: **streaming job events** — clients receive real-time updates as job status changes, without polling.

### Why gRPC for this?

Polling `GET /jobs/{id}` every second is wasteful for long-running jobs (training can take hours). With gRPC streaming, a client opens one connection and receives a message every time the job's status changes:

```
queued → scheduled (message)
scheduled → running (message)
running → completed (message)
```

SDK clients (Python, Go) become much simpler: no polling loops, no latency between status change and client notification.

### Files to build

```
proto/orion/v1/jobs.proto               Service definition + message types
proto/orion/v1/jobs.pb.go               Generated by protoc (do not edit)
proto/orion/v1/jobs_grpc.pb.go          Generated gRPC service stubs
internal/api/grpc/server.go             gRPC service implementation
internal/api/grpc/server_test.go        Tests with in-memory gRPC connection
cmd/api/main.go                          Updated: start gRPC server on :9090 alongside HTTP :8080
docs/phase7/README-grpc.md              Proto definition guide, streaming usage examples
```

### Proto definition

```protobuf
syntax = "proto3";
package orion.v1;

service JobService {
  // Unary: same as POST /jobs
  rpc SubmitJob(SubmitJobRequest) returns (Job);

  // Unary: same as GET /jobs/{id}
  rpc GetJob(GetJobRequest) returns (Job);

  // Server-streaming: receive job status events as they happen
  // One message per status transition, stream closes when job reaches terminal state
  rpc WatchJob(WatchJobRequest) returns (stream JobEvent);

  // Server-streaming: subscribe to all events for a queue
  // Useful for real-time dashboards
  rpc WatchQueue(WatchQueueRequest) returns (stream JobEvent);
}

message JobEvent {
  string job_id = 1;
  string previous_status = 2;
  string new_status = 3;
  string worker_id = 4;
  string error_message = 5;
  google.protobuf.Timestamp timestamp = 6;
}
```

### Streaming implementation strategy

```go
// gRPC server implementation
func (s *Server) WatchJob(req *pb.WatchJobRequest, stream pb.JobService_WatchJobServer) error {
    id, _ := uuid.Parse(req.JobId)

    // Initial state
    job, err := s.store.GetJob(stream.Context(), id)
    if err != nil { return status.Error(codes.NotFound, err.Error()) }

    // Send current state immediately
    _ = stream.Send(jobToEvent(job, ""))

    // Subscribe to PostgreSQL LISTEN/NOTIFY or poll with backoff
    // until job reaches terminal state
    ticker := time.NewTicker(500 * time.Millisecond) // Phase 7 uses polling; Phase 7+ can use PG NOTIFY
    defer ticker.Stop()

    lastStatus := job.Status
    for {
        select {
        case <-stream.Context().Done():
            return nil
        case <-ticker.C:
            updated, _ := s.store.GetJob(stream.Context(), id)
            if updated.Status != lastStatus {
                _ = stream.Send(jobToEvent(updated, string(lastStatus)))
                lastStatus = updated.Status
                if updated.IsTerminal() { return nil }
            }
        }
    }
}
```

### Completion criteria

```bash
# Using grpcurl:
grpcurl -plaintext localhost:9090 list
# orion.v1.JobService

# Submit via gRPC
grpcurl -plaintext -d '{"name":"grpc-test","type":"inline","payload":{"handler_name":"noop"}}' \
    localhost:9090 orion.v1.JobService/SubmitJob

# Watch live status changes
grpcurl -plaintext -d '{"job_id":"550e8400-..."}' \
    localhost:9090 orion.v1.JobService/WatchJob
# Streams events as job moves through states
```

---

## Phase 8 — Rate Limiting and Priority Queues

**Status: 🔲 Planned**

### What this phase does

Adds per-queue concurrency limits and fair scheduling so high-volume users can't starve low-priority jobs, and so a single runaway queue can't consume all worker capacity.

### The problem it solves

Without rate limiting:
```
1000 low-priority batch jobs in queue
→ all 10 worker slots consumed by batch jobs
→ 1 high-priority interactive job waits 100 job durations
→ data scientist frustrated, SLA broken
```

With rate limiting:
```
queue_limits:
  high:    80% of capacity (8 of 10 slots)
  default: 60% of capacity (6 of 10 slots)
  low:     20% of capacity (2 of 10 slots)

→ Even under batch load, 8 slots always available for urgent jobs
```

### Files to build

```
internal/scheduler/ratelimiter.go       Token bucket per queue, slot reservation
internal/scheduler/fairqueue.go         Fair scheduling across queues with weights
internal/config/config.go               Updated: per-queue limit configuration
internal/store/migrations/003_queue_limits.up.sql  queue_config table
docs/phase8/README-rate-limiting.md     Queue configuration guide
```

### Configuration

```bash
# Environment variables for Phase 8
ORION_QUEUE_HIGH_CONCURRENCY=8       # max 8 jobs from high queue simultaneously
ORION_QUEUE_DEFAULT_CONCURRENCY=6    # max 6 from default queue
ORION_QUEUE_LOW_CONCURRENCY=2        # max 2 from low queue
ORION_QUEUE_HIGH_RATE_PER_SEC=100    # max 100 enqueues per second to high queue
```

### Completion criteria

```bash
# Flood the low-priority queue
for i in $(seq 1 500); do
    curl -X POST localhost:8080/jobs -d '{"queue_name":"low","priority":1,...}'
done

# Submit a high-priority job
curl -X POST localhost:8080/jobs -d '{"queue_name":"high","priority":10,...}'

# The high-priority job should complete within seconds
# Despite 500 low-priority jobs queued ahead of it in submission order
```

---

## Phase 9 — Helm Chart and Kubernetes Deployment

**Status: 🔲 Planned**

### What this phase does

Packages Orion for production Kubernetes deployment. A single `helm install` deploys all three services (API, Scheduler, Worker) with proper resource limits, autoscaling, RBAC, TLS, and health probes.

### Files to build

```
deploy/helm/Chart.yaml                          Chart metadata
deploy/helm/values.yaml                         Default configuration (overridable)
deploy/helm/templates/api-deployment.yaml       API server Deployment + Service
deploy/helm/templates/scheduler-deployment.yaml Scheduler Deployment (single replica + leader election)
deploy/helm/templates/worker-deployment.yaml    Worker Deployment (HPA-autoscalable)
deploy/helm/templates/hpa.yaml                  HorizontalPodAutoscaler for workers
deploy/helm/templates/rbac.yaml                 ServiceAccount + ClusterRole for K8s Job creation
deploy/helm/templates/secret.yaml               DB DSN + Redis password (sealed secrets)
deploy/helm/templates/configmap.yaml            Non-sensitive configuration
deploy/docker/Dockerfile.api                    Multi-stage build for API server
deploy/docker/Dockerfile.scheduler              Multi-stage build for scheduler
deploy/docker/Dockerfile.worker                 Multi-stage build for worker
docs/phase9/README-deployment.md                Production deployment guide
docs/phase9/README-scaling.md                   Scaling the worker fleet, tuning parameters
```

### Helm values overview

```yaml
# values.yaml
api:
  replicaCount: 3                 # 3 API replicas behind a LoadBalancer
  resources:
    requests: { cpu: "100m", memory: "128Mi" }
    limits:   { cpu: "500m", memory: "512Mi" }
  service:
    type: LoadBalancer
    port: 8080

scheduler:
  replicaCount: 3                 # 3 pods, only 1 active (leader election)
  resources:
    requests: { cpu: "50m",  memory: "64Mi" }
    limits:   { cpu: "200m", memory: "256Mi" }

worker:
  replicaCount: 5                 # initial count
  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 50
    targetCPUUtilizationPercentage: 70   # scale out when busy
  concurrency: 10                 # 10 goroutines per worker pod
  resources:
    requests: { cpu: "500m", memory: "1Gi" }
    limits:   { cpu: "2",    memory: "4Gi" }

postgresql:
  host: "postgres.prod.svc.cluster.local"
  database: "orion"
  existingSecret: "orion-db-credentials"

redis:
  host: "redis.prod.svc.cluster.local"
  existingSecret: "orion-redis-credentials"
```

### Docker build (multi-stage for minimal image size)

```dockerfile
# Dockerfile.api
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o orion-api ./cmd/api

FROM alpine:3.19 AS runtime
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /app/orion-api /usr/local/bin/orion-api
EXPOSE 8080
ENTRYPOINT ["orion-api"]
```

Resulting image size: ~12MB (vs ~800MB with a full Go toolchain image).

### HPA scaling strategy

```
Worker pod CPU at 70% → scale out
  → add 1 worker pod every 60 seconds until CPU drops below 70%
  → max 50 pods (500 concurrent jobs total)

Worker pod CPU at 30% for 5 minutes → scale in
  → remove 1 worker pod (graceful drain — existing jobs finish)
  → min 2 pods always (avoid cold start delay)
```

### Completion criteria

```bash
# Install on a Kubernetes cluster (e.g., local kind cluster)
helm install orion ./deploy/helm \
  --set postgresql.host=localhost \
  --set redis.host=localhost

# Verify all pods running
kubectl get pods -n orion
# orion-api-...           3/3  Running
# orion-scheduler-...     3/3  Running  (1 leader, 2 standby)
# orion-worker-...        5/5  Running

# Submit a job through the Kubernetes LoadBalancer
kubectl get svc orion-api -n orion
# EXTERNAL-IP: <ip>

curl -X POST http://<ip>:8080/jobs -d '{...}'
# → 201 Created

# Watch worker pods autoscale under load
kubectl get hpa -n orion -w
```

---

## End State — What Orion Looks Like When Complete

When all 9 phases are done, Orion is a production ML orchestration platform with these capabilities:

### Job submission (Phase 1–2)
```bash
# Simple inline function
curl -X POST /jobs -d '{"type":"inline","payload":{"handler_name":"preprocess"}}'

# GPU training on Kubernetes
curl -X POST /jobs -d '{
  "type": "k8s_job",
  "payload": {"kubernetes_spec": {"image": "pytorch/pytorch:2.0", "gpu": 2}}
}'

# Safe retry — idempotent
curl -X POST /jobs -d '{"idempotency_key": "run-001", ...}'
# Returns same job_id even if called 100 times
```

### Pipeline orchestration (Phase 5)
```bash
# Multi-stage ML pipeline
curl -X POST /pipelines -d '{
  "nodes": [preprocess, train, evaluate, deploy],
  "edges": [preprocess→train, train→evaluate, evaluate→deploy]
}'
# Executes in dependency order, cancels downstream on failure
```

### Real-time monitoring (Phase 6–7)
```bash
# gRPC streaming — real-time status updates
grpcurl ... WatchJob {job_id} | jq .new_status
# queued → scheduled → running → completed

# Grafana dashboard — live job throughput
open http://grafana:3000/d/orion
```

### Self-healing (Phase 1–4)
```
Worker crashes mid-job:
→ Heartbeat stops (15s intervals)
→ After 90s: orphan sweep reclaims job
→ Job requeued for another worker
→ New worker picks it up and completes it
→ Total extra latency: 90s + backoff
```

### Kubernetes-native deployment (Phase 9)
```bash
helm install orion ./deploy/helm --namespace ml-platform
# 3 API pods, 1 active scheduler, 5-50 autoscaled workers
# All with TLS, RBAC, resource limits, PodDisruptionBudgets
```

---

## File Growth by Phase

```
Phase 1:  19 files  (skeleton — all interfaces and goroutine patterns)
Phase 2:  27 files  (+8: postgres store, tests, updated handler, wired entrypoints)
Phase 3:  32 files  (+5: inline executor, handler registry, unit tests)
Phase 4:  38 files  (+6: k8s executor, spec translation, RBAC config)
Phase 5:  47 files  (+9: pipeline scheduler, API routes, migrations, tests)
Phase 6:  53 files  (+6: instrumentation in all packages, Grafana dashboard)
Phase 7:  60 files  (+7: proto, generated code, gRPC server, tests)
Phase 8:  66 files  (+6: rate limiter, fair queue, config, migration)
Phase 9:  82 files  (+16: Helm chart templates, Dockerfiles, deploy docs)
```

---

## Decision Log

Key architectural decisions made early that constrain all later phases:

| # | Decision | Rationale | Impact |
|---|---|---|---|
| 1 | **Redis Streams** over Redis Lists | Lists lose messages on crash; Streams have PEL + XACK | At-least-once delivery guaranteed throughout |
| 2 | **PostgreSQL advisory locks** for leader election | Zero infra overhead; auto-release on crash | No ZooKeeper/etcd needed |
| 3 | **CAS via `UPDATE WHERE status = expected`** | Atomic; no separate lock needed | Two schedulers can never both claim the same job |
| 4 | **Interfaces for store + queue** | Enables test fakes; enables backend swaps | Every phase tested without real infrastructure |
| 5 | **Buffered channel as backpressure** | Jobs stay in Redis until worker slot available | No in-memory job accumulation; crash-safe |
| 6 | **Full-jitter backoff** | Scatters retries across time | Thundering herd impossible under mass failure |
| 7 | **Append-only `job_executions`** | Every attempt is an immutable record | Full audit trail; no UPDATE on audit log |
| 8 | **Three separate binaries** | Independent scaling, fault isolation | Worker scale ≠ Scheduler scale ≠ API scale |
| 9 | **JSONB for payload** | ML job specs vary wildly | No migration needed when payload shape changes |
| 10 | **Go 1.22 pattern routing** | Built-in `{id}` path parameters | No third-party router dependency |

---

## Navigation

| Document | What it covers |
|---|---|
| [`PHASE1-MASTER-GUIDE.md`](PHASE1-MASTER-GUIDE.md) | Phase 1 execution guide, all files, expected output |
| [`PHASE2-MASTER-GUIDE.md`](PHASE2-MASTER-GUIDE.md) | Phase 2 deep dive, CAS pattern, idempotency, wiring |
| [`docs/phase2/README-postgres-db.md`](docs/phase2/README-postgres-db.md) | `postgres/db.go` line-by-line explanation |
| [`docs/phase2/README-tests.md`](docs/phase2/README-tests.md) | How to run tests, what each test verifies |
| [`docs/adr/ADR-001-queue-design.md`](docs/adr/ADR-001-queue-design.md) | Why Redis Streams |
| [`docs/adr/ADR-002-leader-election.md`](docs/adr/ADR-002-leader-election.md) | Why PostgreSQL advisory locks |
| **This file** | Complete project roadmap, all 9 phases |