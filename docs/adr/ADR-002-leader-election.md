# ADR-002: PostgreSQL Advisory Locks for Scheduler Leader Election

**Date:** 2024-01-01
**Status:** Accepted

---

## Context

Multiple scheduler instances may run simultaneously (e.g., during rolling deploys or HA setups). Without leader election, two schedulers can:
- Double-schedule the same job (both transition it from `queued` → `scheduled`)
- Create duplicate queue entries
- Cause conflicting orphan reclaim operations

## Decision

Use **PostgreSQL session advisory locks** (`pg_try_advisory_lock`) for leader election.

Evaluated options:

| Option | Complexity | External dependency | Auto-release on crash |
|---|---|---|---|
| PG Advisory Lock | Low | None (PG already required) | ✅ session-scoped |
| Redis SET NX PX + heartbeat | Medium | Redis (already in stack) | ❌ requires heartbeat |
| etcd lease | High | New component | ✅ |
| Kubernetes leader election (client-go) | Medium | K8s API server | ✅ |

## Rationale

Advisory locks are automatically released when the database connection is closed — including on process crash. This means no stale lock situation. etcd adds operational overhead. Kubernetes leader election is appropriate if we're already deeply integrated with K8s, but for the scheduler (which can run outside K8s) it's over-engineering.

The CAS-based `TransitionJobState` function provides a second layer of safety: even if two schedulers somehow both believe they're leader, the atomic `UPDATE WHERE status = 'queued'` prevents double-scheduling.

## Consequences

- The scheduler must maintain a dedicated PG connection for the advisory lock (not from the pool)
- Lock key `7331001` is documented here and must not be reused by other services
- Standby schedulers spin-poll every 3 seconds; this is acceptable overhead

## Future

If we move to Kubernetes-native deployment, consider migrating to `client-go`'s leader election which uses ConfigMap or Lease objects — it's purpose-built for this use case.