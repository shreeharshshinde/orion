<div align="center">

<img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version">
<img src="https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square" alt="License">
<img src="https://img.shields.io/badge/Kubernetes-compatible-326CE5?style=flat-square&logo=kubernetes&logoColor=white" alt="Kubernetes">
<img src="https://img.shields.io/badge/OpenTelemetry-instrumented-f5a800?style=flat-square&logo=opentelemetry&logoColor=white" alt="OpenTelemetry">
<img src="https://img.shields.io/badge/gRPC-streaming-244c5a?style=flat-square&logo=grpc" alt="gRPC">
<img src="https://img.shields.io/badge/status-beta-orange?style=flat-square" alt="Status">

# Orion

**Distributed ML Job Orchestrator for Kubernetes**

Orion schedules, executes, and monitors machine learning workloads on Kubernetes.  
Priority queues · At-least-once delivery · DAG pipelines · Full observability stack.

[Getting Started](#getting-started) · [Architecture](#architecture) · [API Reference](#api-reference) · [Deployment](#deployment) · [Contributing](CONTRIBUTING.md)

</div>

---

## Overview

Orion is a cloud-native job orchestration platform built for ML infrastructure teams. It provides a durable, observable execution layer between your training pipelines and Kubernetes — handling scheduling, retries, backpressure, and real-time status streaming so your application code doesn't have to.

**Core capabilities:**

- **Priority scheduling** — three queues (`high`, `default`, `low`) with weighted dispatch and per-queue rate limiting
- **At-least-once delivery** — Redis Streams consumer groups with Pending Entry List (PEL) tracking; no job is silently dropped
- **Kubernetes-native execution** — launches K8s Jobs via `client-go`; supports GPU resource requests, custom namespaces, and service accounts
- **DAG pipelines** — define multi-step workflows with dependency graphs; automatic topological advancement and cascade-cancel on failure
- **Real-time streaming** — gRPC `WatchJob` / `WatchPipeline` driven by PostgreSQL `LISTEN/NOTIFY`; zero polling overhead
- **Full observability** — OpenTelemetry traces on every layer, Prometheus metrics, structured `slog` JSON logs with `trace_id` correlation
- **Idempotent submission** — clients retry safely; duplicate submissions return the original job
- **Graceful shutdown** — `SIGTERM` drains in-flight jobs before exit; no mid-execution kills

---

## Architecture

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
    end

    subgraph WP ["Worker Pool"]
        DQ["Dequeue Loop"]
        CH["jobCh (buffered)"]
        W1["Worker 1"]
        W2["Worker N"]

        subgraph EX ["Executor Interface"]
            IE["InlineExecutor"]
            KE["KubernetesExecutor"]
        end
    end

    subgraph K8S ["Kubernetes"]
        KJ["K8s Job"]
        KP["Pod"]
    end

    subgraph OBS ["Observability"]
        PR["Prometheus :9091"]
        JG["Jaeger :16686"]
        GR["Grafana :3000"]
        OT["OpenTelemetry Collector"]
    end

    Client -->|"POST /jobs"| AH
    AH --> AV
    AV -->|"INSERT job"| JT
    AH -->|"201 job_id"| Client

    LE -->|"pg_try_advisory_lock"| DB
    SD -->|"SELECT queued jobs"| JT
    SD -->|"UPDATE status=scheduled (CAS)"| JT
    SD -->|"XADD"| QD
    OR -->|"reclaim stale running jobs"| JT
    RP -->|"promote failed → queued"| JT

    DQ -->|"XREADGROUP"| QD & QH & QL
    DQ --> CH
    CH --> W1 & W2

    W1 & W2 --> IE
    W1 & W2 --> KE
    KE -->|"Create Job"| KJ
    KJ --> KP

    W1 -->|"UPDATE status / INSERT execution"| DB
    W2 -->|"Heartbeat"| WT

    WP -->|"metrics"| PR
    API & WP & SCH -->|"spans"| OT
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

<img width="2468" height="2019" alt="Orion architecture diagram" src="https://github.com/user-attachments/assets/451b8db7-eb89-4ae0-84b8-86597aefd788" />

### Job Lifecycle

```mermaid
stateDiagram-v2
    direction LR
    [*] --> queued : submit job
    queued --> scheduled : scheduler dispatches (CAS)
    queued --> cancelled : client cancels
    scheduled --> running : worker claims job
    scheduled --> queued : scheduler rollback
    scheduled --> cancelled : client cancels
    running --> completed : execution success
    running --> failed : error or deadline exceeded
    failed --> retrying : attempt < max_retries
    failed --> dead : attempt >= max_retries
    retrying --> queued : re-enqueued with backoff
    completed --> [*]
    dead --> [*]
    cancelled --> [*]
```

State transitions are **atomic CAS operations** (`UPDATE WHERE status = expected`). Concurrent schedulers and workers cannot double-claim a job.

### Retry & Backoff

Failed jobs use **full-jitter exponential backoff**: `delay = random(0, min(cap, base × 2^attempt))`. This prevents thundering herds during retry storms. Dead jobs (exhausted retries) are moved to `orion:queue:dead` for manual inspection or replay.

### Backpressure

The worker pool's `jobCh` channel capacity equals `Concurrency`. When all workers are busy, the dequeue goroutine blocks on send — stopping Redis reads. No jobs are prefetched beyond what can be immediately executed.

---

## Getting Started

### Prerequisites

| Requirement | Version |
|-------------|---------|
| Go | 1.22+ |
| Docker + Compose | v2+ |
| `golang-migrate` | latest |

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

### Quickstart

```bash
# 1. Clone
git clone https://github.com/shreeharshshinde/orion.git && cd orion

# 2. Start infrastructure (Postgres, Redis, Jaeger, Prometheus, Grafana)
make infra-up

# 3. Apply schema migrations
make migrate-up

# 4. Run services (three terminals)
make run-api
make run-scheduler
make run-worker
```

### Submit Your First Job

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "train-resnet",
    "type": "k8s_job",
    "queue_name": "high",
    "priority": 8,
    "max_retries": 3,
    "idempotency_key": "run-2026-001",
    "payload": {
      "kubernetes_spec": {
        "image": "pytorch/pytorch:2.1.0-cuda11.8-cudnn8-runtime",
        "command": ["python", "train.py", "--epochs", "50"],
        "namespace": "orion-jobs",
        "resources": { "cpu": "4000m", "memory": "16Gi", "gpu": 1 }
      }
    }
  }'
```

Watch it in real time via gRPC streaming:

```bash
grpcurl -plaintext -d '{"job_id": "<job_id>"}' \
  localhost:9090 orion.v1.JobService/WatchJob
```

---

## API Reference

### HTTP REST

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/jobs` | Submit a job (idempotent) |
| `GET` | `/jobs` | List jobs with filters |
| `GET` | `/jobs/{id}` | Get job by ID |
| `GET` | `/jobs/{id}/executions` | Get execution history |
| `POST` | `/jobs/{id}/cancel` | Cancel a queued or running job |
| `POST` | `/pipelines` | Create a DAG pipeline |
| `GET` | `/pipelines/{id}` | Get pipeline status |
| `GET` | `/pipelines/{id}/jobs` | Get pipeline node statuses |
| `GET` | `/queues` | List queue configurations |
| `PUT` | `/queues/{name}` | Update queue config (live reload) |
| `GET` | `/queues/{name}/stats` | Queue depth + rate limiter state |
| `GET` | `/healthz` | Liveness probe |
| `GET` | `/readyz` | Readiness probe (checks DB) |

### gRPC

Service definition: [`proto/orion/v1/jobs.proto`](proto/orion/v1/jobs.proto)

| RPC | Type | Description |
|-----|------|-------------|
| `SubmitJob` | Unary | Submit a job |
| `GetJob` | Unary | Get job by ID |
| `WatchJob` | Server-streaming | Stream status events until terminal state |
| `WatchPipeline` | Server-streaming | Stream pipeline events until terminal state |

`WatchJob` is driven by PostgreSQL `LISTEN/NOTIFY` — every `TransitionJobState` call fires `pg_notify('orion_job_events', ...)`, which the in-process notifier receives and fans out to subscribers. No polling.

---

## DAG Pipelines

Define multi-step workflows as a directed acyclic graph. Orion advances nodes topologically, launching each job only when all its dependencies have completed.

```json
{
  "name": "resnet-training-pipeline",
  "dag_spec": {
    "nodes": [
      { "id": "preprocess", "job_template": { "name": "preprocess-data", "type": "k8s_job" } },
      { "id": "train",      "job_template": { "name": "train-resnet",    "type": "k8s_job" }, "depends_on": ["preprocess"] },
      { "id": "evaluate",   "job_template": { "name": "evaluate-model",  "type": "k8s_job" }, "depends_on": ["train"] }
    ]
  }
}
```

If any node reaches `dead` status, downstream nodes are cascade-cancelled and the pipeline transitions to `failed`.

---

## Observability

| Signal | Tool | Endpoint |
|--------|------|----------|
| Metrics | Prometheus + Grafana | `:9090` / `:3000` |
| Traces | OpenTelemetry → Jaeger | `:16686` |
| Logs | `slog` JSON (stdout) | — |

**Key metrics:**

| Metric | Type | Description |
|--------|------|-------------|
| `orion_jobs_submitted_total` | Counter | Jobs submitted, by queue and type |
| `orion_job_duration_seconds` | Histogram | End-to-end job execution time |
| `orion_queue_depth` | Gauge | Pending messages per queue |
| `orion_worker_active_jobs` | Gauge | In-flight jobs per worker |
| `orion_scheduler_cycle_duration_seconds` | Histogram | Scheduler dispatch loop latency |

Every log line carries `trace_id`, `span_id`, `job_id`, and `worker_id` for correlation across signals.

---

## Deployment

### Helm (Kubernetes)

```bash
helm install orion ./deploy/helm \
  --namespace ml-platform \
  --create-namespace \
  --set database.dsn="postgres://orion:orion@postgres:5432/orion" \
  --set redis.addr="redis:6379"
```

See [`deploy/helm/`](deploy/helm/) for full values reference.

### Docker Compose (local / CI)

```bash
make infra-up      # start all infrastructure
make migrate-up    # apply schema
make build         # compile all binaries
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ORION_DATABASE_DSN` | — | PostgreSQL connection string |
| `ORION_REDIS_ADDR` | `localhost:6379` | Redis address |
| `ORION_HTTP_PORT` | `8080` | API server HTTP port |
| `ORION_GRPC_PORT` | `9090` | API server gRPC port |
| `ORION_WORKER_CONCURRENCY` | `10` | Max concurrent jobs per worker |
| `ORION_OTLP_ENDPOINT` | `localhost:4317` | OpenTelemetry collector gRPC endpoint |
| `ORION_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |

Full reference: [`.env.example`](.env.example)

---

## Project Structure

```
orion/
├── cmd/
│   ├── api/              # API server entrypoint
│   ├── scheduler/        # Scheduler entrypoint
│   └── worker/           # Worker entrypoint
├── internal/
│   ├── api/
│   │   ├── grpc/         # gRPC server, Broadcaster, PG Notifier
│   │   └── handler/      # HTTP handlers, middleware, DTOs
│   ├── config/           # Environment-driven config (no viper, stdlib only)
│   ├── domain/           # Core types: Job, Worker, Pipeline (zero deps)
│   ├── k8s/              # Kubernetes Job launcher (client-go)
│   ├── observability/    # OTel setup, Prometheus registry, slog
│   ├── pipeline/         # DAG advancement engine
│   ├── queue/            # Queue interface + Redis Streams implementation
│   ├── scheduler/        # Dispatch loop, leader election, orphan reclaimer
│   ├── store/            # Store interface + PostgreSQL implementation
│   │   └── migrations/   # SQL migration files (golang-migrate)
│   └── worker/           # Bounded worker pool, executor interface
├── pkg/
│   └── retry/            # Exportable full-jitter backoff (no internal deps)
├── proto/orion/v1/       # .proto definitions + generated gRPC stubs
├── deploy/
│   ├── helm/             # Helm chart
│   ├── docker/           # Per-service Dockerfiles
│   ├── grafana/          # Dashboard + datasource provisioning
│   └── prometheus/       # Scrape config
├── docs/
│   ├── adr/              # Architecture Decision Records (ADR-001 – ADR-007)
│   └── phases/           # Per-phase implementation notes
├── CHANGELOG.md
├── CONTRIBUTING.md
├── SECURITY.md
├── docker-compose.yml
└── Makefile
```

---

## Design Decisions

Seven Architecture Decision Records document the key choices. Summaries:

| ADR | Decision |
|-----|----------|
| [ADR-001](docs/adr/ADR-001-queue-design.md) | Redis Streams with consumer groups for at-least-once delivery |
| [ADR-002](docs/adr/ADR-002-leader-election.md) | PostgreSQL advisory locks for scheduler leader election |
| [ADR-003](docs/adr/ADR-003-cas-state-transitions.md) | CAS `UPDATE WHERE status = expected` for safe concurrent transitions |
| [ADR-004](docs/adr/ADR-004-buffered-jobch-backpressure.md) | Buffered `jobCh` channel as the backpressure boundary |
| [ADR-005](docs/adr/ADR-005-kubernetes-interface-testability.md) | Executor interface for K8s testability without a live cluster |
| [ADR-006](docs/adr/ADR-006-k8s-backofflimit-restartpolicy.md) | K8s Job `backoffLimit=0` + `restartPolicy=Never` — Orion owns retries |
| [ADR-007](docs/adr/ADR-007-jsonb-payload-dagspec.md) | JSONB for job payload and DAG spec — schema-free evolution |

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, code style, testing, and PR guidelines.

```bash
make check              # fmt + vet + lint + unit tests
make test-integration   # spins Docker infra, runs integration suite
```

---

## Security

See [SECURITY.md](SECURITY.md) for the vulnerability disclosure policy and operator hardening guidance.

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
