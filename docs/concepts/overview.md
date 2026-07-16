---
title: System Overview & Topology
description: The high-level architectural thesis, service topology, and workflows of Orion.
---

# System Overview & Topology

Orion separates **control-plane state**, **delivery**, and **execution** into distinct, decoupled subsystems.

PostgreSQL is the single source of truth. Redis is not authoritative; it is used purely as a transient delivery transport. Workers are stateless, disposable executors. The scheduler is an active/passive singleton leader that translates durable database intent into Redis delivery messages. The API tier is responsible for creating and observing this durable intent.

```mermaid
flowchart LR
  Client[HTTP/gRPC client] --> API[orion-api]
  API --> PG[(PostgreSQL)]
  API --> Cancel[Redis pub/sub cancel]
  API --> GRPC[gRPC WatchJob streams]
  PG --> Notify[LISTEN/NOTIFY notifier]
  Notify --> GRPC

  Scheduler[orion-scheduler leader] --> PG
  Scheduler --> Redis[(Redis Streams)]
  Scheduler --> Scheduled[(Redis ZSET scheduled jobs)]

  Worker[orion-worker pool] --> Redis
  Worker --> PG
  Worker --> Inline[Inline handlers]
  Worker --> K8s[Kubernetes batch/v1 Jobs]
  Cancel --> Worker
```

## Architectural Guarantees

* **Atomic Transitions:** Job state transitions are executed atomically using Compare-and-Swap (CAS) queries at the database level.
* **At-Least-Once Delivery:** Message dispatch via Redis Streams ensures at-least-once queue delivery.
* **Idempotent Execution:** Execution safety is enforced by database CAS guards, preventing duplicate runs even if messages are delivered multiple times.
* **Advisory Leader Lock:** Active/passive scheduler redundancy is managed through PostgreSQL session-level advisory locks.
* **Resilient Failovers:** If workers crash, unprocessed or active messages remain in the Redis Pending Entries List (PEL) and are safely reclaimed by the scheduler after heartbeats expire.
* **Orion-Owned Retries:** Kubernetes Job retries are disabled; Orion manages attempt counters, retry schedules, and backoffs directly.

---

## Service Topology

Orion compiles into three production Go binaries:

| Binary | Stateful? | Horizontal Scale | Main Dependencies | Primary Loop |
| --- | --- | --- | --- | --- |
| `orion-api` | No | Yes | PostgreSQL, Redis, gRPC | Request/Response + Event Streams |
| `orion-scheduler` | Logically (via lock) | Active/Passive | PostgreSQL, Redis | Ticker-based schedule and sweep loops |
| `orion-worker` | No | Yes | PostgreSQL, Redis, Kubernetes API | Bounded worker pool consuming streams |

---

## Core Workflows

### 1. End-to-End Job Submission

```mermaid
sequenceDiagram
  participant C as Client
  participant A as API handler
  participant PG as PostgreSQL
  participant S as Scheduler leader
  participant R as Redis Stream
  participant W as Worker pool
  participant E as Executor

  C->>A: POST /jobs
  A->>PG: CreateJob(status=queued)
  PG-->>A: persisted job
  A-->>C: 201 Created
  
  loop every scheduler tick
    S->>PG: List queued jobs / fair queue fetch
    S->>PG: CAS queued -> scheduled
    S->>R: XADD job payload
  end

  W->>R: XREADGROUP
  R-->>W: job message in PEL
  W->>PG: CAS scheduled -> running
  W->>PG: RecordExecution(running)
  W->>E: Execute(ctx, job)
  E-->>W: nil (success) or error
  
  alt success
    W->>PG: CAS running -> completed
    W->>PG: RecordExecution(completed)
    W->>R: XACK
  else failure
    W->>PG: CAS running -> failed + attempt++ + next_retry_at
    W->>PG: RecordExecution(failed)
    W-->>R: no XACK (retained in PEL)
  end
```

### 2. Real-Time Job Watching (gRPC)

```mermaid
sequenceDiagram
  participant Client
  participant GRPC as gRPC Server
  participant B as Broadcaster
  participant PG as PostgreSQL
  participant N as PG Notifier

  Client->>GRPC: WatchJob(job_id)
  GRPC->>PG: GetJob
  GRPC-->>Client: Current status event
  GRPC->>B: Subscribe(job_id)
  loop on state transition
    PG-->>N: NOTIFY orion_job_events
    N->>B: Publish(JobEvent)
    B-->>GRPC: Event channel
    GRPC-->>Client: Stream event
  end
```
