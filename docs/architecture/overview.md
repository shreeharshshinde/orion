# Orion — Architecture Overview

## Components

```
Client
  │  HTTP :8080 / gRPC :9090
  ▼
API Server
  │  INSERT jobs (status=queued)
  ▼
PostgreSQL ◄──────────────────────────────────────────┐
  │                                                    │
  │  SELECT queued (every 2s, leader-elected)          │
  ▼                                                    │
Scheduler                                             │
  │  XADD                                             │
  ▼                                                    │
Redis Streams                                         │
  orion:queue:high / default / low                    │
  orion:queue:scheduled (sorted set, future jobs)     │
  orion:queue:dead (terminal failures)                │
  │  XREADGROUP                                       │
  ▼                                                    │
Worker Pool ──── InlineExecutor (Go handler)          │
             └── KubernetesExecutor ──► K8s Job       │
                                                      │
  UPDATE status=completed/failed ───────────────────►─┘
```

## Data Flow

1. **Submit** — Client POSTs to `/jobs`. API writes `status=queued` to PostgreSQL and returns `job_id`. Stateless; safe to run N replicas.

2. **Dispatch** — Scheduler polls PostgreSQL every 2s. CAS-transitions `queued→scheduled`, then `XADD` to the Redis stream for the job's queue. Only one scheduler is active at a time via `pg_try_advisory_lock`.

3. **Execute** — Worker calls `XREADGROUP` (blocks up to 5s). On message: `scheduled→running` in PG, executes via the matching executor, then `running→completed/failed` in PG, then `XACK`.

4. **Retry** — On failure, `MarkJobFailed` sets `next_retry_at` using full-jitter backoff. Scheduler's retry promoter re-enqueues when the delay expires.

5. **Orphan recovery** — Scheduler reclaims jobs stuck in `running` whose worker missed heartbeats (threshold: 90s). Resets to `queued` for re-dispatch.

6. **Pipelines** — Scheduler's `Advancer` runs on every tick. Reads node statuses, creates jobs for newly-unblocked nodes, detects completion/failure.

## Key Design Decisions

See `docs/adr/` for full rationale on each decision.

| Decision | ADR |
|---|---|
| Redis Streams with consumer groups for at-least-once delivery | ADR-001 |
| PostgreSQL advisory locks for scheduler leader election | ADR-002 |
| CAS state transitions via `UPDATE WHERE status = expected` | ADR-003 |
| JSONB for job payload and DAG spec | ADR-004 |
| gRPC streaming for real-time job watch | ADR-005 |

## Concurrency Model

```
Dequeue goroutines (1 per queue)
        │  send (blocks when full — backpressure)
        ▼
    jobCh  [buffered, cap=Concurrency]
        │  receive
        ▼
Worker goroutines (N = Concurrency)
        │
        ├── InlineExecutor
        └── KubernetesExecutor
```

`jobCh` capacity equals `Concurrency`. When all workers are busy, dequeue goroutines block on send — jobs stay in Redis PEL rather than accumulating in memory.

## Failure Modes

| Failure | Recovery |
|---|---|
| Worker crashes mid-job | PEL reclaimer (`XAUTOCLAIM`) redelivers after visibility timeout |
| Worker goes silent | Orphan reclaimer resets job to `queued` after 90s |
| Scheduler crashes | Another instance acquires advisory lock within 3s |
| Redis unavailable | Workers block in `XREADGROUP`; no data loss |
| PostgreSQL unavailable | All operations fail loudly; jobs stay in Redis PEL |
