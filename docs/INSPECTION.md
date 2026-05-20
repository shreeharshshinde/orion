# Orion — Codebase Inspection Report

**Date:** 2026-05-09
**Scope:** Full codebase audit of Phases 1–9
**Purpose:** Identify what was built, what is missing, potential bugs, and the path to production

---

## Table of Contents

1. [What Was Built — Phase by Phase](#1-what-was-built--phase-by-phase)
2. [Architecture Assessment](#2-architecture-assessment)
3. [What Is Missing / Incomplete](#3-what-is-missing--incomplete)
4. [Potential Bugs and Risk Areas](#4-potential-bugs-and-risk-areas)
5. [Test Coverage Assessment](#5-test-coverage-assessment)
6. [Security Gaps](#6-security-gaps)
7. [Production Readiness Gaps](#7-production-readiness-gaps)
8. [Future Roadmap](#8-future-roadmap)
9. [Priority Action List](#9-priority-action-list)

---

## 1. What Was Built — Phase by Phase

### Phase 1 — Domain Types, Interfaces, Project Structure ✅

**Files:** `internal/domain/`, `internal/store/store.go`, `internal/queue/queue.go`, `pkg/retry/`

- `domain.Job` with full state machine (`queued → scheduled → running → completed/failed/dead/cancelled`)
- `domain.JobPayload` supporting both inline handlers and Kubernetes specs
- `domain.Pipeline` and `domain.DAGSpec` with `ReadyNodes()` graph traversal
- `domain.Worker` with heartbeat TTL check
- `store.Store` composite interface: `JobStore`, `ExecutionStore`, `WorkerStore`, `PipelineStore`, `QueueConfigStore`
- `queue.Queue` interface with `Enqueue`, `Dequeue`, `Len`, `Dead`, `Flush`, `Close`
- `store.TransitionOption` functional options pattern for CAS updates
- Typed sentinel errors: `ErrNotFound`, `ErrStateConflict`, `ErrDuplicate` with `errors.Is()` support
- `pkg/retry` full-jitter exponential backoff: `random(0, min(cap, base × 2^attempt))`
- `config.Config` loaded entirely from environment variables with safe defaults

**Quality:** Excellent. Zero external dependencies in domain layer. Interfaces are clean and testable.

---

### Phase 2 — PostgreSQL Store Implementation ✅

**Files:** `internal/store/postgres/db.go`, `internal/store/postgres/pipeline.go`, `internal/store/migrations/`

- `postgres.DB` implements all of `store.Store`
- `CreateJob` with three-case idempotency handling (pre-check, INSERT, race-condition re-fetch)
- `TransitionJobState` CAS via `UPDATE WHERE status = expected RETURNING id` — returns `ErrStateConflict` on zero rows
- Dynamic SET clause built from `TransitionOption` functions — single round-trip for all field updates
- `ClaimPendingJobs` using `SELECT FOR UPDATE SKIP LOCKED` for concurrent worker safety
- `ReclaimOrphanedJobs` CTE that finds running jobs whose worker missed heartbeats
- `RecordExecution` append-only with `ON CONFLICT (job_id, attempt) DO NOTHING` idempotency
- `RegisterWorker` with `ON CONFLICT DO UPDATE` for restart safety
- `ListQueueConfigs`, `GetQueueConfig`, `UpsertQueueConfig` for Phase 8 live config
- Migration 001: full schema with partial indexes for scheduler hot paths
- Migration 002: pipeline indexes
- Migration 003: `queue_config` table

**Quality:** Very solid. CAS pattern is correct. Nullable column scanning is careful. `nullableString` helper prevents empty-string vs NULL confusion on the unique `idempotency_key` column.

---

### Phase 3 — Worker Pool + Inline Executor ✅

**Files:** `internal/worker/pool.go`, `internal/worker/inline.go`, `internal/worker/handlers/`

- `Pool` with bounded goroutine model: N dequeue goroutines → buffered `jobCh` → N worker goroutines
- Backpressure: `jobCh` capacity = `Concurrency`; dequeue blocks when all workers are busy
- `executeJob` handles: `MarkJobRunning` → `RecordExecution` → `Execute` → `MarkJobCompleted/Failed` → `AckFn`
- Graceful shutdown: `drain()` closes `jobCh`, waits for `wg.Wait()` with `ShutdownTimeout`
- `heartbeatLoop` sends heartbeats every 15s, deregisters worker on clean exit
- `InlineExecutor` with handler registry (`map[string]HandlerFunc`)
- `handlers/handlers.go` with example ML handlers (data validation, model training stubs)
- `nextRetryTime` uses `pkg/retry.FullJitterBackoff`

**Quality:** Good. The drain logic in `runWorker` on `ctx.Done()` correctly drains the channel before exiting. One concern noted in bugs section.

---

### Phase 4 — Kubernetes Executor ✅

**Files:** `internal/worker/k8s/executor.go`, `internal/worker/k8s/spec.go`, `deploy/k8s/rbac.yaml`

- `KubernetesExecutor` implements `worker.Executor` for `k8s_job` type
- `buildK8sJob` translates `domain.KubernetesSpec` → `batchv1.Job` with resource requests/limits
- GPU support via `nvidia.com/gpu` resource requests
- Watch-first with poll fallback: `Watch` → on channel close → `pollForCompletion`
- Context cancellation cleanup: deletes the K8s Job with `PropagationPolicy=Foreground` to avoid zombie GPU pods
- `BuildK8sClient` supports both in-cluster (ServiceAccount) and out-of-cluster (kubeconfig) modes
- RBAC manifest: `ServiceAccount`, `Role`, `RoleBinding` for `orion-jobs` namespace
- TTL cleanup via `TTLSecondsAfterFinished` on the K8s Job spec

**Quality:** Good. The watch/poll fallback is robust. GPU cleanup on cancellation is a nice production detail.

---

### Phase 5 — DAG Pipeline Support ✅

**Files:** `internal/pipeline/advancement.go`, `internal/store/postgres/pipeline.go`

- `Advancer` called by scheduler on every 2s tick via `AdvanceAll()`
- Per-pipeline `advanceOne`: load node statuses → detect dead → pending→running → `ReadyNodes` → create jobs → detect completion
- Idempotency guard: `createdNodes` map prevents duplicate `CreateJob` calls on rapid successive ticks
- Cascade cancellation: `findDownstream` BFS logs which nodes will never start when an upstream node dies
- `GetPipelineJobs` single JOIN query (no N+1) for the advancement algorithm
- `AddPipelineJob` with `ON CONFLICT DO NOTHING` for crash-safe node linking
- `ListPipelinesByStatus` with separate queries per status (partial index friendly)
- Pipeline jobs named `"pipeline-name/node-id"` for easy correlation

**Quality:** Solid algorithm. The idempotency guard is correct. One gap: cascade cancellation only logs — it does not create `cancelled` job records for downstream nodes (noted in missing section).

---

### Phase 6 — Observability Instrumentation ✅

**Files:** `internal/observability/observability.go`, `internal/api/handler/middleware.go`, `deploy/grafana/`, `deploy/prometheus/`

- `observability.Metrics` with 20+ Prometheus metrics across all subsystems
- `SetupTracing` with OTLP/gRPC exporter to Jaeger, `TraceIDRatioBased` sampler
- `NewLogger` with `slog` JSON (prod/staging) or text (dev) output
- `MetricsMiddleware` wraps entire HTTP mux — uses `r.Pattern` (Go 1.22) for low-cardinality route labels
- `TracingMiddleware` propagates W3C TraceContext headers
- OTel spans on: scheduler dispatch cycle, per-job dispatch, worker job execution, pipeline advancement
- `StartQueueDepthPoller` polls Redis XLEN every 5s for accurate queue depth gauge
- `ReclaimStalePending` sweeps PEL every 30s via `XAUTOCLAIM`
- Grafana dashboard JSON provisioned at startup
- Prometheus scrape config covers all three services

**Quality:** Comprehensive. The `r.Pattern` label choice is correct — avoids cardinality explosion from job IDs in URLs. All metric calls are nil-guarded for test safety.

---

### Phase 7 — gRPC Streaming API ✅

**Files:** `internal/api/grpc/server.go`, `internal/api/grpc/broadcaster.go`, `internal/api/grpc/instrumented_store.go`, `proto/orion/v1/`

- `.proto` definition with `JobService`: `SubmitJob`, `GetJob`, `ListJobs`, `WatchJob`, `CancelJob`
- Generated `jobs.pb.go` and `jobs_grpc.pb.go`
- `Broadcaster` fan-out: publishes job state change events to all active `WatchJob` streams
- `InstrumentedStore` wraps `postgres.DB` and calls `broadcaster.Publish()` on every state transition
- `grpcserver.Server` implements all RPC methods
- gRPC server wired in `cmd/api/main.go` alongside HTTP server on separate port (9090)
- Graceful shutdown: `grpcSrv.GracefulStop()` before HTTP shutdown

**Quality:** Good design. The broadcaster pattern is clean. One gap: `InstrumentedStore` only wraps a subset of store methods — not all transitions go through it (noted in bugs section).

---

### Phase 8 — Fair Scheduling + Rate Limiting ✅

**Files:** `internal/scheduler/fairqueue.go`, `internal/scheduler/ratelimiter.go`, `internal/api/handler/queue.go`

- `QueueRateLimiter` token bucket per queue using `golang.org/x/time/rate`
- `FairQueue.FetchReadyJobs` issues one `ListJobs` query per queue with weight-proportional limits
- `ComputeAllocations` distributes batch slots proportionally to queue weights
- `QueueHandler`: `GET /queues`, `GET /queues/{name}`, `PUT /queues/{name}`, `GET /queues/{name}/stats`
- Live config reload: `PUT /queues/{name}` upserts to `queue_config` table; scheduler reads it on next tick
- `queue_config` migration (003) with default rows for high/default/low queues
- Phase 8 Prometheus metrics: `QueueRateLimited`, `QueueConcurrentJobs`, `QueueConcurrencyLimit`, `QueueDispatchWeight`

**Quality:** Good. The weighted fair scheduler prevents queue starvation. The live-reload path (DB → scheduler tick) has up to 2s latency which is acceptable.

---

### Phase 9 — Helm Chart + Production Hardening ✅

**Files:** `deploy/helm/`, `deploy/docker/`, `deploy/k8s/migrate-job.yaml`

- Three Dockerfiles (api, scheduler, worker) using multi-stage builds with `gcr.io/distroless/static`
- Helm chart with templates: `api-deployment.yaml`, `scheduler-deployment.yaml`, `worker-deployment.yaml`, `hpa.yaml`, `rbac.yaml`, `configmap.yaml`, `secret.yaml`, `servicemonitor.yaml`
- HPA for worker: scales on CPU utilization (configurable target queue depth per worker)
- `ServiceMonitor` for Prometheus Operator integration
- `migrate-job.yaml` Kubernetes Job for running migrations before service startup
- `values.yaml` with full production defaults: 3 API replicas, 3 scheduler replicas (1 active via lock), 5 worker replicas
- Resource requests/limits on all deployments
- `imagePullSecrets` support

**Quality:** Solid foundation. Several gaps noted below (no Ingress, no NetworkPolicy, no PodDisruptionBudget).

---

## 2. Architecture Assessment

### What Works Well

**State machine integrity.** Every status transition goes through `TransitionJobState` with a CAS guard. There is no code path that sets `job.Status = X` directly — it always goes through the store. This is the single most important correctness property in the system.

**Interface-driven design.** `store.Store` and `queue.Queue` are pure interfaces. The scheduler, worker, and API handler never import `postgres` or `redis` packages directly. Swapping the backing store or queue broker requires changing only the wiring in `cmd/`.

**Backpressure model.** The buffered `jobCh` (capacity = Concurrency) is the correct design. Jobs stay in Redis PEL until a worker slot opens. No in-memory job accumulation, no OOM risk under load spikes.

**Idempotency everywhere.** `CreateJob` handles three concurrent-submission cases. `RecordExecution` has `ON CONFLICT DO NOTHING`. `AddPipelineJob` has `ON CONFLICT DO NOTHING`. `RegisterWorker` has `ON CONFLICT DO UPDATE`. The system can be safely retried at every layer.

**Observability depth.** Spans on every layer (HTTP → scheduler → worker → pipeline), Prometheus metrics with correct cardinality choices, structured slog with trace/span IDs. This is production-grade observability.

### Structural Concerns

**Advisory lock is session-scoped, not connection-scoped.** `pg_try_advisory_lock` is held by the PostgreSQL *session*. With `pgxpool`, each `QueryRow` call may use a different connection from the pool. If the connection that acquired the lock is returned to the pool and reused for a different query, the lock is still held — but if that connection is closed (idle timeout, pool shrink), the lock is silently released. The scheduler should acquire the lock on a *dedicated* connection held for the lifetime of the leader tenure, not via the shared pool.

**`promoteRetryableJobs` uses `ListJobs` not a dedicated query.** The current implementation fetches all `status=failed` jobs and filters `next_retry_at` in Go. This works but is inefficient at scale — the partial index `idx_jobs_retry_eligible` exists in the schema but is not used. A dedicated store method `ListRetryableJobs(ctx, limit)` with `WHERE status='failed' AND next_retry_at <= NOW()` would use the index.

**`dequeueLoop` goroutines are not tracked by `wg`.** The goroutines launched in `dequeueLoop` are not added to `p.wg`. On shutdown, `drain()` closes `jobCh` and waits for worker goroutines, but the dequeue goroutines may still be blocked in `XREADGROUP` for up to 5 seconds after `jobCh` is closed. This is a minor leak but means shutdown takes longer than necessary.

**Pipeline queue is hardcoded to `"default"`.** `queueNameFromPayload` always returns `"default"` regardless of the node's job template. Pipeline jobs cannot be routed to `high` or `low` queues. This is a known limitation but should be documented as a TODO.

---

## 3. What Is Missing / Incomplete

### Critical Missing Pieces

**1. `DELETE /jobs/{id}` and `POST /jobs/{id}/cancel` HTTP endpoints** — ✅ RESOLVED
`store.DeleteJob` is implemented in postgres but there is no HTTP handler for it. `JobStatusCancelled` exists in the domain but there is no API endpoint to trigger a cancellation. The `CancelJob` RPC exists in gRPC but the HTTP REST API has no equivalent.

> **Solution:** `POST /jobs/{id}/cancel` was already present but undocumented. Added `DeleteJob` handler to `internal/api/handler/job.go` — guards against deleting `running`/`scheduled` jobs (409 Conflict), returns 204 No Content on success. Both routes registered in `cmd/api/main.go`. Six unit tests added in `internal/api/handler/job_test.go`.

**2. Scheduled job promotion (sorted set sweeper)** — ✅ RESOLVED
`Enqueue` correctly writes future-scheduled jobs to `orion:queue:scheduled` (a Redis sorted set). However, there is no goroutine that reads from this sorted set and moves jobs to the appropriate stream when `scheduled_at` arrives. Jobs submitted with a future `scheduled_at` will sit in the sorted set forever and never execute.

> **Solution:** Implemented `StartScheduledSweeper` / `sweepScheduled` on `RedisQueue`. The sweeper ticks every second and uses an atomic Lua script (`zpopByScore`) that combines `ZRANGEBYSCORE` + `ZREM` in a single Redis round-trip, preventing double-promotion if two scheduler instances were ever to run the sweeper concurrently. On `XAdd` failure the member is re-inserted into the sorted set so it is retried next tick. `StartScheduledSweeper` was added to the `Queue` interface and is started inside `runAsLeader` in `scheduler.go` — ensuring only the leader scheduler runs it. The unconditional `go queue.StartScheduledSweeper(ctx)` call was removed from `cmd/scheduler/main.go`. Five unit tests added in `internal/queue/redis/sweep_test.go` using `miniredis`: promotes due jobs, ignores future jobs, no duplicates on double-sweep, routes to correct stream per queue name, handles empty set.

**3. `GET /jobs/{id}/executions` response body** — ✅ RESOLVED
`GetExecutions` is wired in `cmd/api/main.go` and implemented in the store, but the handler in `internal/api/handler/job.go` needs to be verified — the handler file was not fully read. This endpoint is critical for debugging failed jobs.

> **Solution:** Verified fully implemented. Handler returns `{"job_id": ..., "executions": [...], "count": N}`. Returns 404 for unknown jobs (not an ambiguous empty list). Store scans all 11 columns: `id`, `job_id`, `attempt`, `worker_id`, `status`, `started_at`, `finished_at`, `exit_code`, `logs_ref`, `error`, `created_at`. Four unit tests in `internal/api/handler/job_test.go` cover: found with executions (200), job not found (404), invalid UUID (400), empty history (200).

**4. Cascade cancellation creates no job records** — ✅ RESOLVED  
When a pipeline node reaches `dead` status, `logCascadeCancellation` only logs which downstream nodes will not start. It does not create `cancelled` job records for those nodes. The `GET /pipelines/{id}/jobs` endpoint will show those nodes as simply absent rather than explicitly cancelled, making it hard to understand why a pipeline failed.

> **Solution:** Replaced `logCascadeCancellation` with `createCancelledDownstreamJobs` in `internal/pipeline/advancement.go`. For each downstream node that hasn't started, it calls `CreateJob` with `status=cancelled` and links it via `AddPipelineJob`. Nodes that already have a job (running or completed before the failure) are skipped. On `CreateJob` or `AddPipelineJob` failure the error is logged and the loop continues — a partial cancel is better than blocking the pipeline failure transition. One test added: `TestAdvanceAll_CascadeCancel_CreatesJobRecordsForDownstreamNodes` verifies that a 4-node linear pipeline with `train` dead produces cancelled job records for `evaluate` and `deploy` in `pipeline_jobs`.

**5. Worker `Queues` config not wired from `config.WorkerPoolConfig`**
In `config.go`, `WorkerPoolConfig.Queues` is defined as `[]string` but has no default value and no `ORION_WORKER_QUEUES` env var parsing. The worker entrypoint (`cmd/worker/main.go`) must manually set this. If it is left empty, the worker dequeues from no queues and processes nothing silently.

**6. `InstrumentedStore` does not wrap all state transitions**
`grpc.InstrumentedStore` wraps `MarkJobRunning`, `MarkJobCompleted`, `MarkJobFailed` — but not `TransitionJobState` directly. The scheduler calls `TransitionJobState` (queued→scheduled, failed→retrying, retrying→queued) and those transitions are never broadcast to `WatchJob` gRPC streams. Clients watching a job will miss the `scheduled` and `retrying` state transitions.

### Missing Operational Features

**7. Dead-letter queue replay API**
Jobs in `orion:queue:dead` (Redis stream) and `status=dead` (PostgreSQL) have no API to replay them. There is no `POST /jobs/{id}/replay` endpoint. Dead jobs are visible in Grafana but cannot be requeued without direct database manipulation.

**8. `GET /workers` endpoint**
`store.ListActiveWorkers` is implemented but there is no HTTP handler exposing it. Operators cannot see which workers are alive, their queue assignments, or their active job counts without querying the database directly.

**9. `POST /jobs/{id}/cancel` for running jobs**
Cancelling a running job requires signalling the worker that is executing it. The current architecture has no mechanism for this — the worker has no way to receive a cancellation signal for a specific job mid-execution. This requires either a Redis pub/sub channel or a context cancellation registry in the worker pool.

**10. Helm chart missing Ingress template**
`deploy/helm/templates/` has no `ingress.yaml`. The API service is `ClusterIP` only. Exposing it externally requires manually creating an Ingress or LoadBalancer service outside the chart.

**11. No `NetworkPolicy` manifests**
There are no Kubernetes `NetworkPolicy` resources. In a production cluster, the worker pods should only be able to reach PostgreSQL, Redis, and the Kubernetes API server — not arbitrary cluster services.

**12. No `PodDisruptionBudget`**
Rolling updates can take all API or scheduler pods down simultaneously. A PDB ensuring at least 1 API pod and 1 scheduler pod remain available during updates is missing.

**13. Migration is not idempotent on re-run**
The `migrate-job.yaml` Kubernetes Job runs `golang-migrate up`. If the Job is re-applied (e.g., during a Helm upgrade with no schema changes), it will attempt to run and succeed (migrations are idempotent by design), but the Job will show as `Completed` from a previous run. The Job should use `ttlSecondsAfterFinished` to clean itself up.

**14. No `CANCEL` endpoint for pipelines**
There is no `DELETE /pipelines/{id}` or `POST /pipelines/{id}/cancel`. A running pipeline cannot be stopped via the API.

**15. `pkg/retry` not exported with a `Retry` function**
`pkg/retry` exports `FullJitterBackoff` (the delay calculator) but not a `Retry(ctx, fn, opts)` wrapper. Callers that want to retry an operation with backoff must implement the loop themselves.

---

## 4. Potential Bugs and Risk Areas

### Bug 1 — Advisory Lock on Pooled Connection (HIGH RISK)

**Location:** `internal/scheduler/scheduler.go` — `tryAcquireLeaderLock`

```go
err := s.db.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&held)
```

`s.db` is a `*pgxpool.Pool`. `QueryRow` acquires a connection, runs the query, and returns the connection to the pool. The advisory lock is held by that connection's PostgreSQL session. If pgxpool later closes that connection (due to `MaxConnIdleTime` or `MaxConnLifetime`), the lock is silently released — and another scheduler instance can acquire it, resulting in **two active schedulers simultaneously dispatching the same jobs**.

**Fix:** Acquire a dedicated `pgxpool.Conn` at the start of `runAsLeader`, hold it for the entire leader tenure, and release it (returning the connection to the pool) only when leadership ends. The lock is then tied to that specific connection's lifetime.

```go
conn, _ := s.db.Acquire(ctx)
defer conn.Release()
conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&held)
// use conn for all lock operations
```

---

### Bug 2 — Double-Execution Risk on Worker Restart During `MarkJobRunning` (MEDIUM)

**Location:** `internal/worker/pool.go` — `executeJob`

The sequence is:
1. Worker dequeues job from Redis (message in PEL)
2. Worker calls `MarkJobRunning` (scheduled → running in PG)
3. Worker crashes before calling `ackFn(nil)`

On restart, the PEL reclaimer (`ReclaimStalePending`) will redeliver the message. The worker will call `MarkJobRunning` again, which will fail with `ErrStateConflict` (job is already `running`). The worker then calls `ackFn(err)` which is a NACK — the message stays in PEL indefinitely.

The orphan reclaimer will eventually reset the job to `queued` (after 90s), but the message is still in the PEL. When the orphan reclaimer fires, the job goes back to `queued`, the scheduler re-enqueues it to Redis, and now there are **two messages for the same job** in the stream.

**Fix:** In `executeJob`, after `MarkJobRunning` returns `ErrStateConflict`, check the current job status. If it is `running` with a different `worker_id`, NACK and skip. If it is `running` with *this* worker's ID (restart scenario), proceed with execution.

---

### Bug 3 — `dequeueLoop` Goroutine Leak on Shutdown (LOW-MEDIUM)

**Location:** `internal/worker/pool.go` — `dequeueLoop`

```go
func (p *Pool) dequeueLoop(ctx context.Context) {
    for _, queueName := range p.cfg.QueueNames {
        go func(qName string) { ... }(queueName)
    }
}
```

These goroutines are not added to `p.wg`. When `ctx` is cancelled, they exit their loop — but only after the current `XREADGROUP` call returns (up to 5s block timeout). Meanwhile, `drain()` has already closed `jobCh`. If a dequeue goroutine receives a job after `jobCh` is closed, the send `p.jobCh <- task` will panic.

The `select` in the goroutine does check `ctx.Done()` before sending, but there is a race: `ctx` can be cancelled between the `XREADGROUP` return and the `select` check.

**Fix:** Add dequeue goroutines to `p.wg`, or use a separate `sync.WaitGroup` for them. Close `jobCh` only after all dequeue goroutines have exited.

---

### Bug 4 — `promoteRetryableJobs` Fetches All Failed Jobs (MEDIUM — Performance)

**Location:** `internal/scheduler/scheduler.go` — `promoteRetryableJobs`

```go
status := domain.JobStatusFailed
jobs, err := s.store.ListJobs(ctx, store.JobFilter{Status: &status, Limit: s.cfg.BatchSize})
```

This fetches up to `BatchSize` failed jobs regardless of `next_retry_at`. The Go-side filter `job.NextRetryAt != nil && time.Now().Before(*job.NextRetryAt)` then skips most of them. At scale with many failed jobs in backoff, this wastes a DB round-trip and returns rows that are immediately discarded.

The partial index `idx_jobs_retry_eligible` (`WHERE status='failed' AND next_retry_at IS NOT NULL`) exists but is unused. A dedicated store method would fix this.

---

### Bug 5 — `isUniqueViolation` Uses String Matching (LOW)

**Location:** `internal/store/postgres/db.go`

```go
func isUniqueViolation(err error) bool {
    msg := err.Error()
    return strings.Contains(msg, "23505") || ...
}
```

This is fragile. pgx v5 wraps errors as `*pgconn.PgError` with a `Code` field. The correct check is:

```go
var pgErr *pgconn.PgError
if errors.As(err, &pgErr) {
    return pgErr.Code == "23505"
}
```

The string match works in practice but will break if pgx changes its error message format.

---

### Bug 6 — `ReclaimStalePending` Consumer ID is `"reclaimer"` (LOW)

**Location:** `internal/queue/redis/redis_queue.go`

`XAUTOCLAIM` transfers messages to consumer `"reclaimer"`. This consumer is never registered in the consumer group and never calls `XACK`. Messages claimed by `"reclaimer"` will accumulate in its PEL indefinitely and be re-claimed on every sweep. This is functionally correct (messages get redelivered) but the `"reclaimer"` consumer's PEL will grow without bound in Redis memory.

**Fix:** After `XAUTOCLAIM`, re-add the claimed messages back to the stream with `XADD` and then `XACK` them from the reclaimer's PEL, or use a real consumer ID that processes and acks the messages.

---

### Bug 7 — `Dequeue` Creates a New Consumer ID Per Call (LOW)

**Location:** `internal/queue/redis/redis_queue.go`

```go
consumerID := fmt.Sprintf("worker-%d", time.Now().UnixNano())
```

Every `Dequeue` call creates a new consumer in the Redis consumer group. Over time, the consumer group accumulates thousands of stale consumer entries. Redis does not automatically clean these up. `XGROUP DELCONSUMER` must be called periodically.

**Fix:** Use a stable consumer ID per worker instance (e.g., `p.cfg.WorkerID`) and pass it through to `Dequeue`. The `Queue` interface may need a `consumerID` parameter or the `RedisQueue` should be constructed with a consumer ID.

---

### Bug 8 — `go.mod` Declares `go 1.25.0` (LOW)

**Location:** `go.mod`

```
go 1.25.0
```

Go 1.25 does not exist as of this writing. This is likely a typo for `go 1.22.0` (which introduced `http.ServeMux` pattern matching used in `cmd/api/main.go`). This will cause `go build` to fail on any Go toolchain older than the declared version, and may confuse tooling.

---

## 5. Test Coverage Assessment

### What Has Tests

| Package | Test File | Coverage Type |
|---|---|---|
| `internal/domain` | `job_test.go` | Unit — state machine transitions, `IsRetryable`, `IsTerminal` |
| `internal/worker` | `inline_test.go` | Unit — handler registration, execution, error paths |
| `internal/worker/k8s` | `executor_test.go` | Unit — fake k8s client, watch/poll paths, GPU spec |
| `internal/store/postgres` | `postgres_integration_test.go` | Integration — requires live PG |
| `internal/store/postgres` | `store_unit_test.go` | Unit — mock-based |
| `internal/pipeline` | `advancement_test.go` | Unit — DAG advancement algorithm, cascade cancel |
| `internal/api/handler` | `job_test.go`, `pipeline_test.go` | Unit — HTTP handler request/response |
| `internal/api/grpc` | `server_test.go` | Unit — gRPC server methods |
| `pkg/retry` | `retry_test.go` | Unit — backoff distribution |

### What Is Missing Tests

- `internal/scheduler/` — **no tests at all**. The scheduler is the most complex component (leader election, dispatch loop, orphan reclaim, retry promotion, pipeline advancement wiring) and has zero test coverage.
- `internal/queue/redis/` — no tests. The Redis queue implementation (XREADGROUP, XAUTOCLAIM, PEL management) is untested.
- `internal/observability/` — no tests for metric registration or tracing setup.
- `cmd/` entrypoints — no smoke tests or integration tests for the full startup sequence.
- End-to-end test: no test that submits a job via HTTP and verifies it reaches `completed` status through the full pipeline.

### Integration Test Gap

`postgres_integration_test.go` requires a live PostgreSQL instance. There is no `docker-compose.test.yml` or `make test-integration` target that spins up the test database automatically. Running `go test ./...` will skip or fail integration tests in CI without manual setup.

---

## 6. Security Gaps

### Authentication and Authorization — Not Implemented

The HTTP API has **no authentication**. Any client that can reach port 8080 can submit jobs, list all jobs, modify queue configs, and read execution logs. For a production ML platform this is a critical gap.

Missing:
- No API key / Bearer token validation middleware
- No RBAC (e.g., "data-science team can submit to `default` queue but not modify queue configs")
- No mTLS between services (API ↔ scheduler ↔ worker)
- The gRPC server has no interceptor for auth

### TLS — Disabled

`observability.go` explicitly notes:
```go
otlptracegrpc.WithInsecure(), // use TLS in production
```

The OTLP exporter sends traces over plaintext gRPC. In production, traces may contain job payloads with sensitive ML parameters.

The HTTP server in `cmd/api/main.go` has no TLS configuration. It listens on plain HTTP.

### Secret Management

`values.yaml` has:
```yaml
database:
  dsn: ""
  existingSecret: ""
```

The DSN (containing the database password) defaults to empty and must be provided. The Helm `secret.yaml` template creates a Kubernetes Secret from `values.yaml` values — meaning the DSN can end up in Helm release history in plaintext. The `existingSecret` field is the correct production path but is not documented.

### Input Validation

`internal/api/handler/job.go` validates job submissions, but:
- No maximum payload size limit on the HTTP server (a client can send a 1GB JSON body)
- `KubernetesSpec.Image` is not validated against an allowlist — any image can be launched
- `KubernetesSpec.Command` is not sanitized — arbitrary commands can be injected into pods
- No rate limiting on the HTTP API (a client can submit millions of jobs)

### Redis — No Auth in docker-compose

`docker-compose.yml` starts Redis with no password (`redis-server --appendonly yes`). The `RedisConfig` has a `Password` field but it defaults to empty. In production, Redis must be password-protected or network-isolated.

---

## 7. Production Readiness Gaps

### Infrastructure

| Gap | Severity | Notes |
|---|---|---|
| No Ingress template in Helm chart | High | API is unreachable externally without manual work |
| No NetworkPolicy | High | Worker pods can reach any cluster service |
| No PodDisruptionBudget | Medium | Rolling updates can take all pods down |
| No TLS on HTTP API | High | All job data transmitted in plaintext |
| No TLS on gRPC server | Medium | Internal service-to-service traffic unencrypted |
| Redis has no password in defaults | High | Any pod in the cluster can read/write the job queue |
| No Horizontal Pod Autoscaler for API | Medium | API cannot scale under submission load spikes |
| `migrate-job.yaml` missing `ttlSecondsAfterFinished` | Low | Completed migration Jobs accumulate |

### Operational

| Gap | Severity | Notes |
|---|---|---|
| No `GET /workers` endpoint | Medium | Cannot inspect worker health via API |
| No job cancellation API | High | Cannot stop a running job without direct DB access |
| No dead-letter replay API | High | Dead jobs require manual DB intervention to retry |
| No pipeline cancellation API | Medium | Cannot stop a running pipeline |
| Scheduled job sorted-set sweeper missing | Critical | Jobs with `scheduled_at` never execute |
| No `ORION_WORKER_QUEUES` env var parsing | High | Worker silently processes no queues if not set in code |
| No structured runbook for common failures | Medium | `docs/RUNBOOK.md` exists but may be incomplete |

### Reliability

| Gap | Severity | Notes |
|---|---|---|
| Advisory lock on pooled connection | Critical | Two schedulers can run simultaneously |
| Double-execution risk on worker restart | Medium | Same job can execute twice in edge case |
| Dequeue goroutine not in WaitGroup | Low | Potential panic on shutdown under load |
| `promoteRetryableJobs` ignores partial index | Medium | Slow at scale with many failed jobs |
| No circuit breaker on Redis | Medium | Redis outage causes worker goroutines to spin |
| No circuit breaker on PostgreSQL | Medium | PG outage causes all operations to fail loudly |

### Observability

| Gap | Severity | Notes |
|---|---|---|
| No alerting rules in Prometheus | Medium | Metrics exist but no alerts defined |
| No log aggregation config (Loki/CloudWatch) | Medium | Logs go to stdout only |
| No SLO/SLA definitions | Low | No error budget tracking |
| Grafana dashboard covers basic metrics only | Low | No pipeline-level panels, no worker utilization heatmap |

---

## 8. Future Roadmap

### Phase 10 — Security Hardening (Recommended Next)

- Add `AuthMiddleware` to HTTP mux: validate `Authorization: Bearer <token>` against a configurable secret or JWKS endpoint
- Add gRPC `UnaryInterceptor` and `StreamInterceptor` for auth
- Enable TLS on HTTP server (cert from Kubernetes Secret or cert-manager)
- Enable TLS on OTLP exporter
- Add `maxBytesReader` to HTTP server to cap request body size
- Add image allowlist validation for `KubernetesSpec.Image`
- Add Helm `NetworkPolicy` template

### Phase 11 — Missing Core APIs

- `POST /jobs/{id}/cancel` — transition running/queued/scheduled job to `cancelled`
- `POST /jobs/{id}/replay` — re-enqueue a `dead` or `failed` job
- `GET /workers` — list active workers with their queue assignments and active job counts
- `DELETE /pipelines/{id}` — cancel a running pipeline and all its pending nodes
- Cascade cancellation: create explicit `cancelled` job records for downstream nodes

### Phase 12 — Scheduled Job Sweeper

Implement the sorted-set-to-stream promotion loop:

```go
// Every second, move jobs from orion:queue:scheduled whose score <= NOW().Unix()
// into their target Redis stream via ZRANGEBYSCORE + XADD + ZREM
```

This is a critical missing feature for any ML workflow that needs time-based job scheduling (e.g., nightly retraining runs).

### Phase 13 — Reliability Improvements

- Fix advisory lock to use a dedicated `pgxpool.Conn`
- Fix `dequeueLoop` goroutine lifecycle (add to WaitGroup)
- Add `ListRetryableJobs` store method using the partial index
- Fix `isUniqueViolation` to use `pgconn.PgError`
- Fix `ReclaimStalePending` consumer PEL accumulation
- Fix stable consumer ID in `Dequeue`
- Add circuit breaker (e.g., `sony/gobreaker`) around Redis and PostgreSQL calls

### Phase 14 — Scheduler Tests

The scheduler is the most critical untested component. Add:

- Unit tests for `scheduleQueuedJobs` with a mock store and queue
- Unit tests for `promoteRetryableJobs` with time-controlled `next_retry_at`
- Unit tests for `reclaimOrphanedJobs`
- Unit tests for leader election contention (two schedulers, one wins)
- Integration test: submit job → verify it reaches `running` within 5s

### Phase 15 — Redis Queue Tests

- Unit tests for `Enqueue` / `Dequeue` using `miniredis` (in-memory Redis for tests)
- Test PEL reclaim: enqueue, dequeue without ack, advance time, verify reclaim
- Test `Dead` queue routing
- Test `StartQueueDepthPoller` metric updates

### Phase 16 — Multi-Tenancy

For a real ML platform serving multiple teams:

- Add `tenant_id` column to `jobs` and `pipelines` tables
- Add tenant-scoped API keys
- Add per-tenant queue quotas (extend `queue_config` with `tenant_id`)
- Add row-level security in PostgreSQL

### Phase 17 — Job Log Streaming

`JobExecution.LogsRef` is a string field for an S3/GCS URI, but nothing writes to it. Implement:

- Worker streams stdout/stderr from inline handlers to object storage
- K8s executor fetches pod logs after job completion and uploads to object storage
- `GET /jobs/{id}/executions/{attempt}/logs` endpoint that proxies from object storage

### Phase 18 — Event Streaming / Webhooks

- Add `POST /webhooks` to register a URL for job state change notifications
- Publish events to the webhook on every terminal state transition
- Add Kafka/NATS JetStream as an alternative queue backend (the `queue.Queue` interface already supports this)

---

## 9. Priority Action List

Ordered by impact × urgency for making Orion production-ready.

### Immediate (Before Any Production Traffic)

1. **Fix advisory lock** — use a dedicated connection in `runAsLeader`. This is a correctness bug that can cause duplicate job execution.
2. **Add authentication middleware** — even a simple static API key is better than nothing.
3. **Implement scheduled job sweeper** — jobs with `scheduled_at` silently never run.
4. **Add `ORION_WORKER_QUEUES` env var parsing** — without this, a misconfigured worker processes nothing and gives no error.
5. **Add Helm Ingress template** — the API is not externally reachable without it.
6. **Add Redis password to defaults** — the queue is wide open on any cluster.

### Short Term (First Production Sprint)

7. **Add `POST /jobs/{id}/cancel`** — operators need a way to stop runaway jobs.
8. **Add `POST /jobs/{id}/replay`** — dead jobs need a recovery path.
9. **Add `GET /workers`** — basic operational visibility.
10. **Fix `dequeueLoop` goroutine lifecycle** — prevents potential panic on shutdown.
11. **Add scheduler unit tests** — the most critical untested component.
12. **Add Redis queue tests with miniredis** — PEL and reclaim logic needs coverage.
13. **Add `NetworkPolicy` to Helm chart** — isolate worker pods.
14. **Add `PodDisruptionBudget` to Helm chart** — safe rolling updates.

### Medium Term (Hardening Sprint)

15. **Fix `promoteRetryableJobs`** — add `ListRetryableJobs` store method using the partial index.
16. **Fix `isUniqueViolation`** — use `pgconn.PgError` type assertion.
17. **Fix stable consumer ID in `Dequeue`** — prevent consumer group bloat.
18. **Fix `ReclaimStalePending` PEL accumulation** — prevent Redis memory growth.
19. **Add Prometheus alerting rules** — `orion_jobs_dead_total` rate, queue depth thresholds, scheduler cycle latency.
20. **Add cascade cancellation job records** — downstream nodes should appear as `cancelled` in the API.
21. **Enable TLS on HTTP and gRPC servers**.
22. **Add `maxBytesReader` to HTTP server**.

### Long Term (Feature Completeness)

23. Implement Phase 11 (missing APIs), Phase 12 (scheduled sweeper), Phase 16 (multi-tenancy), Phase 17 (log streaming).
24. Add end-to-end integration test suite.
25. Add Loki log aggregation configuration.
26. Define SLOs and add Prometheus recording rules for error budget tracking.

---

*Inspection completed: 2026-05-09. Next review recommended after Priority Items 1–6 are addressed.*
