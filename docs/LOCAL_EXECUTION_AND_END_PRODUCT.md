# Orion Local Execution And End Product Guide

This guide answers the practical questions: what credentials are needed, how to
run Orion locally, how to submit real jobs, what dashboard/product you get, and
which parts are currently live versus mocked.

Use this alongside:

- `README.md` for the short project overview
- `docs/RUNBOOK.md` for deeper troubleshooting
- `docs/project_architecture_details.md` for architecture internals

## What You Get At The End

Running Orion locally gives you a full distributed job orchestration stack:

- API server on `http://localhost:8080`
- gRPC server on `localhost:9090`
- Scheduler process that dispatches queued jobs
- Worker process that executes inline jobs locally
- PostgreSQL database with durable jobs, workers, executions, pipelines, queue config
- Redis Streams for job delivery
- Jaeger UI for traces at `http://localhost:16686`
- Prometheus at `http://localhost:9090`
- Grafana at `http://localhost:3000`
- Next.js product dashboard at `http://localhost:3000` if you run frontend dev server, or a different port if Grafana is already using `3000`

Important distinction:

- **Grafana dashboard**: real metrics from Prometheus when API/scheduler/worker are running and scraped.
- **Next.js Orion dashboard**: polished product UI currently backed by mock/API-shaped data in `frontend/lib/api.ts`; it shows the intended end product but is not fully wired to live backend data yet.

## Local Credentials And Ports

Docker Compose defines these defaults in `docker-compose.yml`.

| Component | Host | Port | Username | Password | Database/Notes |
| --- | --- | ---: | --- | --- | --- |
| PostgreSQL | `localhost` | `5432` | `orion` | `orion` | database `orion` |
| Redis | `localhost` | `6380` | none | none | container port is `6379`; host port is `6380` |
| Jaeger | `localhost` | `16686` | none | none | UI |
| OTLP gRPC | `localhost` | `4317` | none | none | tracing endpoint |
| Prometheus | `localhost` | `9090` | none | none | UI |
| Grafana | `localhost` | `3000` | `admin` | `admin` | UI |
| Orion API | `localhost` | `8080` | none | none | REST |
| Orion gRPC | `localhost` | `9090` | none | none | conflicts with Prometheus only by meaning, not protocol if both bound to same host port; do not run both on 9090 in the same host setup |

Two local port gotchas:

- Redis is exposed as `localhost:6380`, while Orion's code default is `localhost:6379`. Set `ORION_REDIS_ADDR=localhost:6380` unless you also have Redis on `6379`.
- Prometheus and Orion gRPC both default to port `9090` in different configs. The Compose Prometheus service uses host `9090`; the API gRPC server also defaults to `9090`. If Prometheus is running, start the API with another gRPC port such as `ORION_GRPC_PORT=9095`.

## Prerequisites

Install:

- Go matching the repository's `go.mod` expectation
- Docker and Docker Compose
- `golang-migrate`
- `curl`
- `jq` recommended for readable JSON
- Node/npm for the frontend
- `grpcurl` optional for gRPC streaming tests
- `kubectl` and a Kubernetes cluster only for real `k8s_job` execution

Install migration and gRPC tools:

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

## Start Local Infrastructure

```bash
make infra-up
docker compose ps
```

Apply schema:

```bash
make migrate-up
```

If `make migrate-up` cannot connect, run the explicit DSN:

```bash
migrate -database "postgres://orion:orion@localhost:5432/orion?sslmode=disable" \
  -path ./internal/store/migrations up
```

## Environment For Local Services

The current `Makefile` does **not** automatically source `.env`; it only sets
`ORION_ENV=development` for `make run-*` targets. Export variables in your shell
or prefix each command.

Recommended local shell exports:

```bash
export ORION_DATABASE_DSN="postgres://orion:orion@localhost:5432/orion?sslmode=disable"
export ORION_REDIS_ADDR="localhost:6380"
export ORION_OTLP_ENDPOINT="localhost:4317"
export ORION_TRACING_TLS=false
export ORION_GRPC_PORT=9095
```

Use `ORION_OTLP_ENDPOINT=""` if you want to disable tracing completely.

## Run The Three Orion Services

Use three terminals.

Terminal 1:

```bash
ORION_REDIS_ADDR=localhost:6380 ORION_GRPC_PORT=9095 ORION_OTLP_ENDPOINT=localhost:4317 make run-api
```

Terminal 2:

```bash
ORION_REDIS_ADDR=localhost:6380 ORION_OTLP_ENDPOINT=localhost:4317 ORION_METRICS_PORT=9092 make run-scheduler
```

Terminal 3:

```bash
ORION_REDIS_ADDR=localhost:6380 ORION_OTLP_ENDPOINT=localhost:4317 ORION_METRICS_PORT=9093 make run-worker
```

Why separate metrics ports? Each binary starts its own Prometheus metrics server.
If all three use `9091`, the second and third process will fail to bind metrics.

Health checks:

```bash
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
```

Expected:

```json
{"status":"ok"}
{"status":"ready"}
```

## Submit Real Local Inline Jobs

Inline jobs run inside the worker process and do not require Kubernetes. These
are the best local smoke tests.

### Noop Job

```bash
JOB_ID=$(
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{
      "name": "local-noop",
      "type": "inline",
      "queue_name": "orion:queue:default",
      "priority": 5,
      "max_retries": 2,
      "idempotency_key": "local-noop-001",
      "payload": { "handler_name": "noop" }
    }' | jq -r .id
)
echo "$JOB_ID"
```

Watch it:

```bash
watch -n 1 "curl -s http://localhost:8080/jobs/$JOB_ID | jq '{id,name,status,attempt,worker_id,error_message}'"
```

Expected progression:

```text
queued -> scheduled -> running -> completed
```

### Echo Job With Args

```bash
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "local-echo",
    "type": "inline",
    "queue_name": "orion:queue:default",
    "priority": 5,
    "max_retries": 2,
    "payload": {
      "handler_name": "echo",
      "args": {
        "message": "hello orion",
        "dataset": "demo"
      }
    }
  }' | jq .
```

The worker logs should show the args reaching the handler.

### Slow Job For Cancellation/Deadline Testing

```bash
SLOW_ID=$(
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{
      "name": "local-slow",
      "type": "inline",
      "queue_name": "orion:queue:default",
      "priority": 5,
      "max_retries": 1,
      "payload": {
        "handler_name": "slow",
        "args": { "duration_seconds": 30 }
      }
    }' | jq -r .id
)
echo "$SLOW_ID"
```

Cancel it while running:

```bash
curl -s -X POST "http://localhost:8080/jobs/$SLOW_ID/cancel" | jq .
```

### Failing Job For Retry/Dead State

```bash
FAIL_ID=$(
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{
      "name": "local-failure",
      "type": "inline",
      "queue_name": "orion:queue:default",
      "priority": 5,
      "max_retries": 1,
      "payload": { "handler_name": "always_fail" }
    }' | jq -r .id
)
echo "$FAIL_ID"
```

Watch it:

```bash
watch -n 1 "curl -s http://localhost:8080/jobs/$FAIL_ID | jq '{status,attempt,max_retries,error_message,next_retry_at}'"
```

Expected behavior:

- worker runs the job
- handler returns an error
- job becomes `failed`
- scheduler promotes it back through retry flow after `next_retry_at`
- after retries are exhausted it should stop being useful work and become an
  operator-visible failure/dead path depending on the current worker/scheduler
  implementation

Check execution history:

```bash
curl -s "http://localhost:8080/jobs/$FAIL_ID/executions" | jq .
```

## Submit A Real Kubernetes Job

Kubernetes jobs require a real cluster and a kubeconfig. Inline jobs work without
this section.

Requirements:

- `kubectl` works against your cluster
- `KUBECONFIG` points to that cluster, or `~/.kube/config` exists
- worker can build a Kubernetes client
- Orion worker service account/RBAC exists if running worker inside Kubernetes

For local worker using your kubeconfig:

```bash
export ORION_K8S_IN_CLUSTER=false
export KUBECONFIG="$HOME/.kube/config"
export ORION_K8S_NAMESPACE=orion-jobs
```

Create namespaces/RBAC in the cluster:

```bash
kubectl apply -f deploy/k8s/rbac.yaml
kubectl auth can-i create jobs --as=system:serviceaccount:orion-system:orion-worker -n orion-jobs
```

Submit a smoke-test Kubernetes job:

```bash
K8S_ID=$(
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{
      "name": "k8s-smoke",
      "type": "k8s_job",
      "queue_name": "orion:queue:default",
      "priority": 5,
      "max_retries": 1,
      "payload": {
        "kubernetes_spec": {
          "image": "busybox:1.36",
          "command": ["sh", "-c", "echo hello from orion && sleep 2"],
          "namespace": "orion-jobs",
          "resources": { "cpu": "100m", "memory": "128Mi" },
          "ttl_seconds": 60
        }
      }
    }' | jq -r .id
)
echo "$K8S_ID"
```

Watch Kubernetes:

```bash
kubectl get jobs,pods -n orion-jobs -w
```

Watch Orion:

```bash
watch -n 1 "curl -s http://localhost:8080/jobs/$K8S_ID | jq '{status,attempt,error_message}'"
```

If the worker logs `kubernetes client unavailable`, it will not execute
`k8s_job` jobs. Restart the worker after fixing `KUBECONFIG` or cluster access.

## Test Pipelines Locally

Create an inline DAG pipeline:

```bash
PIPE_ID=$(
  curl -s -X POST http://localhost:8080/pipelines \
    -H "Content-Type: application/json" \
    -d '{
      "name": "local-inline-pipeline",
      "dag_spec": {
        "nodes": [
          { "id": "extract", "job_template": { "handler_name": "noop" } },
          { "id": "transform", "job_template": { "handler_name": "echo", "args": { "step": "transform" } } },
          { "id": "publish", "job_template": { "handler_name": "noop" } }
        ],
        "edges": [
          { "source": "extract", "target": "transform" },
          { "source": "transform", "target": "publish" }
        ]
      }
    }' | jq -r .id
)
echo "$PIPE_ID"
```

Watch pipeline:

```bash
watch -n 1 "curl -s http://localhost:8080/pipelines/$PIPE_ID | jq '{id,name,status,created_at,completed_at}'"
```

Watch pipeline node jobs:

```bash
watch -n 1 "curl -s http://localhost:8080/pipelines/$PIPE_ID/jobs | jq ."
```

## gRPC Streaming Test

Start API with a non-conflicting gRPC port, for example `ORION_GRPC_PORT=9095`.

```bash
grpcurl -plaintext localhost:9095 list
grpcurl -plaintext -d "{\"job_id\":\"$JOB_ID\"}" localhost:9095 orion.v1.JobService/WatchJob
```

`WatchJob` streams status transitions until the job reaches a terminal state.

## Inspect The Database And Redis

PostgreSQL:

```bash
docker exec -it orion-postgres psql -U orion -d orion
```

Useful SQL:

```sql
SELECT id, name, type, queue_name, status, attempt, worker_id, created_at, updated_at
FROM jobs
ORDER BY created_at DESC
LIMIT 10;

SELECT job_id, attempt, status, worker_id, started_at, finished_at, error
FROM job_executions
ORDER BY created_at DESC
LIMIT 20;

SELECT id, hostname, queue_names, concurrency, status, last_heartbeat
FROM workers
ORDER BY registered_at DESC;
```

Redis:

```bash
docker exec -it orion-redis redis-cli
XLEN orion:queue:default
XINFO GROUPS orion:queue:default
XRANGE orion:queue:default - + COUNT 5
```

## Run The Next.js Product Dashboard

Grafana uses port `3000`, and Next.js also defaults to `3000`. Use `3001` for
the Orion product dashboard when Grafana is running.

```bash
cd frontend
npm install
npm run dev -- -p 3001
```

Open:

```text
http://localhost:3001
http://localhost:3001/dashboard
```

Current dashboard routes:

| Route | What it shows |
| --- | --- |
| `/` | Orion home page and product entry |
| `/dashboard` | operations overview, active incidents, metrics cards, recent jobs, queue depth |
| `/dashboard/jobs` | jobs table with status, type, queue, priority, attempt, worker, errors |
| `/dashboard/pipelines` | pipeline cards with DAG preview |
| `/dashboard/queues` | queue cards with depth, token availability, rate, burst, weight, max concurrency |
| `/dashboard/workers` | worker capacity, heartbeat freshness, active slots, queue coverage |
| `/docs` | docs hub placeholder |

Current frontend status:

- Uses mock data from `frontend/lib/api.ts`.
- Does not yet persist form actions.
- Buttons like "Submit Job", "Create Pipeline", and "Save live config" are UI
  affordances prepared for live wiring.
- The UI is still valuable as the target end product: an operator console for
  jobs, pipelines, queues, workers, docs, and health.

Expected final product after live API wiring:

- dashboard overview fed by `GET /jobs`, `GET /queues`, `GET /workers`,
  `GET /pipelines`, `/healthz`, `/readyz`
- job submission form calling `POST /jobs`
- pipeline creation form calling `POST /pipelines`
- queue tuning form calling `PUT /queues/{name}`
- job cancel/replay actions calling `POST /jobs/{id}/cancel` and
  `POST /jobs/{id}/replay`
- detail pages for job execution history and pipeline node jobs
- optional streaming status via gRPC/WebSocket bridge or polling

## Grafana And Metrics Dashboard

Open Grafana:

```text
http://localhost:3000
login: admin / admin
```

The provisioned Grafana dashboard reads Prometheus metrics such as:

- job throughput
- failure rate
- job duration percentiles
- worker active jobs
- queue depth
- scheduler cycle latency
- retry/dead-job signals
- queue concurrency utilization

Prometheus targets:

```text
http://localhost:9090/targets
```

If targets are down, confirm the services are running on the ports Prometheus
expects. Local Prometheus config currently uses `host.docker.internal` targets.
On Linux, you may need to adapt the target host or run Prometheus with host
gateway support.

## Common Local Problems

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| API cannot connect to Redis | Redis host port is `6380`, not `6379` | set `ORION_REDIS_ADDR=localhost:6380` |
| API gRPC fails to bind `9090` | Prometheus already uses host port `9090` | set `ORION_GRPC_PORT=9095` |
| metrics server port conflict | all binaries default to `9091` | set scheduler/worker metrics ports to `9092`/`9093` |
| worker says Kubernetes unavailable | no usable kubeconfig | inline jobs still work; set `KUBECONFIG` for k8s jobs |
| frontend port conflict | Grafana uses `3000` | run Next.js with `npm run dev -- -p 3001` |
| job stuck `queued` | scheduler not running or no leader lock | check scheduler logs |
| job stuck `scheduled` | worker not running or Redis mismatch | check worker logs and `ORION_REDIS_ADDR` |
| no traces | OTLP endpoint wrong | use `ORION_OTLP_ENDPOINT=localhost:4317` or blank |

## Fast Happy Path Checklist

```bash
make infra-up
make migrate-up

# terminal 1
ORION_REDIS_ADDR=localhost:6380 ORION_GRPC_PORT=9095 ORION_OTLP_ENDPOINT=localhost:4317 make run-api

# terminal 2
ORION_REDIS_ADDR=localhost:6380 ORION_METRICS_PORT=9092 ORION_OTLP_ENDPOINT=localhost:4317 make run-scheduler

# terminal 3
ORION_REDIS_ADDR=localhost:6380 ORION_METRICS_PORT=9093 ORION_OTLP_ENDPOINT=localhost:4317 make run-worker

# terminal 4
curl -s http://localhost:8080/readyz
```

Then submit `local-noop` from this guide and watch it complete.

