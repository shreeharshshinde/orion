# Orion — Phase 1 Complete Master Guide

> **What this document is:** A single reference covering every file built in Phase 1, the mental model behind the architecture, how to run everything from scratch, what you should observe at each step, and what Phase 2 builds next.

---

## Table of Contents

1. [What We Built — The Big Picture](#1-what-we-built--the-big-picture)
2. [Mental Model — How the Three Services Work Together](#2-mental-model--how-the-three-services-work-together)
3. [Complete File Map](#3-complete-file-map)
4. [Every File Explained in One Line](#4-every-file-explained-in-one-line)
5. [Dependency Graph — How Files Import Each Other](#5-dependency-graph--how-files-import-each-other)
6. [How to Execute Phase 1 — Step by Step](#6-how-to-execute-phase-1--step-by-step)
7. [Expected Output at Every Stage](#7-expected-output-at-every-stage)
8. [Verify Everything is Working](#8-verify-everything-is-working)
9. [Key Design Decisions and Why](#9-key-design-decisions-and-why)
10. [What Phase 1 Cannot Do Yet](#10-what-phase-1-cannot-do-yet)
11. [Phase 2 Preview — What We Build Next](#11-phase-2-preview--what-we-build-next)

---

## 1. What We Built — The Big Picture

Orion is a **distributed ML job orchestrator** — a system that:
- Accepts job submissions via HTTP (a training run, a preprocessing script, a K8s container)
- Stores them durably in PostgreSQL
- Dispatches them to workers via Redis Streams
- Executes them with at-least-once delivery guarantees
- Retries failures with exponential backoff
- Recovers from worker crashes automatically

It is comparable to a lightweight Celery, Airflow, or Kubeflow Pipelines — built from scratch in Go, cloud-native, designed for ML workloads.

**Phase 1 covers the complete skeleton:** all interfaces defined, all business logic written, all goroutine patterns established, infrastructure running. The one thing Phase 1 leaves as `nil` placeholders is the PostgreSQL implementation — that is Phase 2.

---

## 2. Mental Model — How the Three Services Work Together

```
┌───────────────────────────────────────────────────────────────┐
│                         CLIENT                                │
│  curl, SDK, CI/CD pipeline, Kubeflow, any HTTP client         │
└───────────────────────────────┬───────────────────────────────┘
                                │ POST /jobs
                                ↓
┌───────────────────────────────────────────────────────────────┐
│                       API SERVER (:8080)                      │
│                                                               │
│  Receives job submissions                                     │
│  Checks idempotency key (no duplicates)                       │
│  Validates request                                            │
│  Writes job to PostgreSQL with status = queued                │
│  Returns 201 {job_id, status: "queued"}                       │
└───────────────────────────────┬───────────────────────────────┘
                                │ INSERT jobs SET status='queued'
                                ↓
┌─────────────────────────────────────────────────────────────────────────┐
│                          POSTGRESQL                                     │
│                                                                         │
│  Source of truth. Every job state transition happens here.              │
│  Tables: jobs, job_executions, workers, pipelines, pipeline_jobs        │
└──────────┬──────────────────────────────────────────────────────────────┘
           │ SELECT WHERE status='queued' ORDER BY priority DESC (every 2s)
           │
┌──────────↓──────────────────────────────────────────────────────────────┐
│                         SCHEDULER                                        │
│                                                                          │
│  Leader election via PostgreSQL advisory lock                            │
│  Only ONE scheduler is active at a time even if 10 pods are running     │
│                                                                          │
│  Every 2s:  Dispatch queued → scheduled, XADD to Redis                  │
│  Every 2s:  Promote failed jobs whose retry delay has passed             │
│  Every 30s: Reclaim running jobs whose worker stopped heartbeating       │
└──────────┬───────────────────────────────────────────────────────────────┘
           │ XADD orion:queue:default * job_id "..." payload "{...}"
           ↓
┌─────────────────────────────────────────────────────────────────────────┐
│                          REDIS STREAMS                                  │
│                                                                         │
│  orion:queue:high     ← for priority 8-10 jobs                         │
│  orion:queue:default  ← for priority 4-7 jobs (most jobs go here)      │
│  orion:queue:low      ← for priority 1-3 jobs                          │
│  orion:queue:dead     ← jobs that exhausted all retries                 │
│                                                                         │
│  Consumer group: orion-workers                                          │
│  At-least-once delivery via PEL + XACK                                  │
└──────────┬───────────────────────────────────────────────────────────────┘
           │ XREADGROUP (blocking) — each message delivered to ONE worker
           ↓
┌─────────────────────────────────────────────────────────────────────────┐
│                        WORKER POOL                                      │
│                                                                         │
│  N dequeue goroutines (one per queue)                                   │
│  M worker goroutines (Concurrency, default 10)                          │
│  Buffered jobCh channel = backpressure boundary                         │
│                                                                         │
│  For each job:                                                           │
│    1. Mark running in PostgreSQL (CAS: scheduled → running)             │
│    2. Resolve executor (inline or k8s_job)                              │
│    3. Execute with optional deadline                                     │
│    4a. SUCCESS → mark completed + XACK (remove from Redis PEL)          │
│    4b. FAILURE → mark failed + set next_retry_at + NACK (stay in PEL)  │
│                                                                         │
│  Heartbeat: every 15s tells PostgreSQL "this worker is alive"           │
│  Graceful shutdown: drains in-flight jobs on SIGTERM                    │
└─────────────────────────────────────────────────────────────────────────┘
```

### The one golden rule

**PostgreSQL is always the truth. Redis is a fast delivery pipe.**

If Redis crashes and restarts with AOF enabled, all queued messages come back. If they don't, the scheduler's orphan sweep (every 30s) reclaims any job stuck in `scheduled` state without a message in Redis and re-enqueues it. The system is self-healing.

---

## 3. Complete File Map

```
orion/
│
├── go.mod                                     Step 01 — module identity + dependencies
│
├── internal/
│   ├── domain/
│   │   ├── job.go                             Step 02 — Job struct, state machine, ValidTransitions
│   │   ├── worker.go                          Step 02 — Worker struct, heartbeat helpers
│   │   └── pipeline.go                        Step 02 — Pipeline, DAGSpec, ReadyNodes()
│   │
│   ├── config/
│   │   └── config.go                          Step 03 — all ORION_* env vars, Load()
│   │
│   ├── store/
│   │   ├── store.go                           Step 04 — JobStore, ExecutionStore, WorkerStore interfaces
│   │   ├── migrations/
│   │   │   └── 001_initial_schema.up.sql      Step 12 — all tables, indexes, trigger
│   │   └── postgres/
│   │       └── db.go                          Phase 2 — PostgreSQL implementation (725 lines)
│   │
│   ├── queue/
│   │   ├── queue.go                           Step 05 — Queue interface, AckFunc, queue names
│   │   └── redis/
│   │       └── redis_queue.go                 Step 07 — Redis Streams implementation
│   │
│   ├── observability/
│   │   └── observability.go                   Step 08 — Prometheus metrics, OTel tracing, slog
│   │
│   ├── api/
│   │   └── handler/
│   │       └── job.go                         Step 09 — HTTP handlers: POST/GET /jobs
│   │
│   ├── scheduler/
│   │   └── scheduler.go                       Step 10 — leader election, 3 dispatch loops
│   │
│   └── worker/
│       └── pool.go                            Step 11 — goroutine pool, backpressure, drain
│
├── pkg/
│   └── retry/
│       └── retry.go                           Step 06 — FullJitterBackoff, WithRetry
│
├── cmd/
│   ├── api/
│   │   └── main.go                            Step 13 — API server binary entry point
│   ├── scheduler/
│   │   └── main.go                            Step 13 — Scheduler binary entry point
│   └── worker/
│       └── main.go                            Step 13 — Worker binary entry point
│
├── docker-compose.yml                         Step 14 — PostgreSQL, Redis, Jaeger, Prometheus, Grafana
├── Makefile                                   Step 15 — build, test, migrate, run shortcuts
│
└── docs/
    └── adr/
        ├── ADR-001-queue-design.md            Why Redis Streams not Redis Lists
        └── ADR-002-leader-election.md         Why PostgreSQL advisory locks not ZooKeeper
```

---

## 4. Every File Explained in One Line

| File | What it does |
|---|---|
| `go.mod` | Declares the module name and all external package dependencies |
| `domain/job.go` | Defines Job struct, all 8 statuses, state machine transitions, priority levels |
| `domain/worker.go` | Defines Worker struct with heartbeat and slot-availability helpers |
| `domain/pipeline.go` | Defines Pipeline, DAGSpec, and the ReadyNodes() topology algorithm |
| `config/config.go` | Reads all `ORION_*` environment variables with sane defaults |
| `store/store.go` | Interface contract for all DB operations — no SQL, no PostgreSQL |
| `store/postgres/db.go` | Implements every store interface method using real PostgreSQL SQL |
| `store/migrations/001.sql` | Creates all 5 tables, 4 partial indexes, and the updated_at trigger |
| `queue/queue.go` | Interface contract for the message queue — no Redis commands |
| `queue/redis/redis_queue.go` | Implements queue with XADD, XREADGROUP, XAUTOCLAIM, XACK |
| `observability/observability.go` | Sets up Prometheus metrics, OpenTelemetry tracing, slog logger |
| `api/handler/job.go` | HTTP handlers for POST /jobs, GET /jobs/{id}, GET /jobs |
| `scheduler/scheduler.go` | Advisory lock leader election + dispatch + retry + orphan loops |
| `worker/pool.go` | Goroutine pool with backpressure channel, executor interface, graceful drain |
| `pkg/retry/retry.go` | FullJitterBackoff math — scatters retries to prevent thundering herd |
| `cmd/api/main.go` | Wires config + logger + handler into an HTTP server with graceful shutdown |
| `cmd/scheduler/main.go` | Wires config + logger + scheduler.Run() with signal handling |
| `cmd/worker/main.go` | Wires config + logger + pool.Start() with signal handling |
| `docker-compose.yml` | Starts PostgreSQL, Redis, Jaeger, Prometheus, Grafana with one command |
| `Makefile` | Shortcuts for build, test, migrate, lint, infra-up, run-* |

---

## 5. Dependency Graph — How Files Import Each Other

```
cmd/api  cmd/scheduler  cmd/worker        ← binary entry points
    │           │              │
    │           │              │
    ▼           ▼              ▼
 handler    scheduler       worker/pool
    │           │              │
    └───────────┼──────────────┘
                │  all depend on:
                ▼
         store (interface)  ←── store/postgres/db.go (implementation)
         queue (interface)  ←── queue/redis/redis_queue.go (implementation)
         domain             ←── no external deps, pure data types
         config             ←── only stdlib (os, strconv, time)
         observability      ←── prometheus, otel
         pkg/retry          ←── only stdlib (math, math/rand, time)
```

**Rule: arrows only point inward.**
- `domain` imports nothing external — it is the base layer
- Interfaces (`store`, `queue`) depend only on `domain` — not on any implementation
- Implementations (`postgres`, `redis`) depend on the interface — not the other way around
- `cmd/` packages are the only ones that see everything and wire it together

This means you can test the scheduler without PostgreSQL — swap in a fake store that implements the interface. This is the entire reason for using interfaces.

---

## 6. How to Execute Phase 1 — Step by Step

Follow these steps in order. Do not skip any.

### Prerequisites

```bash
# Check Go version — must be 1.22+
go version
# go version go1.22.x linux/amd64

# Check Docker is running
docker --version
docker compose version

# Install golang-migrate (for running SQL migrations)
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Verify migrate is on PATH
migrate -version
```

---

### Step A — Clone and set up the module

```bash
# Create your project directory
mkdir -p ~/projects/orion && cd ~/projects/orion

# Initialize git (important — the Makefile reads git tags for VERSION)
git init

# Place all Phase 1 files in the correct structure
# (as described in File Map above)

# Download all dependencies
go mod tidy
```

Expected output from `go mod tidy`:
```
go: downloading github.com/jackc/pgx/v5 v5.5.4
go: downloading github.com/redis/go-redis/v9 v9.5.1
go: downloading github.com/google/uuid v1.6.0
...
```

A `go.sum` file is created — this is the lockfile (do not edit it manually).

---

### Step B — Start the infrastructure

```bash
make infra-up
```

This runs `docker compose up -d` and starts 5 containers:

```
[+] Running 5/5
 ✔ Container orion-postgres    Started
 ✔ Container orion-redis       Started
 ✔ Container orion-jaeger      Started
 ✔ Container orion-prometheus  Started
 ✔ Container orion-grafana     Started
```

Verify all containers are healthy:
```bash
docker compose ps
```

Expected:
```
NAME                STATUS
orion-grafana       running (healthy)
orion-jaeger        running (healthy)
orion-postgres      running (healthy)
orion-prometheus    running
orion-redis         running (healthy)
```

Wait until all show `healthy` before proceeding (usually 10-15 seconds).

---

### Step C — Apply the database migration

```bash
make migrate-up
```

Expected output:
```
1/u 001_initial_schema (13.291ms)
```

Verify the tables were created:
```bash
docker compose exec postgres psql -U orion -d orion -c "\dt"
```

Expected:
```
              List of relations
 Schema |      Name       | Type  | Owner
--------+-----------------+-------+-------
 public | job_executions  | table | orion
 public | jobs            | table | orion
 public | pipeline_jobs   | table | orion
 public | pipelines       | table | orion
 public | workers         | table | orion
(5 rows)
```

Verify the indexes:
```bash
docker compose exec postgres psql -U orion -d orion -c "\di"
```

You should see all 6 indexes including the critical partial indexes:
- `idx_jobs_queued_priority` — WHERE status='queued'
- `idx_jobs_retry_eligible` — WHERE status='failed'
- `idx_jobs_running_worker` — WHERE status='running'

---

### Step D — Build all binaries

```bash
make build
```

Expected output:
```
Building orion-api...
Building orion-scheduler...
Building orion-worker...
```

Verify binaries exist:
```bash
ls -lh build/
```

Expected:
```
-rwxr-xr-x  orion-api        ~12MB
-rwxr-xr-x  orion-scheduler  ~11MB
-rwxr-xr-x  orion-worker     ~11MB
```

Go binaries are self-contained — no runtime dependencies, no shared libraries. You can copy `orion-api` to any Linux machine and run it.

---

### Step E — Run the code quality checks

```bash
make vet
```

Expected: no output (silence = success for `go vet`).

```bash
make test
```

Expected output (Phase 1 has limited tests since no mock store exists yet):
```
ok   github.com/shreeharshshinde/orion/pkg/retry      0.004s
ok   github.com/shreeharshshinde/orion/internal/domain 0.003s
```

The scheduler, worker, and API packages will show `[no test files]` — that is expected. Phase 2 adds integration tests.

---

### Step F — Start all three services

Open **three separate terminal windows** for this step.

#### Terminal 1 — API Server

```bash
make run-api
```

Expected output:
```
time=2024-01-15T10:00:00Z level=INFO service=orion-api env=development msg="API server listening" port=8080
```

If tracing setup fails (Jaeger may take a moment):
```
time=2024-01-15T10:00:00Z level=WARN service=orion-api msg="tracing setup failed, continuing without traces" err="..."
time=2024-01-15T10:00:00Z level=INFO service=orion-api env=development msg="API server listening" port=8080
```

This is fine — the API starts regardless. Tracing is non-critical.

#### Terminal 2 — Scheduler

```bash
make run-scheduler
```

Expected output:
```
time=2024-01-15T10:00:01Z level=INFO service=orion-scheduler env=development msg="starting scheduler" batch_size=50 schedule_interval=2s
time=2024-01-15T10:00:01Z level=INFO service=orion-scheduler env=development msg="acquired scheduler leader lock"
```

The scheduler acquired the PostgreSQL advisory lock and is now the leader. It is running its dispatch loops (every 2s) and orphan sweep (every 30s). Since no jobs exist yet, nothing is dispatched.

#### Terminal 3 — Worker

```bash
make run-worker
```

Expected output:
```
time=2024-01-15T10:00:02Z level=INFO service=orion-worker env=development msg="starting worker pool" worker_id=your-hostname concurrency=10 queues=[orion:queue:high orion:queue:default orion:queue:low]
```

The worker registered itself in PostgreSQL and is now blocking on `XREADGROUP` — waiting for jobs to appear in Redis.

---

### Step G — Verify the full health stack

```bash
# API health check
curl -s http://localhost:8080/healthz
```
Expected: `{"status":"ok"}`

```bash
# Readiness check
curl -s http://localhost:8080/readyz
```
Expected: `{"status":"ready"}`

```bash
# Check worker registered in PostgreSQL
docker compose exec postgres psql -U orion -d orion \
  -c "SELECT id, status, last_heartbeat FROM workers;"
```

Expected:
```
       id        | status |       last_heartbeat
-----------------+--------+----------------------------
 your-hostname   | idle   | 2024-01-15 10:00:02+00
(1 row)
```

The worker is registered and sending heartbeats every 15 seconds. Watch `last_heartbeat` update if you run the query multiple times.

---

### Step H — Submit a test job (Phase 2 required for full execution)

With Phase 2 complete, you would run:

```bash
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-training-run",
    "type": "inline",
    "queue_name": "default",
    "priority": 5,
    "payload": {
      "handler_name": "noop"
    },
    "max_retries": 3,
    "idempotency_key": "test-run-001"
  }' | jq .
```

Expected response (Phase 2+):
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "test-training-run",
  "type": "inline",
  "status": "queued",
  "priority": 5,
  "queue_name": "default",
  "attempt": 0,
  "max_retries": 3,
  "created_at": "2024-01-15T10:00:05Z",
  "updated_at": "2024-01-15T10:00:05Z"
}
```

Then track it:
```bash
curl -s http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000 | jq .status
# "queued" → "scheduled" → "running" → "completed"
```

> **Note:** In Phase 1, this call returns a panic because `store = nil`. This is expected and intentional — Phase 2 fills in the PostgreSQL store.

---

## 7. Expected Output at Every Stage

### What you see when the scheduler is idle (no jobs)

```
-- Every 2 seconds, scheduler runs a cycle but finds nothing:
level=DEBUG msg="no queued jobs to dispatch"
level=DEBUG msg="no retry-eligible jobs"

-- Every 30 seconds:
level=DEBUG msg="orphan reclaim: 0 jobs reclaimed"
```

(Debug logs only visible if `ORION_LOG_LEVEL=debug`)

### What you see when a job is submitted (Phase 2+)

```
-- API Server:
level=INFO msg="job submitted" job_id=550e8400 name=test-training-run status=queued

-- Scheduler (within 2s):
level=INFO msg="dispatched jobs to queue" count=1

-- Worker (within 1s):
level=INFO msg="executing job" job_id=550e8400 job_name=test-training-run attempt=1
level=INFO msg="job completed successfully" job_id=550e8400 duration_ms=241
```

### What you see on graceful shutdown (Ctrl+C)

```
-- Worker terminal (Ctrl+C):
level=INFO msg="received shutdown signal, draining in-flight jobs"
level=INFO msg="draining worker pool"
level=INFO msg="worker pool drained cleanly"

-- Scheduler terminal (Ctrl+C):
level=INFO msg="received shutdown signal"

-- API terminal (Ctrl+C):
level=INFO msg="received shutdown signal"
level=INFO msg="server shut down cleanly"
```

---

## 8. Verify Everything is Working

### Check 1 — PostgreSQL tables exist

```bash
docker compose exec postgres psql -U orion -d orion -c "\dt"
# Expect: 5 tables listed
```

### Check 2 — Redis Streams consumer group exists

```bash
docker compose exec redis redis-cli XINFO GROUPS orion:queue:default
```

Expected:
```
1) "name"
2) "orion-workers"
3) "consumers"
4) (integer) 1
5) "pending"
6) (integer) 0
7) "last-delivered-id"
8) "0-0"
```

The consumer group `orion-workers` was created by the worker on startup.

### Check 3 — Worker heartbeat is updating

```bash
# Run this twice, 20 seconds apart
docker compose exec postgres psql -U orion -d orion \
  -c "SELECT id, last_heartbeat FROM workers;"
# The last_heartbeat timestamp should advance each time
```

### Check 4 — Prometheus is scraping metrics

Open http://localhost:9090/targets in your browser. You should see your services listed (after configuring `deploy/prometheus/prometheus.yml`).

Or query directly:
```bash
curl -s http://localhost:9091/metrics | grep orion_
# You should see metrics like:
# orion_jobs_submitted_total
# orion_queue_depth
# orion_worker_active_jobs
```

### Check 5 — Jaeger UI is accessible

Open http://localhost:16686 in your browser. Select `orion-api` from the service dropdown and click "Find Traces".

### Check 6 — Duplicate idempotency key is rejected

```bash
# Submit twice with the same idempotency_key
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"test","type":"inline","payload":{},"idempotency_key":"unique-key-1"}'

curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"test","type":"inline","payload":{},"idempotency_key":"unique-key-1"}'

# Both calls must return the SAME job_id and HTTP 200 on the second call
# (not a new job, not a 409 error)
```

---

## 9. Key Design Decisions and Why

### Why Redis Streams instead of Redis Lists

| Redis Lists | Redis Streams |
|---|---|
| `LPUSH` / `BRPOP` | `XADD` / `XREADGROUP` |
| If worker crashes: **message lost** | If worker crashes: message stays in PEL |
| No consumer groups | Multiple workers share one stream safely |
| No delivery guarantee | At-least-once delivery via XACK |

Full rationale in `docs/adr/ADR-001-queue-design.md`.

### Why PostgreSQL advisory locks for leader election

| Option | Complexity | Auto-release on crash |
|---|---|---|
| ZooKeeper / etcd | High (new service to run) | Yes |
| Redis SETNX | Medium (TTL renewal needed) | Only if TTL set correctly |
| PostgreSQL advisory lock | Zero (already have Postgres) | **Yes, automatically** |

The advisory lock releases the instant the connection closes — even on crash, kill, OOM. No TTL management, no extra infrastructure.

Full rationale in `docs/adr/ADR-002-leader-election.md`.

### Why CAS (Compare-And-Swap) for state transitions

```sql
-- The ONLY way a job status changes in the entire codebase:
UPDATE jobs
SET status = 'scheduled'
WHERE id = $1 AND status = 'queued'  -- CAS check
RETURNING id;

-- 0 rows returned → someone else already changed it → ErrStateConflict
-- 1 row returned → we won the race → proceed
```

This prevents two schedulers from both scheduling the same job. Atomic at the database level. No distributed locks needed.

### Why backpressure via a buffered channel

```go
jobCh := make(chan *jobTask, cfg.Concurrency)  // capacity = 10
```

When all 10 workers are busy:
- The dequeue goroutine tries to send to `jobCh`
- Send **blocks** — the goroutine sleeps
- The XREADGROUP call is not made again
- Jobs stay safely in Redis (not pulled into memory)
- When a worker finishes, a slot opens, the send unblocks

Without this: unlimited memory growth. Workers pull jobs faster than they can execute them.

### Why full-jitter backoff for retries

```
Problem: 1000 jobs fail at the same moment
Naive fix: retry after 30 seconds
Result: 1000 jobs all retry at the same moment → system collapses again

Full jitter: each job picks random(0, min(cap, base × 2^attempt))
Result: 1000 jobs retry spread across a 30-minute window → smooth recovery
```

---

## 10. What Phase 1 Cannot Do Yet

| Gap | Location | Filled in Phase |
|---|---|---|
| `nil` store placeholder | `cmd/api/main.go`, `cmd/scheduler/main.go`, `cmd/worker/main.go` | **Phase 2** |
| No InlineExecutor | `internal/worker/` | **Phase 4** |
| No KubernetesExecutor | `internal/worker/` | **Phase 5** |
| No handler registry | `cmd/worker/main.go` | **Phase 4** |
| No gRPC streaming | `proto/` | **Phase 7** |
| No pipeline scheduler | `internal/scheduler/` | **Phase 8** |
| No Helm chart | `deploy/helm/` | **Phase 9** |

**Concrete symptom in Phase 1:** If you call `POST /jobs`, the handler calls `h.store.GetJobByIdempotencyKey(...)` and since `h.store` is `nil`, the process panics:

```
panic: runtime error: invalid memory address or nil pointer dereference
```

This is expected. It is not a bug — it is the designed boundary between Phase 1 and Phase 2.

---

## 11. Phase 2 Preview — What We Build Next

Phase 2 has one deliverable: **fill every `nil` placeholder with real PostgreSQL calls.**

**File to create:** `internal/store/postgres/db.go`

The file already exists (725 lines). Here is what it implements:

```go
// The 12 methods the scheduler and worker depend on most:

func (db *DB) CreateJob(ctx, job) (*Job, error)
// INSERT INTO jobs ... ON CONFLICT (idempotency_key) → return existing job

func (db *DB) TransitionJobState(ctx, id, expected, next, opts...) error
// UPDATE jobs SET status=$next WHERE id=$1 AND status=$expected RETURNING id
// → 0 rows = ErrStateConflict

func (db *DB) MarkJobRunning(ctx, id, workerID) error
// CAS: scheduled → running, set worker_id + started_at

func (db *DB) MarkJobCompleted(ctx, id) error
// CAS: running → completed, set completed_at

func (db *DB) MarkJobFailed(ctx, id, errMsg, nextRetryAt) error
// CAS: running → failed, set error_message + next_retry_at + increment attempt

func (db *DB) ListJobs(ctx, filter) ([]*Job, error)
// SELECT with dynamic WHERE clause based on JobFilter

func (db *DB) ReclaimOrphanedJobs(ctx, staleThreshold) (int, error)
// Find running jobs whose worker stopped heartbeating → reset to queued

func (db *DB) RegisterWorker(ctx, worker) error
// INSERT ... ON CONFLICT DO UPDATE SET last_heartbeat=NOW()

func (db *DB) Heartbeat(ctx, workerID) error
// UPDATE workers SET last_heartbeat=NOW() WHERE id=$1

func (db *DB) DeregisterWorker(ctx, workerID) error
// UPDATE workers SET status='offline' WHERE id=$1

func (db *DB) RecordExecution(ctx, exec) error
// INSERT INTO job_executions (append-only, no UPDATE)

func (db *DB) GetExecutions(ctx, jobID) ([]*JobExecution, error)
// SELECT * FROM job_executions WHERE job_id=$1 ORDER BY attempt ASC
```

Once Phase 2 is wired in:

```bash
# cmd/api/main.go — replace nil with:
db, _ := pgxpool.New(ctx, cfg.Database.DSN)
store := postgres.New(db)
jobHandler := handler.NewJobHandler(store, logger)

# cmd/scheduler/main.go — same:
sched := scheduler.New(cfg, db, store, queue, logger)

# cmd/worker/main.go — same:
pool := worker.NewPool(cfg, queue, store, executors, logger)
```

After that: `make infra-up && make migrate-up && make run-api` — and you can submit a real job and watch it execute end-to-end.

**Phase 2 completion criteria:**
```bash
# Submit a job
curl -X POST http://localhost:8080/jobs -d '{...}'
# → 201 {"id": "abc", "status": "queued"}

# 2 seconds later (scheduler dispatch cycle):
curl http://localhost:8080/jobs/abc
# → {"status": "scheduled"}

# Within 1 second (worker picks it up):
# → {"status": "running"}

# After execution:
# → {"status": "completed"}

# Check the audit log:
curl http://localhost:8080/jobs/abc/executions
# → [{"attempt":1,"status":"completed","duration_ms":241}]
```

This is the target for Phase 2.

---

## Quick Reference Card

```
SERVICES
  API Server   → :8080        (HTTP: POST/GET /jobs, /healthz, /readyz)
  Scheduler    → internal     (no HTTP; talks directly to PG + Redis)
  Worker       → internal     (no HTTP; reads from Redis, writes to PG)

INFRASTRUCTURE
  PostgreSQL   → localhost:5432   (orion/orion/orion)
  Redis        → localhost:6379
  Jaeger UI    → http://localhost:16686
  Prometheus   → http://localhost:9090
  Grafana      → http://localhost:3000  (admin/admin)
  Metrics      → http://localhost:9091/metrics (orion-api)

MAKE COMMANDS
  make infra-up      start all Docker services
  make migrate-up    apply all SQL migrations
  make build         compile all 3 binaries to build/
  make run-api       run API server (dev mode)
  make run-scheduler run scheduler (dev mode)
  make run-worker    run worker (dev mode)
  make test          all tests with race detector
  make lint          golangci-lint
  make check         fmt + vet + lint + test

ENV VARS (all have defaults, override as needed)
  ORION_DATABASE_DSN          postgres://orion:orion@localhost:5432/orion
  ORION_REDIS_ADDR            localhost:6379
  ORION_HTTP_PORT             8080
  ORION_WORKER_CONCURRENCY    10
  ORION_SCHEDULER_INTERVAL    2s
  ORION_OTLP_ENDPOINT         http://localhost:4317
  ORION_ENV                   development
  ORION_LOG_LEVEL             info
```