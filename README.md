<div align="center">

<img width="400"  alt="Orion-Icon" src="https://github.com/user-attachments/assets/667e235d-8703-4fbd-82cd-e14223d93c24" />

# Orion

**Distributed ML job orchestrator for Kubernetes**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue?style=flat-square)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/shreeharshshinde/orion?style=flat-square)](https://goreportcard.com/report/github.com/shreeharshshinde/orion)
[![OpenTelemetry](https://img.shields.io/badge/OpenTelemetry-instrumented-f5a800?style=flat-square&logo=opentelemetry&logoColor=white)](https://opentelemetry.io)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-compatible-326CE5?style=flat-square&logo=kubernetes&logoColor=white)](https://kubernetes.io)
[![Status](https://img.shields.io/badge/status-beta-orange?style=flat-square)]()

[Overview](#overview) · [Quickstart](#quickstart) · [Architecture](#architecture) · [API](#api) · [Deployment](#deployment) · [Contributing](CONTRIBUTING.md)

</div>

---

Orion is a durable job execution platform that schedules, runs, and monitors machine learning workloads on Kubernetes. It sits between your training pipelines and the cluster — handling priority queuing, retries, backpressure, DAG orchestration, and real-time status streaming so your application code doesn't have to.

![architecture](assets/architecture.png)

---

## Overview

| Capability | How it works |
|---|---|
| **Priority queues** | Three Redis Streams queues (`high`, `default`, `low`) with weighted dispatch and per-queue rate limiting |
| **At-least-once delivery** | Redis Streams consumer groups + PEL tracking; unacknowledged jobs are reclaimed by the orphan sweeper |
| **Kubernetes execution** | Launches K8s Jobs via `client-go`; supports GPU requests, custom namespaces, and service accounts |
| **DAG pipelines** | Topological advancement with cascade-cancel on node failure |
| **Real-time streaming** | gRPC `WatchJob` / `WatchPipeline` driven by PostgreSQL `LISTEN/NOTIFY` — zero polling |
| **Idempotent submission** | Clients retry safely; duplicate `idempotency_key` returns the original job |
| **Graceful shutdown** | `SIGTERM` drains in-flight jobs before exit |

### Job lifecycle

![job_lifecycle](assets/job_lifecycle.png)


State transitions are atomic CAS operations (`UPDATE … WHERE status = expected`). Concurrent schedulers and workers cannot double-claim a job.

Retries use **full-jitter exponential backoff**: `delay = random(0, min(cap, base × 2ⁿ))`.

---

## Quickstart

**Prerequisites:** Go 1.22+, Docker Compose v2, [`golang-migrate`](https://github.com/golang-migrate/migrate)

```bash
git clone https://github.com/shreeharshshinde/orion.git && cd orion
make infra-up       # Postgres · Redis · Jaeger · Prometheus · Grafana
make migrate-up     # apply schema
make run-api        # :8080 HTTP  :9090 gRPC
make run-scheduler  # leader-elected dispatch loop
make run-worker     # bounded worker pool
```

Submit a job:

```bash
curl -sX POST http://localhost:8080/jobs \
  -H 'Content-Type: application/json' \
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

Stream status events in real time:

```bash
grpcurl -plaintext -d '{"job_id": "<id>"}' \
  localhost:9090 orion.v1.JobService/WatchJob
```

---

## Architecture

### Components

| Component | Responsibility |
|---|---|
| **API Server** | HTTP REST + gRPC; idempotency checks; job and pipeline CRUD |
| **Scheduler** | Leader-elected dispatch loop; orphan reclaimer; retry promoter |
| **Worker Pool** | Bounded goroutine pool; dequeues from Redis; runs executors |
| **InlineExecutor** | Runs registered Go handler functions in-process |
| **KubernetesExecutor** | Creates K8s Jobs via `client-go`; polls for completion |
| **PG Notifier** | Listens on `orion_job_events` channel; fans out to gRPC subscribers |

### Backpressure

`jobCh` channel capacity equals `Concurrency`. When all workers are busy, the dequeue goroutine blocks — stopping Redis reads. No jobs are prefetched beyond what can be immediately executed.

### Leader election

The scheduler acquires a PostgreSQL advisory lock (`pg_try_advisory_lock`) on a dedicated connection held for the entire leader tenure. Only one scheduler instance dispatches at a time; others stand by and take over within one poll interval on failure. The dedicated connection ensures the lock cannot be silently released by connection pool rotation.

---

## API

### HTTP

| Method | Path | Description |
|---|---|---|
| `POST` | `/jobs` | Submit a job (idempotent) |
| `GET` | `/jobs` | List jobs |
| `GET` | `/jobs/{id}` | Get job |
| `GET` | `/jobs/{id}/executions` | Execution history |
| `POST` | `/jobs/{id}/cancel` | Cancel queued/scheduled job; signals running job via Redis pub/sub |
| `POST` | `/jobs/{id}/replay` | Re-enqueue a `dead` or `failed` job |
| `DELETE` | `/jobs/{id}` | Delete a job record (not allowed for running/scheduled) |
| `POST` | `/pipelines` | Create DAG pipeline |
| `GET` | `/pipelines/{id}` | Pipeline status |
| `GET` | `/pipelines/{id}/jobs` | Node statuses |
| `POST` | `/pipelines/{id}/cancel` | Cancel a pending or running pipeline |
| `GET` | `/queues` | List queue configs |
| `PUT` | `/queues/{name}` | Update queue config (live reload) |
| `GET` | `/queues/{name}/stats` | Depth + rate limiter state |
| `GET` | `/workers` | List active workers |
| `GET` | `/healthz` | Liveness |
| `GET` | `/readyz` | Readiness (checks DB) |

### gRPC — `orion.v1.JobService`

| RPC | Type | Description |
|---|---|---|
| `SubmitJob` | Unary | Submit a job |
| `GetJob` | Unary | Get job by ID |
| `WatchJob` | Server-streaming | Events until terminal state |
| `WatchPipeline` | Server-streaming | Pipeline events until terminal state |

Proto definition: [`proto/orion/v1/jobs.proto`](proto/orion/v1/jobs.proto)

---

## DAG Pipelines

```json
{
  "name": "resnet-pipeline",
  "dag_spec": {
    "nodes": [
      { "id": "preprocess", "job_template": { "name": "preprocess", "type": "k8s_job" } },
      { "id": "train",      "job_template": { "name": "train",      "type": "k8s_job" }, "depends_on": ["preprocess"] },
      { "id": "evaluate",   "job_template": { "name": "evaluate",   "type": "k8s_job" }, "depends_on": ["train"] }
    ]
  }
}
```

Orion advances nodes topologically. If any node reaches `dead`, downstream nodes are cascade-cancelled (explicit `cancelled` job records are created) and the pipeline transitions to `failed`.

---

## Observability

| Signal | Stack | Endpoint |
|---|---|---|
| Metrics | Prometheus + Grafana | `:9090` / `:3000` |
| Traces | OpenTelemetry → Jaeger | `:16686` |
| Logs | `slog` JSON to stdout | — |

Every log line carries `trace_id`, `span_id`, `job_id`, and `worker_id`.

Key metrics: `orion_jobs_submitted_total` · `orion_job_duration_seconds` · `orion_queue_depth` · `orion_worker_active_jobs` · `orion_scheduler_cycle_duration_seconds`

---

## Deployment

### Helm

```bash
helm install orion ./deploy/helm \
  --namespace ml-platform --create-namespace \
  --set database.dsn="postgres://orion:orion@postgres:5432/orion" \
  --set redis.addr="redis:6379"
```

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `ORION_DATABASE_DSN` | — | PostgreSQL DSN |
| `ORION_REDIS_ADDR` | `localhost:6379` | Redis address |
| `ORION_HTTP_PORT` | `8080` | HTTP port |
| `ORION_GRPC_PORT` | `9090` | gRPC port |
| `ORION_WORKER_CONCURRENCY` | `10` | Max concurrent jobs per worker |
| `ORION_WORKER_QUEUES` | `orion:queue:high,orion:queue:default,orion:queue:low` | Comma-separated queue names for the worker to consume |
| `ORION_OTLP_ENDPOINT` | `localhost:4317` | OTel collector endpoint |
| `ORION_LOG_LEVEL` | `info` | `debug` · `info` · `warn` · `error` |

See [`.env.example`](.env.example) for the full reference.

---

## Design decisions

| ADR | Decision |
|---|---|
| [ADR-001](docs/adr/ADR-001-queue-design.md) | Redis Streams + consumer groups for at-least-once delivery |
| [ADR-002](docs/adr/ADR-002-leader-election.md) | PostgreSQL advisory locks for scheduler leader election |
| [ADR-003](docs/adr/ADR-003-cas-state-transitions.md) | CAS `UPDATE WHERE status = expected` for concurrent-safe transitions |
| [ADR-004](docs/adr/ADR-004-buffered-jobch-backpressure.md) | Buffered `jobCh` as the backpressure boundary |
| [ADR-005](docs/adr/ADR-005-kubernetes-interface-testability.md) | Executor interface for K8s testability without a live cluster |
| [ADR-006](docs/adr/ADR-006-k8s-backofflimit-restartpolicy.md) | K8s `backoffLimit=0` + `restartPolicy=Never` — Orion owns retries |
| [ADR-007](docs/adr/ADR-007-jsonb-payload-dagspec.md) | JSONB for payload and DAG spec — schema-free evolution |

---

## Contributing

```bash
make check            # fmt · vet · lint · unit tests
make test-integration # spins Docker infra, runs integration suite
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, code style, and PR guidelines.

## Security

See [SECURITY.md](SECURITY.md) for the vulnerability disclosure policy.

## License

[Apache 2.0](LICENSE)
