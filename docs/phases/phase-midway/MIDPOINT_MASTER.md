# Orion — Midpoint Master Document
## Phases 1–4 Complete · Phases 5–9 Ahead

> **Who this document is for:** Anyone picking up the Orion codebase for the first time, a mentor reviewing the work, or Shreeharsh returning to the project after any gap. Read this document and you understand every decision made, every file built, how to run the full system, how to run all tests, and exactly what to build next.

---

## Table of Contents

1. [What Orion Is](#1-what-orion-is)
2. [Architecture — The Full Picture](#2-architecture--the-full-picture)
3. [Technology Stack — Every Choice Explained](#3-technology-stack--every-choice-explained)
4. [Complete File Map — All 50 Files](#4-complete-file-map--all-50-files)
5. [Phase 1 — Foundation Skeleton (Complete)](#5-phase-1--foundation-skeleton)
6. [Phase 2 — PostgreSQL Store (Complete)](#6-phase-2--postgresql-store)
7. [Phase 3 — Inline Executor (Complete)](#7-phase-3--inline-executor)
8. [Phase 4 — Kubernetes Executor (Complete)](#8-phase-4--kubernetes-executor)
9. [The Test Suite — 133 Tests Across 7 Files](#9-the-test-suite--133-tests-across-7-files)
10. [How to Run Everything](#10-how-to-run-everything)
11. [Verification Checklist — Proving the System Works](#11-verification-checklist--proving-the-system-works)
12. [Key Design Decisions](#12-key-design-decisions)
13. [What Phases 5–9 Build](#13-what-phases-59-build)
14. [Navigation Index](#14-navigation-index)

---

## 1. What Orion Is

Orion is a **production-grade distributed ML job orchestrator** built in Go. It solves one problem precisely: **running ML workloads reliably at scale**.

A data scientist submits a training job via HTTP. Orion guarantees:

- **Exactly-once execution** — idempotency keys + CAS state transitions prevent duplicates even under concurrent submission
- **Automatic retry** — failed jobs retry with full-jitter exponential backoff, scattering retries to prevent thundering herd
- **Self-healing** — if a worker crashes mid-job, orphan detection reclaims it within 90 seconds
- **Full audit trail** — every execution attempt is an immutable record in `job_executions`
- **Two execution backends** — inline Go functions (Phase 3) and Kubernetes pods with GPU support (Phase 4)
- **Production observability** — structured JSON logging, OpenTelemetry tracing, Prometheus metrics

---

## 2. Architecture — The Full Picture

```
┌──────────────────────────────────────────────────────────────────────────┐
│  CLIENTS                                                                  │
│  curl · Python SDK · CI/CD pipeline · Kubeflow · Go SDK                  │
└─────────────────────────────┬────────────────────────────────────────────┘
                              │ HTTP :8080
                              ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  API SERVER  (cmd/api/main.go)                                            │
│  POST /jobs                 GET /jobs/{id}                                │
│  GET  /jobs                 GET /jobs/{id}/executions                     │
│  GET  /healthz              GET /readyz  (pings PostgreSQL)               │
└─────────────────────────────┬────────────────────────────────────────────┘
                              │ INSERT / SELECT (store.Store interface)
                              ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  POSTGRESQL 16  (source of truth for ALL state)                           │
│  jobs · job_executions · workers · pipelines · pipeline_jobs              │
│  Partial indexes · updated_at trigger · gen_random_uuid()                 │
└────────────┬─────────────────────────────────────────────────────────────┘
             │ SELECT queued ORDER BY priority DESC (every 2s)
             │ pg_try_advisory_lock(7331001) for leader election
             ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  SCHEDULER  (cmd/scheduler/main.go)  — leader-elected                    │
│  scheduleQueuedJobs()    → CAS queued→scheduled + XADD to Redis          │
│  promoteRetryableJobs()  → CAS failed→retrying, retrying→queued          │
│  reclaimOrphanedJobs()   → reset running→queued when worker dies         │
└────────────┬─────────────────────────────────────────────────────────────┘
             │ XADD (Redis Streams)
             ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  REDIS 7 STREAMS                                                          │
│  orion:queue:high    orion:queue:default    orion:queue:low               │
│  Consumer groups · PEL (Pending Entry List) · XAUTOCLAIM                 │
└────────────┬─────────────────────────────────────────────────────────────┘
             │ XREADGROUP (blocking, per queue goroutine)
             ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  WORKER POOL  (cmd/worker/main.go)                                        │
│  N goroutines (default 10) · buffered jobCh (backpressure)               │
│  Heartbeat every 15s · Graceful drain on SIGTERM                          │
│                                                                           │
│  resolveExecutor(job.Type):                                               │
│    "inline"   → InlineExecutor  → fn(ctx, job)  [Phase 3]                │
│    "k8s_job"  → K8sExecutor    → K8s pod runs  [Phase 4]                 │
└──────────────┬────────────────────────────────────────────────────────────┘
               │ kubectl / client-go
               ▼
┌──────────────────────────────────────────────────────────────────────────┐
│  KUBERNETES CLUSTER                                                       │
│  batchv1.Job · GPU pods · backoffLimit=0 · RestartPolicyNever            │
│  Watch loop for completion events · poll fallback                        │
└──────────────────────────────────────────────────────────────────────────┘

OBSERVABILITY (cross-cutting)
  Prometheus :9091   Jaeger :16686   Grafana :3000   slog JSON logs
```

### Data flow — one job, end to end

```
1.  POST /jobs {"type":"inline","payload":{"handler_name":"preprocess"}}
2.  handler.SubmitJob → store.CreateJob → INSERT INTO jobs (status=queued)
3.  scheduler.scheduleQueuedJobs → CAS queued→scheduled + XADD orion:queue:default
4.  worker.dequeueLoop → XREADGROUP → jobCh ← (backpressure boundary)
5.  worker.runWorker → executeJob → store.MarkJobRunning (CAS scheduled→running)
6.  store.RecordExecution(attempt=1, status=running, started_at=now)
7.  InlineExecutor.Execute → registry.Get("preprocess") → fn(ctx, job)
8a. Success: store.MarkJobCompleted + RecordExecution(completed) + ackFn(nil) [XACK]
8b. Failure: store.MarkJobFailed + RecordExecution(failed) + ackFn(err) [NACK stays PEL]
9.  On failure: scheduler.promoteRetryableJobs → failed→retrying→queued (with backoff)
10. Repeat from step 3 until completed or dead (max_retries exhausted)
```

---

## 3. Technology Stack — Every Choice Explained

| Layer | Technology | Version | Why this, not the alternative |
|---|---|---|---|
| Language | Go | 1.22 | Native goroutines for concurrent job execution. Single binary. Fast compile. stdlib has everything needed (slog, http, sync). |
| Database | PostgreSQL | 16 | **CAS via `UPDATE WHERE status = expected`** — atomic state transitions without external locks. Advisory locks for leader election. JSONB for flexible payloads. |
| Queue | Redis Streams | 7 | **At-least-once delivery via PEL + XACK**. Redis Lists lose messages on crash. Streams track which messages are in-flight and deliver them to new consumers after crash. |
| K8s Client | `k8s.io/client-go` | v0.29.2 | Official Go SDK. Ships `fake.NewSimpleClientset()` for testing — no cluster needed in unit tests. Same library Kubeflow uses. |
| Tracing | OpenTelemetry → Jaeger | 1.24.0 | Vendor-neutral OTLP format. Jaeger for local dev, any OTLP-compatible backend in production. |
| Metrics | Prometheus | v1.19.0 | Pull-based scraping. PromQL for alerting. Standard across cloud-native ML stacks. |
| Logging | `log/slog` | stdlib | Zero dependencies. JSON in production, text in development. Structured key-value pairs. |
| Retry | `pkg/retry` (internal) | — | Full-jitter backoff prevents thundering herd when many jobs fail simultaneously. |
| Container | Docker + Compose | — | All 5 infra services in one `docker compose up`. Zero local setup beyond Docker. |

---

## 4. Complete File Map — All 50 Files

### Source files (production code)

```
cmd/
├── api/main.go                     API server: pgxpool + store + HTTP routes + graceful shutdown
├── scheduler/main.go               Scheduler: pgxpool (for advisory locks) + store + queue
└── worker/main.go                  Worker: store + queue + InlineExecutor + KubernetesExecutor

internal/
├── api/handler/
│   └── job.go                      POST/GET /jobs, GET /jobs/{id}/executions — thin, no SQL
├── config/
│   └── config.go                   All ORION_* env vars with defaults
├── domain/
│   ├── job.go                      Job struct, 8-state machine, ValidTransitions, KubernetesSpec
│   ├── worker.go                   Worker struct, IsAlive(), AvailableSlots()
│   └── pipeline.go                 Pipeline, DAGSpec, DAGNode, DAGEdge, ReadyNodes()
├── observability/
│   └── observability.go            Prometheus metrics, OTel tracing, slog logger factory
├── queue/
│   ├── queue.go                    Queue interface + AckFunc type
│   └── redis/
│       └── redis_queue.go          XADD, XREADGROUP, XAUTOCLAIM, XACK implementation
├── scheduler/
│   └── scheduler.go                Leader election + 3 dispatch loops (schedule, promote, reclaim)
├── store/
│   ├── store.go                    JobStore + ExecutionStore + WorkerStore interfaces + sentinels
│   └── postgres/
│       └── db.go                   ALL SQL — 12 methods, CAS patterns, idempotency, scanning
├── store/migrations/
│   ├── 001_initial_schema.up.sql   5 tables, 4 partial indexes, updated_at trigger
│   └── 001_initial_schema.down.sql DROP TABLE in dependency order
└── worker/
    ├── pool.go                     Goroutine pool, backpressure, executeJob, RecordExecution
    ├── inline.go                   Registry (RWMutex), HandlerFunc, InlineExecutor
    ├── handlers/
    │   └── handlers.go             Noop, Echo, AlwaysFail, Slow, PanicRecover
    └── k8s/
        ├── spec.go                 domain.KubernetesSpec → batchv1.Job translation
        └── executor.go             KubernetesExecutor, Watch loop, poll fallback, BuildK8sClient

pkg/
└── retry/
    └── retry.go                    FullJitterBackoff, EqualJitterBackoff, WithRetry

deploy/
└── k8s/
    └── rbac.yaml                   ServiceAccount + ClusterRole + ClusterRoleBinding

go.mod                              All dependencies
docker-compose.yml                  postgres:16, redis:7, jaeger:1.55, prometheus, grafana
Makefile                            build, test, migrate, run-*, lint, fmt, infra-up/down
```

### Test files (7 files, 133 tests total)

```
internal/domain/domain_test.go                 21 tests  — state machine, IsTerminal, DAG logic
pkg/retry/retry_test.go                        12 tests  — backoff math, WithRetry behaviour
internal/api/handler/job_test.go               19 tests  — all 4 HTTP routes, fake store
internal/worker/inline_test.go                 28 tests  — Registry, InlineExecutor, handlers
internal/store/postgres/store_unit_test.go     10 tests  — StoreError, sentinels, TransitionOption
internal/store/postgres/postgres_integration_test.go  20 tests  — real DB, all 12 store methods
internal/worker/k8s/executor_test.go           23 tests  — fake K8s client, spec fields, paths
```

### Documentation files

```
ROADMAP.md                          9-phase roadmap, architecture, technology decisions
PHASE1-MASTER-GUIDE.md              Phase 1 execution guide
PHASE2-MASTER-GUIDE.md              CAS pattern, idempotency, store deep-dive
docs/phase2/README-postgres-db.md   postgres/db.go line-by-line
docs/phase2/README-entrypoints.md   cmd/ wiring explanation
docs/phase2/README-handler.md       All 4 HTTP routes documented
docs/phase2/README-tests.md         How to run tests
docs/phase3/PHASE3-MASTER-GUIDE.md  Inline executor planning + design
docs/phase3/README-inline.md        Registry + InlineExecutor deep-dive
docs/phase3/README-handlers.md      Built-in handlers: patterns and usage
docs/phase4/PHASE4-MASTER-GUIDE.md  K8s executor planning + design
docs/phase4/README-k8s-executor.md  spec.go + executor.go deep-dive
docs/phase4/README-wiring-rbac.md   RBAC + main.go wiring
docs/adr/ADR-001-queue-design.md    Why Redis Streams not Redis Lists
docs/adr/ADR-002-leader-election.md Why PostgreSQL advisory locks not ZooKeeper
```

---

## 5. Phase 1 — Foundation Skeleton

**Status: ✅ Complete**

### What was built

The complete architectural skeleton. Every interface defined. Every domain type written. Every goroutine pattern established. The deliberate gap: `store = nil` in all three binaries (filled in Phase 2).

### Key files

| File | What it defines |
|---|---|
| `domain/job.go` | `Job` struct, 8-state machine, `ValidTransitions`, `KubernetesSpec`, `CanTransitionTo`, `IsTerminal`, `IsRetryable` |
| `domain/worker.go` | `Worker` struct, `IsAlive()`, `AvailableSlots()` |
| `domain/pipeline.go` | `Pipeline`, `DAGSpec`, `ReadyNodes()` |
| `store/store.go` | `Store` interface (composite of JobStore + ExecutionStore + WorkerStore), `TransitionOption` pattern, sentinel errors |
| `queue/queue.go` | `Queue` interface, `AckFunc` type |
| `queue/redis/redis_queue.go` | XADD, XREADGROUP, XAUTOCLAIM, XACK |
| `scheduler/scheduler.go` | Leader election via `pg_try_advisory_lock(7331001)`, 3 dispatch loops |
| `worker/pool.go` | Bounded goroutine pool, buffered `jobCh` (backpressure), `Executor` interface, graceful drain |
| `pkg/retry/retry.go` | `FullJitterBackoff` (thundering herd prevention) |
| `store/migrations/001_*` | 5 tables: `jobs`, `job_executions`, `workers`, `pipelines`, `pipeline_jobs` |

### The state machine

```
                     ┌──────────────────────────────────────┐
                     │                                      │
              cancel │                               cancel │
                     ▼                                      │
queued ──────────► scheduled ──────────► running            │
  │                    │                  │   │             │
  │                    │ (reclaim)        │   │             │
  │                    └──────────────────┘   │             │
  │                                           ▼             │
  │                                       completed ←───────┘
  │                                       (terminal)
  │
  └──► cancelled (terminal)

running ──► failed ──► retrying ──► queued (retry loop)
                 │
                 └──► dead (terminal, max_retries exhausted)
```

---

## 6. Phase 2 — PostgreSQL Store

**Status: ✅ Complete**

### What was built

`internal/store/postgres/db.go` — 798 lines, implements all 12 store methods. Zero SQL anywhere else in the codebase.

### The three critical patterns

**CAS (Compare-And-Swap):**
```sql
UPDATE jobs SET status = $new_status [, worker_id = $4] [, started_at = $5] ...
WHERE id = $1 AND status = $expected_status    ← atomic guard
RETURNING id                                    ← 0 rows = ErrStateConflict
```
Two schedulers cannot both claim the same job. PostgreSQL guarantees this atomically.

**Idempotency:**
```
Normal: SELECT (not found) → INSERT → return new job
Retry:  SELECT (found) → return existing job (no INSERT)
Race:   both pass SELECT → one INSERT wins → other gets 23505 → SELECT → return winner's job
```

**Orphan reclaim:**
```sql
WITH orphaned AS (SELECT j.id FROM jobs j WHERE j.status = 'running'
  AND NOT EXISTS (SELECT 1 FROM workers w WHERE w.id = j.worker_id
                  AND w.last_heartbeat > NOW() - $1::interval))
UPDATE jobs SET status = 'queued', worker_id = NULL
FROM orphaned WHERE jobs.id = orphaned.id
RETURNING jobs.id
```

### New API route (Phase 2 addition)

```
GET /jobs/{id}/executions  →  append-only audit log of every attempt
```

### Files changed

- `internal/store/postgres/db.go` — NEW: all 12 SQL methods
- `internal/store/store.go` — updated: `IsNotFound`, `IsStateConflict`, `IsDuplicate` helpers
- `internal/api/handler/job.go` — updated: `GetExecutions` handler added
- `cmd/api/main.go` — updated: `postgres.New(db)` wired
- `cmd/scheduler/main.go` — updated: store + queue wired
- `cmd/worker/main.go` — updated: store + queue wired

---

## 7. Phase 3 — Inline Executor

**Status: ✅ Complete**

### What was built

The first real executor — Go functions run as jobs. After Phase 3, `status: completed` appears in the database for the first time.

### How it works

```
Registry: map[string]HandlerFunc  (sync.RWMutex, read-heavy, zero runtime contention)

InlineExecutor.Execute(ctx, job):
  1. registry.Get(job.Payload.HandlerName) → fn
  2. fn(ctx, job) → nil or error
  3. return result to pool (which calls MarkJobCompleted or MarkJobFailed)
```

### The mandatory handler pattern

```go
// Every long-running handler MUST check ctx.Done():
func MyHandler(ctx context.Context, job *domain.Job) error {
    for _, item := range items {
        if err := ctx.Err(); err != nil { return err }  // check before each unit of work
        processItem(item)
    }
    return nil
}

// Or for blocking waits:
select {
case <-time.After(duration): return nil
case <-ctx.Done():           return ctx.Err()  // always return ctx.Err(), not a custom error
}
```

### Files added

| File | What it contains |
|---|---|
| `internal/worker/inline.go` | `HandlerFunc` type, `Registry` (RWMutex), `InlineExecutor` |
| `internal/worker/handlers/handlers.go` | `Noop`, `Echo`, `AlwaysFail`, `Slow`, `PanicRecover` |
| `internal/worker/inline_test.go` | 28 unit tests including concurrent registry access with `-race` |

### Files changed

- `internal/worker/pool.go` — updated: `RecordExecution` wired into `executeJob()` at start, on failure, on success
- `cmd/worker/main.go` — updated: registry + `InlineExecutor` replaces nil executor slice

### RecordExecution wiring (Phase 3 addition to pool.go)

```go
// In executeJob():
_ = p.store.RecordExecution(ctx, &domain.JobExecution{
    JobID: job.ID, Attempt: job.Attempt + 1,
    Status: domain.JobStatusRunning, StartedAt: &startedAt,
})
// ... execute ...
_ = p.store.RecordExecution(ctx, &domain.JobExecution{
    JobID: job.ID, Attempt: job.Attempt + 1,
    Status: domain.JobStatusCompleted/Failed,
    StartedAt: &startedAt, FinishedAt: &finishedAt,
})
```

---

## 8. Phase 4 — Kubernetes Executor

**Status: ✅ Complete**

### What was built

`KubernetesExecutor` — launches real Kubernetes pods for GPU-intensive ML training. Uses `client-go` with Watch-based completion detection and poll fallback.

### The translation layer (spec.go)

```
domain.KubernetesSpec             batchv1.Job
─────────────────────────         ────────────────────────────────────────
Image         ──────────────────► containers[0].image
Command       ──────────────────► containers[0].command
Args          ──────────────────► containers[0].args
EnvVars       ──────────────────► containers[0].env  (sorted alphabetically)
CPU / Memory  ──────────────────► resources.requests == limits (Guaranteed QoS)
GPU           ──────────────────► resources["nvidia.com/gpu"]
Namespace     ──────────────────► Job namespace
TTLSeconds    ──────────────────► spec.ttlSecondsAfterFinished  (default: 86400)
                                  backoffLimit: 0   (Orion owns all retries)
                                  RestartPolicy: Never
```

### Four non-negotiable decisions in spec.go

| Decision | What | Why |
|---|---|---|
| `backoffLimit = 0` | K8s does NOT retry pods | Orion owns all retry logic. K8s retry would desync the state machine. |
| `RestartPolicy = Never` | Pod fails immediately on non-zero exit | `OnFailure` restarts in-place; Orion never sees failure. |
| Guaranteed QoS | `requests == limits` | Training pods never evicted mid-run. |
| `TTLSecondsAfterFinished` | Default 24h auto-cleanup | Prevents accumulation of completed Jobs. |

### The Watch loop

```go
watcher, _ := client.BatchV1().Jobs(ns).Watch(ctx, ...)
for event := range watcher.ResultChan() {
    job := event.Object.(*batchv1.Job)
    if Complete=True  → return nil       // success
    if Failed=True    → return error     // failure
    if channel closed → pollForCompletion()  // fallback
}
// On ctx cancelled: Delete K8s Job with fresh context (30s) → no zombie GPU pods
```

### Files added

| File | What it contains |
|---|---|
| `internal/worker/k8s/spec.go` | `buildK8sJob()`, `buildResourceRequirements()`, `buildEnvVars()` |
| `internal/worker/k8s/executor.go` | `KubernetesExecutor`, Watch+poll loop, `BuildK8sClient()` |
| `internal/worker/k8s/executor_test.go` | 23 tests using `fake.NewSimpleClientset()` — no cluster needed |
| `deploy/k8s/rbac.yaml` | Namespaces, ServiceAccount, ClusterRole, ClusterRoleBinding |

### Files changed

- `cmd/worker/main.go` — updated: `BuildK8sClient()` called (non-fatal failure), `K8sExecutor` appended to executor slice

### GPU job example

```bash
curl -X POST http://localhost:8080/jobs -d '{
  "name": "train-resnet50",
  "type": "k8s_job",
  "queue_name": "high",
  "priority": 9,
  "max_retries": 2,
  "payload": {
    "kubernetes_spec": {
      "image": "pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime",
      "command": ["python", "-m", "train"],
      "args": ["--model=resnet50", "--epochs=50"],
      "namespace": "ml-training",
      "resources": {"cpu": "8", "memory": "32Gi", "gpu": 2},
      "env_vars": {"WANDB_PROJECT": "orion", "S3_BUCKET": "ml-artifacts"},
      "ttl_seconds": 3600
    }
  }
}'
```

---

## 9. The Test Suite — 133 Tests Across 7 Files

### Overview

| File | Type | Tests | What it covers |
|---|---|---|---|
| `internal/domain/domain_test.go` | Unit | 21 | State machine, `CanTransitionTo`, `IsTerminal`, `IsRetryable`, `ReadyNodes` DAG, `Worker.IsAlive`, `Worker.AvailableSlots` |
| `pkg/retry/retry_test.go` | Unit | 12 | `FullJitterBackoff` (non-negative, capped, grows), `EqualJitterBackoff` (minimum delay), `WithRetry` (success, retry, exhaustion, ctx cancel) |
| `internal/api/handler/job_test.go` | Unit | 19 | All 4 HTTP routes with fake store: 201/200/400/422/404/500 responses, idempotency, content-type header |
| `internal/worker/inline_test.go` | Unit | 28 | Registry CRUD, concurrent RW (run with `-race`), panic guards, `InlineExecutor` routing, context cancellation, args pass-through |
| `internal/store/postgres/store_unit_test.go` | Unit | 10 | `StoreError.Is()`, sentinel errors, `TransitionOption` application |
| `internal/store/postgres/postgres_integration_test.go` | Integration | 20 | All 12 store methods against real PostgreSQL: CAS conflict, idempotency race, orphan reclaim, audit log |
| `internal/worker/k8s/executor_test.go` | Unit | 23 | `spec.go` field verification (backoffLimit=0, RestartPolicy=Never, GPU resources, sorted envvars), Watch→success, Watch→failure, Watch drop→poll fallback, ctx cancel, namespace resolution |

**Total: 133 tests (113 unit + 20 integration)**

### Running tests

```bash
# All unit tests (no infrastructure needed, takes ~3 seconds)
go test -race ./... -count=1

# Specific package
go test -race ./internal/domain/...            -v
go test -race ./pkg/retry/...                  -v
go test -race ./internal/api/handler/...       -v
go test -race ./internal/worker/...            -v
go test -race ./internal/worker/k8s/...        -v
go test -race ./internal/store/postgres/...    -v   # unit tests only

# Integration tests (requires: docker compose up -d && make migrate-up)
go test -tags=integration -race ./internal/store/postgres/... -v

# Coverage report
go test -race ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

### What "-race" catches

The `-race` flag enables Go's race detector. Critical for Orion because:
- `Registry` uses `sync.RWMutex` — the race detector verifies no data races under concurrent access
- `Pool.activeCount` uses `sync/atomic` — the race detector verifies correct atomic usage
- Worker goroutines share the store interface — any unprotected shared state is caught

---

## 10. How to Run Everything

### Prerequisites

```bash
# Install tools
go install golang.org/x/tools/cmd/goimports@latest
go install golang.org/x/lint/golint@latest
# For K8s Phase 4 local testing:
go install sigs.k8s.io/kind@latest
```

### Step 1 — Start infrastructure

```bash
# Start PostgreSQL, Redis, Jaeger, Prometheus, Grafana
make infra-up

# Verify all containers are healthy (takes ~10 seconds)
docker compose ps
# All 5 should show "healthy"
```

### Step 2 — Apply database migrations

```bash
make migrate-up
# Creates: jobs, job_executions, workers, pipelines, pipeline_jobs tables
# Applies indexes and updated_at trigger

# Verify:
docker compose exec postgres psql -U orion -d orion -c "\dt"
```

### Step 3 — Build all binaries

```bash
make build
# Produces: bin/orion-api, bin/orion-scheduler, bin/orion-worker

# Or build individually:
make build-api
make build-scheduler
make build-worker
```

### Step 4 — Run all three services (separate terminals)

**Terminal 1 — API:**
```bash
ORION_ENV=development make run-api
# INFO msg="API server listening" port=8080 env=development
```

**Terminal 2 — Scheduler:**
```bash
ORION_ENV=development make run-scheduler
# INFO msg="scheduler starting, contending for leader lock"
# INFO msg="acquired scheduler leader lock"
```

**Terminal 3 — Worker:**
```bash
ORION_ENV=development \
ORION_K8S_IN_CLUSTER=false \
make run-worker
# INFO msg="inline executor ready" registered_handlers=[always_fail echo noop slow]
# INFO msg="kubernetes executor ready" default_namespace=orion-jobs
# (or WARN if no kind cluster running — inline still works)
```

### Step 5 — Verify infrastructure URLs

| Service | URL | What to check |
|---|---|---|
| API | http://localhost:8080/healthz | `{"status":"ok"}` |
| API | http://localhost:8080/readyz | `{"status":"ready"}` (DB reachable) |
| Jaeger | http://localhost:16686 | Trace viewer |
| Prometheus | http://localhost:9090 | Metrics scraping |
| Grafana | http://localhost:3000 | admin/admin |

---

## 11. Verification Checklist — Proving the System Works

Run these in order after startup. Each step proves one layer of the system works correctly.

### Tier 1 — Infrastructure

```bash
# All must return healthy
docker compose ps | grep -E "healthy|running"

# API alive
curl -s localhost:8080/healthz | jq .
# {"status":"ok"}

# DB reachable
curl -s localhost:8080/readyz | jq .
# {"status":"ready"}

# Workers registered in DB
docker exec -it $(docker compose ps -q postgres) \
  psql -U orion -d orion -c "SELECT id, status, last_heartbeat FROM workers;"
# → at least one row with recent last_heartbeat
```

### Tier 2 — Inline job happy path (Phase 3)

```bash
# Submit a noop job
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"midpoint-test","type":"inline","payload":{"handler_name":"noop"}}' \
  | jq -r .id)
echo "Job: $JOB_ID"

# Watch it complete (should take 2-5 seconds)
for i in $(seq 1 15); do
  STATUS=$(curl -s http://localhost:8080/jobs/$JOB_ID | jq -r .status)
  echo "$(date +%H:%M:%S) $STATUS"
  [ "$STATUS" = "completed" ] && echo "✅ INLINE HAPPY PATH WORKS" && break
  sleep 1
done

# Verify execution audit log
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .
# count=1, attempt=1, status=completed, started_at+finished_at both set
```

### Tier 3 — Retry cycle (Phase 3)

```bash
FAIL_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"retry-test","type":"inline","payload":{"handler_name":"always_fail"},"max_retries":2}' \
  | jq -r .id)

# Watch retry cycle (takes 1-2 minutes due to backoff)
watch -n2 "curl -s http://localhost:8080/jobs/$FAIL_ID | jq '{status,attempt,error_message}'"
# failed(1) → retrying → queued → failed(2) → retrying → queued → failed(3) → dead

# Verify 3 audit records
curl -s http://localhost:8080/jobs/$FAIL_ID/executions | jq .count
# 3  ✅

echo "✅ RETRY CYCLE + DEAD STATE WORKS"
```

### Tier 4 — Idempotency (Phase 2)

```bash
KEY="idempotency-test-$(date +%s)"

# First submission → 201
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"idem-test\",\"type\":\"inline\",\"payload\":{\"handler_name\":\"noop\"},\"idempotency_key\":\"$KEY\"}")
echo "First: HTTP $CODE"  # 201

# Second submission → 200 (same job_id)
ID1=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"idem-test\",\"type\":\"inline\",\"payload\":{\"handler_name\":\"noop\"},\"idempotency_key\":\"$KEY\"}" | jq -r .id)
ID2=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"idem-test\",\"type\":\"inline\",\"payload\":{\"handler_name\":\"noop\"},\"idempotency_key\":\"$KEY\"}" | jq -r .id)

[ "$ID1" = "$ID2" ] && echo "✅ IDEMPOTENCY WORKS (same ID returned)" || echo "❌ FAILED"
```

### Tier 5 — Kubernetes job (Phase 4) — requires kind cluster

```bash
# First time only:
kind create cluster --name orion-dev
kubectl apply -f deploy/k8s/rbac.yaml

# Verify RBAC
kubectl auth can-i create jobs \
  --as=system:serviceaccount:orion-system:orion-worker -n orion-jobs
# yes ✅

# Submit k8s smoke test
K8S_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name":"k8s-smoke-test",
    "type":"k8s_job",
    "payload":{"kubernetes_spec":{
      "image":"busybox:latest",
      "command":["sh","-c"],
      "args":["echo Hello && sleep 2 && exit 0"],
      "namespace":"orion-jobs",
      "resources":{"cpu":"100m","memory":"64Mi"}
    }}
  }' | jq -r .id)

# Watch pod + status
kubectl get pods -n orion-jobs -w &
for i in $(seq 1 30); do
  STATUS=$(curl -s http://localhost:8080/jobs/$K8S_ID | jq -r .status)
  echo "$STATUS"
  [ "$STATUS" = "completed" ] && echo "✅ K8S JOB WORKS" && break
  sleep 2
done
```

### Tier 6 — Database state verification

```bash
PSQL="docker exec -i $(docker compose ps -q postgres) psql -U orion -d orion -c"

# Job lifecycle states visible
$PSQL "SELECT name, status, attempt, completed_at FROM jobs ORDER BY created_at DESC LIMIT 10;"

# Audit trail intact
$PSQL "SELECT job_id, attempt, status, started_at, finished_at FROM job_executions ORDER BY created_at DESC LIMIT 10;"

# Workers heartbeating
$PSQL "SELECT id, status, last_heartbeat FROM workers;"
```

### Tier 7 — All tests pass

```bash
# Unit tests (no infrastructure needed)
go test -race ./... -count=1
# ok  github.com/shreeharsh-a/orion/internal/domain         0.003s
# ok  github.com/shreeharsh-a/orion/internal/api/handler    0.005s
# ok  github.com/shreeharsh-a/orion/internal/worker         0.012s
# ok  github.com/shreeharsh-a/orion/internal/worker/k8s     0.018s
# ok  github.com/shreeharsh-a/orion/internal/store/postgres  0.004s
# ok  github.com/shreeharsh-a/orion/pkg/retry               0.003s

# Integration tests (requires running postgres)
go test -tags=integration -race ./internal/store/postgres/... -v
# --- PASS: TestCreateJob_Basic
# --- PASS: TestTransitionJobState_CAS_Conflict
# ... (all 20 pass)
```

---

## 12. Key Design Decisions

These decisions constrain all future phases. Understanding them is understanding the architecture.

### D1 — Redis Streams over Redis Lists

**Choice:** Redis Streams with consumer groups (XADD/XREADGROUP/XACK)

**Why not Lists (LPUSH/BRPOP):** If a worker crashes after dequeuing from a List but before completing the job, the job is permanently lost. Streams maintain a PEL (Pending Entry List) — every dequeued message stays in the PEL until explicitly acknowledged with XACK. Crashed workers leave their jobs in the PEL; XAUTOCLAIM delivers them to another worker.

**Impact:** At-least-once delivery guaranteed. No job is ever silently lost.

### D2 — PostgreSQL advisory locks for leader election

**Choice:** `pg_try_advisory_lock(7331001)` on a dedicated DB connection

**Why not ZooKeeper / etcd:** Adding ZooKeeper or etcd requires operating another distributed system. PostgreSQL is already required. Advisory locks are session-scoped — if the scheduler crashes, the connection closes, the lock releases automatically, and a standby scheduler acquires it within seconds.

**Impact:** Zero extra infrastructure. Scheduler HA is free.

### D3 — CAS via `UPDATE WHERE status = expected RETURNING id`

**Choice:** Atomic SQL CAS in every state transition

**Why not application-level locking:** SELECT then UPDATE in two steps creates a race condition window — another process can change the row between the SELECT and UPDATE. The single SQL statement `UPDATE WHERE status = expected` is atomic at the database level. If 0 rows are affected, someone else won. Return `ErrStateConflict` and move on.

**Impact:** Two schedulers can never double-dispatch the same job. Zero coordination overhead.

### D4 — `kubernetes.Interface` not `*kubernetes.Clientset`

**Choice:** Accept the interface type in `KubernetesExecutor`

**Why:** `fake.NewSimpleClientset()` implements `kubernetes.Interface`. Testing with the real `*kubernetes.Clientset` requires a real cluster. By accepting the interface, all 23 K8s tests run without any cluster.

**Impact:** Full test coverage of K8s executor without cluster infrastructure.

### D5 — `backoffLimit=0` and `RestartPolicyNever` in K8s Jobs

**Choice:** K8s must not retry or restart pods

**Why:** Orion owns the entire retry lifecycle through its state machine and backoff policy. If K8s retried a pod independently (backoffLimit=3), Orion would see the job stuck in `running` status for hours while K8s retried. If K8s restarted the container in-place (`RestartPolicyOnFailure`), Orion would never see the failure event.

**Impact:** Orion has full control over retry timing, backoff, and dead-letter handling.

### D6 — Buffered `jobCh` as backpressure boundary

**Choice:** `jobCh := make(chan *jobTask, cfg.Concurrency)`

**Why:** When all N worker goroutines are busy, dequeue goroutines block on `jobCh <- task`. Jobs stay in Redis PEL (not in process memory). If the worker crashes, jobs are redelivered by Redis (after XAUTOCLAIM timeout). No in-memory job accumulation.

**Impact:** Crash-safe under load. Memory bounded to N in-flight jobs.

### D7 — Append-only `job_executions` table

**Choice:** `INSERT ... ON CONFLICT DO NOTHING` — never UPDATE

**Why:** Every execution attempt is a permanent forensic record. Updating rows would lose information about retries. The `ON CONFLICT DO NOTHING` makes each insert idempotent — if the worker retries `RecordExecution` (e.g., after a transient DB error), the second call is silently ignored.

**Impact:** Full forensic history. 3 attempts = 3 rows, always.

---

## 13. What Phases 5–9 Build

### Phase 5 — Pipeline Orchestration (DAG)

```
POST /pipelines -d '{
  "nodes": [preprocess, train, evaluate, deploy],
  "edges": [preprocess→train, train→evaluate, evaluate→deploy]
}'
```

Infrastructure already exists: `domain/pipeline.go` has `ReadyNodes()`, `migrations/001` has the `pipelines` and `pipeline_jobs` tables. Phase 5 adds the handler, PipelineStore SQL, and the scheduler's pipeline advancement loop.

### Phase 6 — Full Observability

Wire the existing `observability.go` metrics into every operation: scheduler dispatch latency histogram, queue depth gauge, job duration by type, worker utilisation. Add pre-built Grafana dashboard.

### Phase 7 — gRPC Streaming API

`WatchJob(job_id)` — clients receive real-time status events as jobs transition states. No more polling `/jobs/{id}` every second. `.proto` file, generated stubs, gRPC server on `:9090`.

### Phase 8 — Rate Limiting and Fair Scheduling

Per-queue concurrency limits: high queue can use at most 80% of worker capacity. Prevents batch workloads from starving interactive ML jobs.

### Phase 9 — Helm + Production Kubernetes

`helm install orion ./deploy/helm` deploys all three services with HPA autoscaling, TLS, RBAC, PodDisruptionBudgets, and Prometheus ServiceMonitors.

---

## 14. Navigation Index

| You want to understand | Read this |
|---|---|
| The full 9-phase plan | `ROADMAP.md` |
| Phase 1 in depth | `PHASE1-MASTER-GUIDE.md` |
| Phase 2 — CAS, idempotency, SQL | `PHASE2-MASTER-GUIDE.md` + `docs/phase2/README-postgres-db.md` |
| Phase 3 — Inline executor, handlers | `docs/phase3/PHASE3-MASTER-GUIDE.md` + `docs/phase3/README-inline.md` |
| Phase 4 — K8s executor, RBAC | `docs/phase4/PHASE4-MASTER-GUIDE.md` + `docs/phase4/README-k8s-executor.md` |
| How to run tests | Section 9 + `docs/phase2/README-tests.md` |
| Queue design decision | `docs/adr/ADR-001-queue-design.md` |
| Leader election decision | `docs/adr/ADR-002-leader-election.md` |
| All HTTP routes | `docs/phase2/README-handler.md` |
| Verification step-by-step | Section 11 (this document) |
| This document | `MIDPOINT-MASTER.md` |