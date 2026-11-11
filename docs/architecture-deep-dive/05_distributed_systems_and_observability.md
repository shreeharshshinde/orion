---
title: "Distributed Systems & Observability"
description: "Active-passive leader election, distributed failure modes, crash recovery matrix, and observability stack."
---

# Distributed Systems Guarantees & Observability

## Source Of Truth & Consistency Model

### Source Of Truth

PostgreSQL is authoritative. Redis can lose messages only within its persistence configuration, but job state remains safe in PostgreSQL. The scheduler can re-dispatch queued, retryable, or reclaimed work at any time.

### At-Least-Once Delivery

Redis Streams plus consumer groups provide Pending Entries List (PEL)-backed at-least-once delivery. The system does not claim exactly-once execution. It uses idempotency keys and CAS transitions to make duplicate messages survivable.

### Idempotency Matrix

Idempotency appears across multiple layers:
- **Client submission**: Enforced via `idempotency_key` and unique database constraint.
- **Pipeline jobs**: `AddPipelineJob` uses `ON CONFLICT DO NOTHING`.
- **Execution history**: `RecordExecution` uses `ON CONFLICT (job_id, attempt) DO NOTHING`.
- **Scheduler dispatch**: CAS `queued -> scheduled` before `XADD`.
- **Worker execution**: CAS `scheduled -> running` before invoking the executor.
- **Scheduled queue**: Redis sorted set atomic pop through Lua.

### Distributed Fault Tolerance & Crash Recovery

| Failure Scenario | Detection Mechanism | Recovery Action | Max Recovery Latency |
| :--- | :--- | :--- | :--- |
| **Worker Process Crash Mid-Execution** | Missing heartbeat in `workers` table (`last_heartbeat < NOW() - 30s`) | Scheduler `ReclaimOrphanedJobs` resets `running` jobs to `queued` | 30s (worker heartbeat timeout) + 1 tick |
| **Worker Drops Redis Connection** | Message remains unacknowledged in Redis Pending Entries List (PEL) | Redis queue `ReclaimStalePending` calls `XAUTOCLAIM` and re-injects to stream | Min-idle duration (default 30s) |
| **Scheduler Leader Crashes** | Dedicated `pgxpool.Conn` session terminates; PostgreSQL releases lock | Standby scheduler acquires `pg_try_advisory_lock` on next tick | 1 tick (default 1s) |
| **Kubernetes API Watch Interruption** | Watch stream channel closes or returns error | Worker falls back to periodic `Get` polling | 2s polling interval |
| **Downstream Infrastructure Outage** | Repeated job execution failures | Full-jitter exponential backoff (`min(30m, 5s * 2^attempt)`) avoids thundering herds | Automated with backoff |

### Consistency Model

The system is eventually consistent across API views, Redis delivery, worker execution, and gRPC events. PostgreSQL row state is strongly consistent per row. Multi-component flows are not atomic. For example, a scheduler can transition a job to `scheduled` and fail to enqueue it; the code attempts to revert to `queued`, but if that revert fails an operator or sweeper path may be needed.

### Backpressure Architecture

Backpressure exists at multiple levels:
- **Scheduler batch size**: Limits dispatch per tick.
- **Token buckets**: Limit per-queue dispatch rate.
- **Worker `jobCh`**: Bounds in-process jobs to worker concurrency.
- **Redis Streams**: Hold pending backlog safely outside process memory.
- **Horizontal Pod Autoscaling (HPA)**: Scales workers based on queue depth per worker.

---

## Full Observability Stack

### Metrics

`internal/observability/observability.go` defines a single `Metrics` struct with Prometheus counters, gauges, and histograms. Each binary creates its own registry and metrics endpoint.

Important metric families:
- **Job lifecycle**: `orion_jobs_submitted_total`, `orion_jobs_completed_total`, `orion_jobs_failed_total`
- **Duration**: `orion_job_duration_seconds` histogram
- **Queue depth**: `orion_queue_depth` gauge
- **Worker utilization**: `orion_worker_active_jobs` gauge
- **Scheduler latency**: `orion_scheduler_cycle_seconds` histogram
- **Pipelines**: `orion_pipeline_duration_seconds`
- **HTTP / DB**: `orion_http_requests_total`, `orion_db_operation_duration_seconds`
- **Rate limits / Queues**: `orion_queue_rate_limit`, `orion_queue_concurrency`, `orion_queue_weight`

Cardinality discipline is explicit: labels use queue, type, status, method, route pattern, operation names. Job IDs are never metric labels.

### HTTP Middleware

- `MetricsMiddleware`: Wraps `http.ResponseWriter` with `statusRecorder` and uses `r.Pattern` for low-cardinality route labels.
- `TracingMiddleware`: Starts a server span per request. The current code starts spans from `r.Context()` and uses `otelgrpc.NewServerHandler` for gRPC.

### Logging

`NewLogger` returns structured `slog` loggers:
- Text format in local development.
- JSON format in staging/production.
- `WithTrace` enriches loggers with active OpenTelemetry trace and span IDs.

---

## Performance Profiling & Scaling Characteristics

### Scaling Strengths

- API is stateless and horizontally scalable.
- Workers are stateless and horizontally scalable.
- Scheduler replicas provide high availability through advisory locks.
- PostgreSQL indexes target all critical hot queries.
- Redis Streams handle high-throughput delivery.
- Worker channel backpressure prevents memory blowups.
- Queue depth metrics support automated autoscaling (HPA).

### Potential Bottlenecks

- **Single Active Leader**: The scheduler is a single active leader, so dispatch throughput is bounded by one process and DB/Redis round-trips.
- **Pipeline Advancement Query**: Pipeline advancement queries active pipelines every tick.
- **gRPC Watch Polling Fallback**: The 500ms WatchJob polling fallback can add DB load if many clients watch jobs simultaneously.
- **Single Rate-Limiter Mutex**: `QueueRateLimiter` uses one mutex for all queues.
- **Per-Queue Queries**: `FetchReadyJobs` issues one DB query per queue per tick.
- **Redis Stream Trimming**: Redis stream length via `XLEN` counts historical messages unless streams are trimmed; long-lived streams need trimming policies.
