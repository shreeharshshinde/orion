# Orion — Execution Guide

> How to run, test, and debug every phase of the codebase. Read this before asking an AI.

---

## Table of Contents

1. [How the System Works](#how-the-system-works)
2. [Prerequisites](#prerequisites)
3. [Infrastructure Setup](#infrastructure-setup)
4. [Running the Services](#running-the-services)
5. [Phase-by-Phase Execution](#phase-by-phase-execution)
6. [Submitting a Job (End-to-End Test)](#submitting-a-job-end-to-end-test)
7. [Debugging Runbook](#debugging-runbook)
8. [Observability Endpoints](#observability-endpoints)
9. [Environment Variables Reference](#environment-variables-reference)

---

## How the System Works

Three independent processes communicate through PostgreSQL and Redis:

```
Client → API Server → PostgreSQL (jobs table)
                           ↑
                      Scheduler (polls every 2s, dispatches to Redis)
                           ↓
                        Redis Streams (orion:queue:high/default/low)
                           ↓
                      Worker Pool (dequeues, executes, writes result back to PG)
```

**API Server** — accepts job submissions, writes `status=queued` to Postgres, returns `job_id`. Stateless; can run multiple replicas.

**Scheduler** — polls Postgres for `status=queued` jobs, transitions them to `status=scheduled`, pushes them onto the Redis stream. Uses a PostgreSQL advisory lock so only one scheduler instance is active at a time (leader election). Also reclaims orphaned jobs (workers that died mid-execution) and promotes failed jobs for retry.

**Worker Pool** — `XREADGROUP` blocks on Redis streams. When a message arrives, a worker goroutine claims it, transitions the job to `status=running` in Postgres, executes it (inline handler or Kubernetes Job), then writes `status=completed` or `status=failed`. Heartbeats every 15s so the scheduler can detect dead workers.

**Job State Machine:**
```
queued → scheduled → running → completed
                             ↘ failed → retrying → queued (retry loop)
                                      ↘ dead (max retries exceeded)
queued/scheduled → cancelled
```

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.22+ | https://go.dev/dl |
| Docker + Docker Compose | any recent | https://docs.docker.com/get-docker |
| `golang-migrate` | latest | `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest` |

---

## Infrastructure Setup

### Start containers

```bash
make infra-up
```

Starts: PostgreSQL `:5432`, Redis `:6380` (host) → `:6379` (container), Jaeger `:16686`, Prometheus `:9090`, Grafana `:3000`.

> **Note:** Redis is mapped to host port `6380` (not `6379`) because another service may already use `6379`. Inside the Docker network it still runs on `6379`.

### Verify containers are healthy

```bash
docker compose ps
```

All containers should show `Up` or `Up (healthy)`. If Redis shows `Up (health: starting)`, wait 5 seconds and re-check.

### Apply database migrations

```bash
make migrate-up
```

Creates tables: `jobs`, `job_executions`, `workers`, `pipelines`, `pipeline_jobs`.

### Tear down

```bash
make infra-down        # stop containers, keep volumes
docker compose down -v # stop containers AND delete volumes (wipes DB)
```

---

## Running the Services

> Each service must run in its own terminal. They all read from `.env` automatically via the Makefile.

```bash
# Terminal 1
make run-api

# Terminal 2
make run-scheduler

# Terminal 3
make run-worker
```

**Expected startup logs:**

API:
```
level=INFO msg="connected to postgres"
level=INFO msg="API server listening" port=8080
```

Scheduler:
```
level=INFO msg="connected to postgres"
level=INFO msg="connected to redis"
level=INFO msg="acquired scheduler leader lock"
level=INFO msg="starting scheduler"
```

Worker:
```
level=INFO msg="connected to postgres"
level=INFO msg="connected to redis"
level=INFO msg="inline executor ready" registered_handlers="[always_fail echo noop slow]"
level=WARN msg="kubernetes client unavailable — k8s_job type will not be served"  ← normal in local dev
level=INFO msg="starting worker pool"
```

---

## Phase-by-Phase Execution

### Phase 1 — Domain types + retry package

**What it is:** Pure Go structs and logic. No infrastructure needed.

**Files:**
- `internal/domain/job.go` — `Job`, `JobStatus`, state machine (`CanTransitionTo`, `IsTerminal`, `IsRetryable`)
- `internal/domain/worker.go` — `Worker`, `IsAlive`, `AvailableSlots`
- `internal/domain/pipeline.go` — `Pipeline`, `DAGSpec`, `ReadyNodes`
- `pkg/retry/retry.go` — `FullJitterBackoff`, `EqualJitterBackoff`, `WithRetry`

**Run tests:**
```bash
go test ./internal/domain/... ./pkg/retry/... -v
```

Expected: all PASS, no infrastructure required.

---

### Phase 2 — PostgreSQL store

**What it is:** SQL implementation of the `store.Store` interface.

**Files:**
- `internal/store/store.go` — interface definitions only (no SQL here)
- `internal/store/postgres/db.go` — all job/worker/execution SQL
- `internal/store/postgres/pipeline.go` — pipeline SQL
- `internal/store/migrations/` — SQL migration files

**Run unit tests (no DB needed):**
```bash
go test ./internal/store/postgres/... -run Unit -v
```

**Run integration tests (DB required):**
```bash
make infra-up && make migrate-up
go test ./internal/store/postgres/... -run Integration -v -tags=integration
```

---

### Phase 3 — Redis queue + API handlers

**What it is:** Redis Streams consumer group implementation and HTTP handlers.

**Files:**
- `internal/queue/redis/redis_queue.go` — `XADD`, `XREADGROUP`, `XACK`, PEL sweeper
- `internal/api/handler/job.go` — `POST /jobs`, `GET /jobs`, `GET /jobs/{id}`
- `internal/api/handler/pipeline.go` — `POST /pipelines`, `GET /pipelines/{id}`
- `internal/api/handler/middleware.go` — tracing + metrics middleware

**Run handler tests (no infra needed — uses mocks):**
```bash
go test ./internal/api/handler/... -v
```

---

### Phase 4 — Worker pool + inline executor

**What it is:** Bounded goroutine pool that dequeues from Redis and executes jobs.

**Files:**
- `internal/worker/pool.go` — dequeue loop, worker goroutines, heartbeat, graceful drain
- `internal/worker/inline.go` — `InlineExecutor` (runs registered Go functions)
- `internal/worker/handlers/handlers.go` — built-in handlers: `noop`, `echo`, `always_fail`, `slow`
- `internal/worker/k8s/executor.go` — `KubernetesExecutor` (skipped in local dev)

**Run tests:**
```bash
go test ./internal/worker/... -v
```

---

### Phase 5 — Scheduler + pipeline advancement

**What it is:** Dispatch loop with leader election and DAG pipeline advancement.

**Files:**
- `internal/scheduler/scheduler.go` — dispatch loop, orphan reclaimer, retry promoter, leader election via `pg_try_advisory_lock`
- `internal/pipeline/advancement.go` — reads DAG, creates jobs for ready nodes, detects completion/failure

**Run tests:**
```bash
go test ./internal/scheduler/... ./internal/pipeline/... -v
```

---

### Phase 6 — Observability

**What it is:** Prometheus metrics, OpenTelemetry traces, structured logging wired into every layer.

**Files:**
- `internal/observability/observability.go` — `NewLogger`, `NewMetrics`, `SetupTracing`, `MetricsServer`

**Metrics exposed (`:9091/metrics`):**
| Metric | Type | Description |
|--------|------|-------------|
| `orion_jobs_submitted_total` | Counter | Jobs submitted via API |
| `orion_job_duration_seconds` | Histogram | End-to-end job execution time |
| `orion_queue_depth` | Gauge | Messages in each Redis stream |
| `orion_worker_active_jobs` | Gauge | Jobs currently executing |
| `orion_scheduler_cycle_duration_seconds` | Histogram | Scheduler dispatch loop latency |

**View traces:** http://localhost:16686 (Jaeger UI)

**View dashboards:** http://localhost:3000 (Grafana, login: `admin`/`admin`)

---

## Submitting a Job (End-to-End Test)

With all three services running:

### Inline job (runs inside worker process)

```bash
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-echo",
    "type": "inline",
    "queue_name": "default",
    "priority": 5,
    "max_retries": 2,
    "idempotency_key": "test-001",
    "payload": {
      "handler_name": "echo",
      "args": {"message": "hello orion"}
    }
  }' | python3 -m json.tool
```

### Check job status

```bash
# Replace <job_id> with the id from the response above
curl -s http://localhost:8080/jobs/<job_id> | python3 -m json.tool
```

### Expected status progression

Watch the scheduler and worker logs. The job should move through:
`queued` → `scheduled` → `running` → `completed`

This takes ~2–4 seconds (scheduler polls every 2s).

### List all jobs

```bash
curl -s http://localhost:8080/jobs | python3 -m json.tool
```

### Health check

```bash
curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"} — checks DB connectivity
```

---

## Debugging Runbook

### Service won't start — `postgres ping failed`

**Cause:** PostgreSQL container not running, or host port forwarding broken.

**Fix:**
```bash
# 1. Check containers
docker compose ps

# 2. If postgres is not running
make infra-up

# 3. If postgres is running but unreachable (port forwarding issue)
# Get the container IP directly
docker inspect orion-postgres --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'

# 4. Update .env with the container IP
echo "ORION_DATABASE_DSN=postgres://orion:orion@<container-ip>:5432/orion?sslmode=disable" > .env
```

---

### Service won't start — `failed to connect to redis`

**Cause:** Redis container not running, port conflict, or not joined to Docker network.

**Fix:**
```bash
# 1. Check redis
docker compose ps | grep redis

# 2. If redis is running but has no network IP
docker inspect orion-redis --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'
# If this returns empty, redis isn't on the compose network

# 3. Force recreate
docker compose up -d --force-recreate redis
sleep 3
docker inspect orion-redis --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'

# 4. If port 6379 is already in use by another container
docker ps --filter publish=6379   # find the conflicting container
# The docker-compose.yml maps redis to host port 6380 to avoid this

# 5. Update .env
echo "ORION_REDIS_ADDR=<container-ip>:6379" >> .env
```

---

### `migrate-up` fails — `connection refused`

**Cause:** `migrate` resolves `localhost` to `[::1]` (IPv6) which may not be forwarded.

**Fix:**
```bash
# Use container IP directly
POSTGRES_IP=$(docker inspect orion-postgres --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
migrate -database "postgres://orion:orion@${POSTGRES_IP}:5432/orion?sslmode=disable" \
        -path ./internal/store/migrations up
```

---

### Jobs stuck in `queued` — never become `scheduled`

**Cause:** Scheduler not running, or scheduler didn't acquire the leader lock.

**Check:**
```bash
# Is the scheduler running?
# Look for this in scheduler logs:
# level=INFO msg="acquired scheduler leader lock"

# If you see "contending for leader lock" but never "acquired":
# Another scheduler instance holds the lock. Kill it or wait for its session to expire.

# Check the advisory lock in Postgres
docker exec orion-postgres psql -U orion -c \
  "SELECT pid, granted FROM pg_locks WHERE locktype='advisory';"
```

---

### Jobs stuck in `scheduled` — never become `running`

**Cause:** Worker not running, or Redis stream has no consumer group.

**Check:**
```bash
# Is the worker running and connected to Redis?
# Look for in worker logs:
# level=INFO msg="starting worker pool"

# Check the Redis stream directly
REDIS_IP=$(docker inspect orion-redis --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
docker exec orion-redis redis-cli XLEN orion:queue:default
docker exec orion-redis redis-cli XINFO GROUPS orion:queue:default
```

---

### `metrics server error: bind: address already in use` on port 9091

**Cause:** Two services started in the same terminal sequentially — the first one's metrics server port wasn't released yet.

**Fix:** Run each service in a separate terminal. This is not an error when running them properly.

---

### Worker logs show `kubernetes client unavailable`

This is **expected and normal** in local development. The Kubernetes executor is skipped. Only `inline` type jobs work locally. `k8s_job` type jobs require a kubeconfig at `~/.kube/config`.

---

### Traces not appearing in Jaeger

**Cause:** OTLP endpoint misconfigured. The gRPC endpoint must NOT have `http://` prefix.

**Fix:**
```bash
# Correct format in .env:
ORION_OTLP_ENDPOINT=172.18.0.5:4317   # ← no http://

# Wrong:
ORION_OTLP_ENDPOINT=http://172.18.0.5:4317  # ← causes "too many colons" error
```

---

### Tests fail with `connection refused` (integration tests)

Integration tests require running infrastructure. Unit tests do not.

```bash
# Unit tests only (no infra needed)
go test ./internal/domain/... ./pkg/retry/... ./internal/api/handler/... -v

# Integration tests (infra required)
make infra-up && make migrate-up
go test ./internal/store/postgres/... -v -tags=integration
```

---

### How to reset everything

```bash
# Stop all containers and wipe all data
docker compose down -v

# Restart fresh
make infra-up
make migrate-up
```

---

### General log reading tips

All services emit structured JSON logs. Key fields:

| Field | Meaning |
|-------|---------|
| `msg` | What happened |
| `err` | Error detail — always read this |
| `job_id` | Which job |
| `worker_id` | Which worker instance |
| `trace_id` | Correlate with Jaeger |

Filter logs for a specific job:
```bash
make run-worker 2>&1 | grep <job_id>
```

Increase log verbosity:
```bash
ORION_LOG_LEVEL=debug make run-api
```

---

## Observability Endpoints

| Service | URL | Notes |
|---------|-----|-------|
| API | http://localhost:8080 | Main API |
| API health | http://localhost:8080/healthz | Always 200 if process is up |
| API readiness | http://localhost:8080/readyz | 200 only if DB is reachable |
| Metrics | http://localhost:9091/metrics | Prometheus scrape target |
| Prometheus | http://localhost:9090 | Query metrics directly |
| Grafana | http://localhost:3000 | Dashboards (admin/admin) |
| Jaeger | http://localhost:16686 | Distributed traces |

---

## Environment Variables Reference

All variables have defaults. Override by editing `.env` in the project root.

| Variable | Default | Description |
|----------|---------|-------------|
| `ORION_DATABASE_DSN` | `postgres://orion:orion@localhost:5432/orion?sslmode=disable` | Full Postgres DSN |
| `ORION_REDIS_ADDR` | `localhost:6379` | Redis address |
| `ORION_OTLP_ENDPOINT` | `localhost:4317` | Jaeger OTLP gRPC endpoint (no `http://`) |
| `ORION_HTTP_PORT` | `8080` | API server port |
| `ORION_METRICS_PORT` | `9091` | Prometheus metrics port |
| `ORION_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ORION_ENV` | `development` | Environment label in logs |
| `ORION_WORKER_CONCURRENCY` | `10` | Max parallel jobs per worker |
| `ORION_WORKER_ID` | hostname | Unique worker identifier |
| `ORION_SCHEDULER_INTERVAL` | `2s` | How often scheduler polls for queued jobs |
| `ORION_SCHEDULER_BATCH_SIZE` | `50` | Max jobs dispatched per scheduler tick |

> The `.env` file in the project root is sourced automatically by `make run-*` targets. It overrides the defaults above with container IPs for local Docker networking.
