# ADR-001: Redis Streams for Job Queue with Consumer Groups

**Date:** 2024-01-01
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Orion needs a distributed job queue that provides:
- At-least-once delivery guarantees
- Competing consumer semantics (one worker picks up each job)
- Dead-letter queue for failed messages
- Ability to inspect pending/stuck messages

## Decision

Use **Redis Streams with Consumer Groups** (`XREADGROUP` / `XACK`).

We evaluated three options:

| Option | At-least-once | Competing consumers | PEL / reclaim | Ops overhead |
|---|---|---|---|---|
| Redis List (LPUSH/BRPOP) | ❌ message lost on crash | ✅ | ❌ | Low |
| Redis Streams + Consumer Groups | ✅ via PEL | ✅ | ✅ XAUTOCLAIM | Low |
| NATS JetStream | ✅ | ✅ | ✅ | Medium |

Redis Lists were eliminated because an un-acked message (worker crash mid-execution) is permanently lost. NATS JetStream is operationally heavier and adds a new component; we can migrate to it later via the Queue interface abstraction.

## Consequences

**Positive:**
- Redis is already in the stack for caching; no new infrastructure
- Consumer groups give exactly the semantics we need
- `XAUTOCLAIM` handles stale pending entries from crashed workers
- Streams are persisted to disk with AOF (configure `appendonly yes`)

**Negative:**
- Redis is not designed as a primary message broker; under extreme load, use NATS or Kafka
- Consumer group semantics are more complex than simple LPUSH/BRPOP
- Must run `ReclaimStalePending` background sweep to handle crashed workers

## Implementation Notes

- All workers join consumer group `orion-workers`
- Visibility timeout: 5 minutes (configurable)
- Stale PEL sweep interval: 30 seconds
- Dead-letter stream: `orion:queue:dead`
- Scheduled jobs use a sorted set (`ZADD` by unix timestamp) with a promotion goroutine

## Migration Path

The `queue.Queue` interface is the abstraction boundary. To migrate to NATS JetStream:
1. Implement `NATSQueue` satisfying `queue.Queue`
2. Swap in `config.go` — no worker or scheduler changes required