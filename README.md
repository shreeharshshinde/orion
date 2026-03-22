# Orion — Distributed ML Job Orchestrator

Orion is a production-grade distributed job orchestration platform written in Go. It schedules, executes, and monitors machine learning workloads on Kubernetes.

Designed to demonstrate senior-level backend engineering: distributed systems, Go concurrency, observability, and cloud-native architecture.

---

## System Architecture

```mermaid
graph TD
    Client(["🖥️ Client / SDK"])

    subgraph API ["API Server :8080"]
        AH["HTTP Handlers"]
        AV["Validation & Idempotency"]
    end

    subgraph DB ["PostgreSQL"]
        JT[("jobs")]
        ET[("job_executions")]
        WT[("workers")]
    end

    subgraph SCH ["Scheduler"]
        LE["Leader Election\n(PG Advisory Lock)"]
        SD["Dispatch Loop"]
        OR["Orphan Reclaimer"]
        RP["Retry Promoter"]
    end

    subgraph RD ["Redis Streams"]
        QH["orion:queue:high"]
        QD["orion:queue:default"]
        QL["orion:queue:low"]
        QDL["orion:queue:dead"]
        QS[("orion:queue:scheduled\nSorted Set")]
    end

    subgraph WP ["Worker Pool"]
        DQ["Dequeue Loop"]
        CH["jobCh (buffered)"]
        W1["Worker 1"]
        W2["Worker 2"]
        WN["Worker N"]

        subgraph EX ["Executor Interface"]
            IE["InlineExecutor\n(Go handler)"]
            KE["KubernetesExecutor\n(client-go)"]
        end
    end

    subgraph K8S ["Kubernetes"]
        KJ["K8s Job"]
        KP["Pod"]
    end

    subgraph OBS ["Observability"]
        PR["Prometheus\n:9091/metrics"]
        JG["Jaeger\n:16686"]
        GR["Grafana\n:3000"]
        OT["OpenTelemetry\nCollector"]
    end

    Client -->|"POST /jobs"| AH
    AH --> AV
    AV -->|"INSERT job"| JT
    AH -->|"200 job_id"| Client

    LE -->|"pg_try_advisory_lock"| DB
    SD -->|"SELECT queued jobs"| JT
    SD -->|"UPDATE status=scheduled"| JT
    SD -->|"XADD"| QD
    OR -->|"reclaim stale running jobs"| JT
    RP -->|"promote failed → queued"| JT

    DQ -->|"XREADGROUP"| QD
    DQ -->|"XREADGROUP"| QH
    DQ -->|"XREADGROUP"| QL
    DQ --> CH
    CH --> W1 & W2 & WN

    W1 & W2 & WN --> IE
    W1 & W2 & WN --> KE
    KE -->|"Create Job"| KJ
    KJ --> KP

    W1 -->|"UPDATE status / INSERT execution"| DB
    W2 -->|"Heartbeat"| WT

    WP -->|"metrics"| PR
    API -->|"traces"| OT
    WP -->|"traces"| OT
    SCH -->|"traces"| OT
    OT --> JG
    PR --> GR

    style API fill:#1e3a5f,color:#fff,stroke:#4a90d9
    style SCH fill:#1a3d2b,color:#fff,stroke:#4caf50
    style RD fill:#7f1d1d,color:#fff,stroke:#ef4444
    style WP fill:#3b1f5e,color:#fff,stroke:#a855f7
    style DB fill:#1a2e4a,color:#fff,stroke:#60a5fa
    style K8S fill:#0f3460,color:#fff,stroke:#3b82f6
    style OBS fill:#2d2000,color:#fff,stroke:#f59e0b
```
<img width="2468" height="2019" alt="image" src="https://github.com/user-attachments/assets/451b8db7-eb89-4ae0-84b8-86597aefd788" />

---

## Job Lifecycle State Machine

```mermaid
stateDiagram-v2
    direction LR

    [*] --> queued : submit job

    queued --> scheduled : scheduler dispatches\n(CAS lock acquired)
    queued --> cancelled : client cancels

    scheduled --> running : worker claims job
    scheduled --> queued : scheduler rollback\n(enqueue failure)
    scheduled --> cancelled : client cancels

    running --> completed : execution success
    running --> failed : execution error\nor deadline exceeded

    failed --> retrying : attempt < max_retries\n& next_retry_at elapsed
    failed --> dead : attempt >= max_retries

    retrying --> queued : re-enqueued\nwith backoff delay

    completed --> [*]
    dead --> [*]
    cancelled --> [*]

    note right of running
        Worker heartbeats every 15s.
        Scheduler reclaims orphans
        after 2x TTL with no heartbeat.
    end note

    note right of failed
        Retry delay uses full-jitter
        exponential backoff.
        Max delay: 30 minutes.
    end note
```

---

## Request Flow: Job Submission

```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant API as API Server
    participant PG as PostgreSQL
    participant SCH as Scheduler
    participant RD as Redis Streams
    participant WK as Worker Pool
    participant K8S as Kubernetes

    Client->>+API: POST /jobs {payload, idempotency_key}
    API->>PG: SELECT job WHERE idempotency_key = ?
    alt key already exists
        PG-->>API: existing job row
        API-->>Client: 200 OK {job_id, status}
    else new job
        API->>PG: INSERT job (status=queued)
        PG-->>API: job row
        API-->>-Client: 201 Created {job_id}
    end

    loop every 2s
        SCH->>PG: SELECT jobs WHERE status=queued ORDER BY priority DESC
        SCH->>PG: UPDATE status=scheduled WHERE status=queued (CAS)
        SCH->>RD: XADD orion:queue:default {job}
    end

    WK->>RD: XREADGROUP (blocks until message)
    RD-->>WK: job message + delivery tag
    WK->>PG: UPDATE status=running, worker_id=?, started_at=NOW()
    WK->>PG: INSERT job_executions (attempt, worker_id)

    alt type = k8s_job
        WK->>K8S: Create Job (client-go)
        K8S-->>WK: Job running / completed
    else type = inline
        WK->>WK: Execute registered handler
    end

    alt success
        WK->>PG: UPDATE status=completed, completed_at=NOW()
        WK->>RD: XACK (remove from PEL)
    else failure
        WK->>PG: UPDATE status=failed, error_message=?, attempt++
        note over WK: NACK — message stays in PEL\nfor visibility timeout
    end
```

---

## Worker Pool Concurrency Model

```mermaid
graph LR
    subgraph DQ ["Dequeue Goroutines (per queue)"]
        D1["Queue: high\nXREADGROUP"]
        D2["Queue: default\nXREADGROUP"]
        D3["Queue: low\nXREADGROUP"]
    end

    CH["jobCh\nbuffered channel\ncap = Concurrency\n\n← backpressure boundary"]

    subgraph WP ["Worker Goroutines"]
        W1["Worker 1"]
        W2["Worker 2"]
        W3["Worker 3"]
        WN["Worker N"]
    end

    subgraph EX ["Executor"]
        IE["InlineExecutor"]
        KE["KubernetesExecutor"]
    end

    D1 -->|"send blocks when full"| CH
    D2 -->|"send blocks when full"| CH
    D3 -->|"send blocks when full"| CH

    CH -->|"receive"| W1 & W2 & W3 & WN

    W1 & W2 & W3 & WN --> IE
    W1 & W2 & W3 & WN --> KE

    style CH fill:#7f1d1d,color:#fff,stroke:#ef4444
    style DQ fill:#1a3d2b,color:#fff,stroke:#4caf50
    style WP fill:#1e3a5f,color:#fff,stroke:#4a90d9
    style EX fill:#3b1f5e,color:#fff,stroke:#a855f7
```

> **Backpressure:** `jobCh` capacity equals `Concurrency`. When all workers are busy, sending blocks the dequeue goroutine, which stops pulling from Redis. No jobs are prefetched beyond what can be immediately worked on.

---

## Retry & Backoff Strategy

```mermaid
flowchart TD
    F["Job fails\n(execution error)"] --> IC{"attempt < max_retries?"}

    IC -->|No| DL["Move to Dead Letter\nstatus = dead\nXADD orion:queue:dead"]
    IC -->|Yes| CA["Calculate next_retry_at\nFull-Jitter Backoff\n\ndelay = random(0, min(cap, base × 2^attempt))"]

    CA --> ST["UPDATE jobs\nstatus = failed\nnext_retry_at = NOW() + delay\nattempt++"]

    ST --> PL["Scheduler polls every 2s:\nSELECT WHERE status=failed\nAND next_retry_at <= NOW()"]

    PL --> RT["Transition:\nfailed → retrying → queued"]
    RT --> EQ["Re-enqueue to Redis\nwith same priority"]
    EQ --> RN["Worker picks up\nand executes again"]
    RN --> F

    DL --> NF["Dead jobs visible in\nGrafana + dead-letter stream\nfor manual inspection / replay"]

    style F fill:#7f1d1d,color:#fff,stroke:#ef4444
    style DL fill:#3b1f1f,color:#fff,stroke:#ef4444
    style CA fill:#1a3d2b,color:#fff,stroke:#4caf50
    style NF fill:#2d2000,color:#fff,stroke:#f59e0b
```

---

## Database Schema

```mermaid
erDiagram
    jobs {
        uuid id PK
        varchar idempotency_key UK
        varchar name
        varchar type
        varchar queue_name
        smallint priority
        varchar status
        jsonb payload
        smallint max_retries
        smallint attempt
        varchar worker_id FK
        text error_message
        timestamptz scheduled_at
        timestamptz deadline
        timestamptz next_retry_at
        timestamptz started_at
        timestamptz completed_at
        timestamptz created_at
        timestamptz updated_at
    }

    job_executions {
        uuid id PK
        uuid job_id FK
        smallint attempt
        varchar worker_id
        varchar status
        timestamptz started_at
        timestamptz finished_at
        int exit_code
        text logs_ref
        text error
        timestamptz created_at
    }

    workers {
        varchar id PK
        varchar hostname
        text[] queue_names
        int concurrency
        int active_jobs
        varchar status
        timestamptz last_heartbeat
        timestamptz registered_at
    }

    pipelines {
        uuid id PK
        varchar name
        varchar status
        jsonb dag_spec
        timestamptz created_at
        timestamptz updated_at
        timestamptz completed_at
    }

    pipeline_jobs {
        uuid pipeline_id FK
        uuid job_id FK
        varchar node_id
        uuid[] depends_on
    }

    jobs ||--o{ job_executions : "has many attempts"
    workers ||--o{ jobs : "executes"
    pipelines ||--o{ pipeline_jobs : "contains nodes"
    pipeline_jobs ||--|| jobs : "maps to"
```

---

## Observability Stack

```mermaid
graph LR
    subgraph Services ["Services"]
        API["API Server"]
        SCH["Scheduler"]
        WK["Worker Pool"]
    end

    subgraph Metrics ["Metrics Pipeline"]
        PR["Prometheus :9091\n\norion_jobs_submitted_total\norion_job_duration_seconds\norion_queue_depth\norion_worker_active_jobs\norion_scheduler_cycle_duration_seconds"]
        GR["Grafana :3000\nDashboards & Alerts"]
    end

    subgraph Traces ["Tracing Pipeline"]
        OT["OpenTelemetry SDK\n(per service)"]
        JA["Jaeger :16686\nTrace Explorer"]
    end

    subgraph Logs ["Structured Logging"]
        SL["slog JSON\ntrace_id + span_id\njob_id + worker_id\non every log line"]
    end

    API & SCH & WK -->|"counters / gauges / histograms"| PR
    PR --> GR

    API & SCH & WK -->|"spans"| OT
    OT -->|"OTLP gRPC :4317"| JA

    API & SCH & WK --> SL

    style Metrics fill:#1a3d2b,color:#fff,stroke:#4caf50
    style Traces fill:#1e3a5f,color:#fff,stroke:#4a90d9
    style Logs fill:#2d2000,color:#fff,stroke:#f59e0b
    style Services fill:#3b1f5e,color:#fff,stroke:#a855f7
```

---

## Project Structure

```
orion/
├── cmd/
│   ├── api/              # API server entrypoint
│   ├── scheduler/        # Scheduler entrypoint
│   └── worker/           # Worker entrypoint
├── internal/
│   ├── api/handler/      # HTTP handlers, DTOs
│   ├── config/           # Environment-driven config
│   ├── domain/           # Job, Worker, Pipeline types (zero external deps)
│   ├── observability/    # OTel setup, Prometheus, structured logging
│   ├── queue/            # Queue interface + Redis Streams implementation
│   ├── scheduler/        # Dispatch loop, leader election, orphan reclaim
│   ├── store/            # Store interface + PostgreSQL implementation
│   │   └── migrations/   # SQL migration files (golang-migrate)
│   ├── worker/           # Bounded worker pool, executor interface
│   └── k8s/              # Kubernetes Job launcher (client-go)
├── pkg/
│   └── retry/            # Exportable full-jitter backoff utilities
├── proto/                # .proto definitions + generated gRPC code
├── deploy/
│   ├── helm/             # Helm chart for Kubernetes deployment
│   └── docker/           # Per-service Dockerfiles
├── docs/
│   └── adr/              # Architecture Decision Records
├── docker-compose.yml
└── Makefile
```

---

## Getting Started

### Prerequisites
- Go 1.22+
- Docker + Docker Compose
- `golang-migrate` for schema migrations

### Local Development

```bash
# Start all infrastructure (Postgres, Redis, Jaeger, Prometheus, Grafana)
make infra-up

# Apply schema migrations
make migrate-up

# Run each service in a separate terminal
make run-api
make run-scheduler
make run-worker
```

### Submit a Job

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "train-mnist",
    "type": "k8s_job",
    "queue_name": "default",
    "priority": 7,
    "max_retries": 3,
    "idempotency_key": "run-2024-001",
    "payload": {
      "kubernetes_spec": {
        "image": "pytorch/pytorch:2.1.0-cuda11.8-cudnn8-runtime",
        "command": ["python", "train.py"],
        "namespace": "orion-jobs",
        "resources": { "cpu": "2000m", "memory": "4Gi" }
      }
    }
  }'
```

### Observability Endpoints

| Service    | URL                                 |
|------------|-------------------------------------|
| API Server | http://localhost:8080               |
| Prometheus | http://localhost:9090               |
| Grafana    | http://localhost:3000 (admin/admin) |
| Jaeger UI  | http://localhost:16686              |

---

## Key Engineering Decisions

See `docs/adr/` for full Architecture Decision Records:

- **ADR-001**: Redis Streams with consumer groups for at-least-once delivery
- **ADR-002**: PostgreSQL advisory locks for scheduler leader election

## Design Principles

1. **State transitions are atomic CAS operations** — `UPDATE WHERE status = expected_status` prevents concurrent mutation from multiple workers or schedulers
2. **Queue interface is abstract** — Redis can be swapped for NATS JetStream or Kafka without touching worker or scheduler logic
3. **Full-jitter backoff** — `random(0, min(cap, base × 2^attempt))` prevents thundering herds during retry storms
4. **Append-only execution log** — `job_executions` rows are never mutated; each attempt gets its own immutable record
5. **Idempotency keys** — clients retry job submissions safely; duplicate submissions return the original job
6. **Graceful shutdown** — `SIGTERM` stops accepting new work, drains in-flight jobs, then exits cleanly

---

## Development Roadmap

- [x] Phase 1: Domain types, interfaces, project structure
- [ ] Phase 2: PostgreSQL store implementation (`internal/store/postgres/`)
- [ ] Phase 3: Redis queue full implementation + PEL sweeper
- [ ] Phase 4: Worker pool + inline executor
- [ ] Phase 5: Kubernetes executor via client-go
- [ ] Phase 6: Scheduler + leader election wiring
- [ ] Phase 7: Observability instrumentation (spans on every layer)
- [ ] Phase 8: DAG pipeline support
- [ ] Phase 9: Helm chart + production hardening
