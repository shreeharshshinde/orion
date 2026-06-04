# Changelog

All notable changes to Orion are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- PG LISTEN/NOTIFY notifier (`internal/api/grpc/notifier.go`) drives the
  Broadcaster on job status changes, replacing the 500 ms poll ticker in
  `WatchJob`. Reduces DB load from O(clients × 120 qps) to near-zero.
- `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md` for open-source readiness.
- `make test-integration` now auto-spins Docker infrastructure and waits for
  Postgres + Redis health before running the integration test suite.

### Changed
- `WatchJob` poll ticker removed; broadcaster is now the sole event source.
- `WatchPipeline` poll interval unchanged (pipeline events are low-frequency).

---

## [0.2.0] — 2026-05-27

### Added

**APIs**
- `POST /jobs/{id}/cancel` — cancels `queued`/`scheduled` jobs via CAS transition; signals in-flight `running` jobs via Redis pub/sub (`orion:cancel` channel). Returns 503 when no cancel signaler is configured.
- `POST /jobs/{id}/replay` — re-enqueues `dead` or `failed` jobs by CAS transition to `queued` and calling `queue.Enqueue`. Scheduler orphan reclaimer recovers the job if the Redis enqueue fails.
- `GET /workers` — lists workers that have sent a heartbeat within the last 45 seconds, with queue assignments and active job counts.
- `POST /pipelines/{id}/cancel` — cancels `pending` or `running` pipelines; returns 409 for already-terminal pipelines. The scheduler's `AdvanceAll` loop naturally stops advancing cancelled pipelines.
- `DELETE /jobs/{id}` — deletes a job record; guards against deleting `running`/`scheduled` jobs with 409 Conflict; returns 204 No Content on success.

**Domain**
- `dead → queued` and `failed → queued` added to `ValidTransitions` in `domain/job.go` to support the replay endpoint.
- `pkg/retry.Do(ctx, fn, opts...)` — context-aware retry wrapper with `WithMaxAttempts`, `WithBase`, and `WithCap` functional options. Cancellation interrupts the jitter sleep immediately.

**Queue**
- `StartScheduledSweeper` on `RedisQueue`: ticks every second and promotes due jobs from the `orion:queue:scheduled` sorted set to their target stream via an atomic Lua `ZRANGEBYSCORE`+`ZREM` script. Only the leader scheduler runs the sweeper.
- `ORION_WORKER_QUEUES` env var parsed in `config.Load()` (comma-separated). Default: `orion:queue:high,orion:queue:default,orion:queue:low`. Removes the manual fallback from `cmd/worker/main.go`.

**Worker**
- `internal/worker/cancel/cancel.go` — `Signaler` interface (`Publish`, `Subscribe`) backed by `redis.Client`. Enables cross-process job cancellation.
- Per-job `context.WithCancel` in `executeJob`; cancel func registered in `Pool.cancelRegistry` (mutex-guarded) and deregistered on completion.
- `startCancelListener` goroutine in `Pool` subscribes to Redis cancel signals and invokes `CancelJob(id)` on match.

**gRPC**
- `TransitionJobState` override added to `InstrumentedStore`: publishes a `JobEvent` with `new_status` on every successful scheduler-driven transition (`queued→scheduled`, `failed→retrying`, `retrying→queued`, `queued→cancelled`). Previously these transitions were invisible to `WatchJob` streams.
- 500 ms poll ticker added to `WatchJob` as a safety net when PG LISTEN/NOTIFY is unavailable or slow.

**Pipeline**
- `createCancelledDownstreamJobs` replaces `logCascadeCancellation` in `internal/pipeline/advancement.go`. Downstream nodes that never started now receive explicit `cancelled` job records linked via `AddPipelineJob`. Partial failures are logged and do not block the pipeline failure transition.

**Store**
- `ListRetryableJobs(ctx, limit)` added to `store.JobStore` interface and implemented in `postgres.DB`. Queries `WHERE status='failed' AND next_retry_at IS NOT NULL AND next_retry_at <= NOW() AND attempt < max_retries ORDER BY next_retry_at ASC`, using the `idx_jobs_retry_eligible` partial index. Replaces the Go-side filter in `promoteRetryableJobs`.

**Helm / Kubernetes**
- `deploy/helm/templates/ingress.yaml` — gated on `ingress.enabled` (default `false`); supports `ingressClassName`, `host`, `annotations`, and `tls`.
- `deploy/helm/templates/network-policy.yaml` — gated on `networkPolicy.enabled` (default `false`); one `NetworkPolicy` per component enforcing least-privilege egress (PostgreSQL, Redis, OTLP, K8s API server, DNS) and restricted ingress.
- `deploy/helm/templates/migrate-job.yaml` — Helm pre-install/pre-upgrade hook Job with `ttlSecondsAfterFinished: 300` and `hook-delete-policy: before-hook-creation`. Replaces the manual `deploy/k8s/migrate-job.yaml` for Helm-managed deployments.
- Worker `PodDisruptionBudget` (`maxUnavailable: 1`) added to `worker-deployment.yaml`. API (`minAvailable: 2`) and scheduler (`minAvailable: 1`) PDBs were already present.

**Tests**
- 11 unit tests for `internal/scheduler` covering dispatch, retry promotion, orphan reclaim, leader election, and context cancellation — using `fakeStore` and `fakeQueue` (no live infrastructure required).
- 12 unit tests for `internal/queue/redis` using `miniredis`: consumer group creation, enqueue/dequeue, scheduled job routing, PEL reclaim (XADD+XACK), queue depth polling, and stable consumer ID.
- 15 unit tests for `internal/observability` covering metrics registration, tracing setup, logging configuration, and HTTP endpoints.
- Smoke tests for all three `cmd/` entrypoints (`cmd/api`, `cmd/scheduler`, `cmd/worker`) — config load, health probes, and handler wiring with zero external dependencies.
- Tests for `internal/api/grpc/instrumented_store`, `internal/api/handler/worker`, `internal/api/handler/pipeline`, and `pkg/retry`.

### Fixed

- **Advisory lock on pooled connection (HIGH)** — `tryAcquireLeaderLock` now acquires a dedicated `*pgxpool.Conn` held for the entire leader tenure. `runAsLeader` defers `releaseLeaderLock(conn)` as its first action, ensuring the lock is released on every exit path. Eliminates the race where pgxpool could silently close the lock-holding connection, causing two schedulers to dispatch simultaneously.
- **Double-execution risk on worker restart (MEDIUM)** — `executeJob` now branches on `ErrStateConflict` from `MarkJobRunning`: resumes execution if `worker_id` matches this worker (restart scenario), ACKs stale PEL message if another worker owns the job, and ACKs for terminal/unexpected states. Eliminates the two-message scenario caused by the orphan reclaimer re-queuing an already-running job.
- **`dequeueLoop` goroutine leak on shutdown (LOW-MEDIUM)** — added `dequeueWg sync.WaitGroup` to `Pool`. `drain()` now calls `dequeueWg.Wait()` before `close(jobCh)`, preventing the panic on send-to-closed-channel and ensuring a clean, deterministic shutdown sequence.
- **`promoteRetryableJobs` ignores partial index (MEDIUM)** — replaced `ListJobs(status=failed)` + Go-side filter with `ListRetryableJobs`, which targets `idx_jobs_retry_eligible` directly and returns zero rows when no jobs are due.
- **`isUniqueViolation` uses string matching (LOW)** — replaced with `errors.As(err, &pgErr) && pgErr.Code == "23505"` using `pgconn.PgError`. Robust against pgx error message format changes.
- **`ReclaimStalePending` PEL accumulation (LOW)** — after `XAUTOCLAIM`, claimed messages are now re-added to the stream with `XADD` and then `XACK`ed from `"reclaimer"`'s PEL. Prevents unbounded Redis memory growth from the reclaimer consumer's PEL.
- **Per-call consumer ID in `Dequeue` (LOW)** — `RedisQueue` now stores a stable `consumerID` set at construction (`New()` accepts it; falls back to `os.Hostname()`). Workers pass `cfg.Worker.WorkerID`. Eliminates per-call consumer group bloat (~6M stale entries/week in a 10-worker deployment).
- **`TestExecute_WatchChannelClose_FallsBackToPoll` hung indefinitely** — replaced `fakeClient.Tracker().Add()` with `UpdateStatus()` so the fake K8s client correctly replaces the existing job object. The test was looping forever because `Add` silently no-ops on an already-existing object.
- **`TestWatchJob_PollFallback` hung indefinitely** — added 500 ms poll ticker to `WatchJob` in `server.go`. The poll path is a safety net when the broadcaster delivers no events; the test now completes correctly.

### Changed
- `NewJobHandler` accepts an optional `cancel.Signaler` variadic argument (nil disables running-job cancellation, returns 503).
- `NewPool` accepts an optional `cancel.Signaler` variadic argument (nil disables cross-process cancellation).
- Scheduler `runAsLeader` signature changed to accept `*pgxpool.Conn`; `tryAcquireLeaderLock` returns the connection alongside the boolean result.
- `StartScheduledSweeper` added to the `Queue` interface; started inside `runAsLeader` so only the leader runs it.

---

## [0.1.0] — 2026-05-12

### Added
- Core job lifecycle: `queued → scheduled → running → completed/failed/dead`.
- PostgreSQL store with CAS state transitions and advisory-lock leader election.
- Redis Streams consumer groups for at-least-once job delivery across three
  priority queues (`high`, `default`, `low`) and a dead-letter stream.
- Bounded worker pool with `InlineExecutor` and `KubernetesExecutor` (client-go).
- Full-jitter exponential backoff retry with configurable cap and max attempts.
- Scheduler: dispatch loop, orphan reclaimer, retry promoter.
- gRPC `JobService`: `SubmitJob`, `GetJob`, `WatchJob`, `WatchPipeline`.
- HTTP REST API: job CRUD, idempotency keys, pipeline endpoints.
- DAG pipeline support with topological advancement and cascade-cancel.
- OpenTelemetry tracing (Jaeger), Prometheus metrics, structured `slog` logging.
- Grafana dashboards and Prometheus scrape config.
- Helm chart for Kubernetes deployment.
- `docker-compose.yml` for local development stack.
- Architecture Decision Records: ADR-001 (Redis Streams), ADR-002 (PG advisory locks).

[Unreleased]: https://github.com/shreeharshshinde/orion/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/shreeharshshinde/orion/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/shreeharshshinde/orion/releases/tag/v0.1.0
