# Orion — Deployment Plan

Two paths: **Local** (demo on your machine) and **Cloud** (share with others via a public URL).
Pick the one that fits your goal.

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Docker + Docker Compose | 24+ | https://docs.docker.com/get-docker |
| Go | 1.22+ | https://go.dev/dl |
| `golang-migrate` | latest | `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest` |
| `kubectl` | 1.28+ | only for K8s path |
| `helm` | 3.14+ | only for K8s path |

---

## Path A — Local Demo (Docker Compose)

The fastest way to run the full stack and show it to someone sitting next to you.

### Step 1 — Environment

```bash
cp .env.example .env
# .env defaults work as-is for local; no edits needed
```

### Step 2 — Start infrastructure

```bash
make infra-up
# Starts: PostgreSQL :5432, Redis :6379, Jaeger :16686,
#         Prometheus :9090, Grafana :3000
```

Wait until all containers are healthy:

```bash
docker compose ps
# All services should show "healthy" or "running"
```

### Step 3 — Apply migrations

```bash
make migrate-up
```

### Step 4 — Run the three services

Open three terminals (or use tmux/screen):

```bash
# Terminal 1
make run-api

# Terminal 2
make run-scheduler

# Terminal 3
make run-worker
```

Or build binaries first and run them directly:

```bash
make build
./build/orion-api &
./build/orion-scheduler &
./build/orion-worker &
```

### Step 5 — Verify everything is up

```bash
# Health check
curl http://localhost:8080/health

# Submit a test job
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "demo-job",
    "type": "inline",
    "queue_name": "default",
    "priority": 5,
    "max_retries": 2,
    "idempotency_key": "demo-001",
    "payload": {}
  }'

# Check job status (replace <job_id> with the returned id)
curl http://localhost:8080/jobs/<job_id>
```

### Observability URLs

| Service | URL | Credentials |
|---------|-----|-------------|
| API | http://localhost:8080 | — |
| Prometheus | http://localhost:9090 | — |
| Grafana | http://localhost:3000 | admin / admin |
| Jaeger UI | http://localhost:16686 | — |

### Tear down

```bash
make infra-down
# Add -v to also delete volumes: docker compose down -v
```

---

## Path B — Cloud / Shareable (Docker images + a VPS or managed K8s)

Use this when you want to share a live URL with others.

### Option B1 — Single VPS (simplest public demo)

Good for: showing to recruiters, friends, or a small team.

**Requirements:** Any Linux VPS with 2 vCPU / 4 GB RAM (e.g. DigitalOcean Droplet, Hetzner CX22, AWS t3.medium).

#### 1. Provision the server

```bash
# On the VPS — install Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
```

#### 2. Copy the project

```bash
# From your machine
scp -r /home/shreeharsh157/Desktop/orion user@<VPS_IP>:~/orion
# or git clone if the repo is on GitHub
```

#### 3. Configure environment

```bash
# On the VPS
cd ~/orion
cp .env.example .env
# Edit .env — change at minimum:
#   ORION_ENV=production
#   ORION_DATABASE_DSN=postgres://orion:orion@localhost:5432/orion?sslmode=disable
#   ORION_REDIS_ADDR=localhost:6379
#   ORION_OTLP_ENDPOINT=http://localhost:4317
```

#### 4. Start infra + services

```bash
make infra-up
make migrate-up
make build

# Run as background processes (or use systemd units — see below)
nohup ./build/orion-api      > /var/log/orion-api.log 2>&1 &
nohup ./build/orion-scheduler > /var/log/orion-scheduler.log 2>&1 &
nohup ./build/orion-worker   > /var/log/orion-worker.log 2>&1 &
```

#### 5. Open firewall ports

```bash
# Allow HTTP API and observability UIs
ufw allow 8080   # API
ufw allow 3000   # Grafana
ufw allow 16686  # Jaeger
ufw allow 9090   # Prometheus
```

Access via `http://<VPS_IP>:8080`.

---

### Option B2 — Kubernetes via Helm (production-grade demo)

Use this to demonstrate the full K8s deployment story.

**Requirements:** A K8s cluster (minikube, kind, or a managed cluster like GKE/EKS/AKS) with `kubectl` and `helm` configured.

#### 1. Build and push Docker images

```bash
# Set your registry (GitHub Container Registry, Docker Hub, etc.)
export REGISTRY=ghcr.io/<your-username>/orion

make docker-push REGISTRY=$REGISTRY
# This builds orion-api, orion-scheduler, orion-worker and pushes them
```

#### 2. Provision PostgreSQL and Redis

For a quick demo, use Helm charts:

```bash
# PostgreSQL
helm repo add bitnami https://charts.bitnami.com/bitnami
helm install orion-pg bitnami/postgresql \
  --set auth.username=orion \
  --set auth.password=orion \
  --set auth.database=orion \
  --namespace ml-platform --create-namespace

# Redis
helm install orion-redis bitnami/redis \
  --set auth.enabled=false \
  --namespace ml-platform
```

Get the connection strings:

```bash
# Postgres DSN
export DB_DSN="postgres://orion:orion@orion-pg-postgresql.ml-platform.svc.cluster.local:5432/orion?sslmode=disable"

# Redis address
export REDIS_ADDR="orion-redis-master.ml-platform.svc.cluster.local:6379"
```

#### 3. Run database migrations

```bash
kubectl apply -f deploy/k8s/migrate-job.yaml
kubectl wait --for=condition=complete job/orion-migrate -n ml-platform --timeout=60s
```

#### 4. Apply RBAC

```bash
kubectl apply -f deploy/k8s/rbac.yaml
```

#### 5. Install Orion via Helm

```bash
make helm-install \
  DB_DSN="$DB_DSN" \
  REDIS_ADDR="$REDIS_ADDR" \
  HELM_NS=ml-platform
```

Check rollout:

```bash
kubectl get pods -n ml-platform
kubectl rollout status deployment/orion-api -n ml-platform
```

#### 6. Expose the API

```bash
# Port-forward for local access
kubectl port-forward svc/orion-api 8080:8080 -n ml-platform

# Or create a LoadBalancer service / Ingress for public access
kubectl expose deployment orion-api --type=LoadBalancer --port=8080 -n ml-platform
```

#### 7. Upgrade after code changes

```bash
make docker-push REGISTRY=$REGISTRY
make helm-upgrade HELM_NS=ml-platform
```

---

## Smoke Test Checklist

Run these after any deployment to confirm the system is working end-to-end.

```bash
BASE=http://localhost:8080   # or your public IP/domain

# 1. Health
curl -sf $BASE/health | jq .

# 2. Submit inline job
JOB=$(curl -sf -X POST $BASE/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke","type":"inline","queue_name":"default","priority":5,"max_retries":1,"idempotency_key":"smoke-001","payload":{}}')
echo $JOB | jq .
JOB_ID=$(echo $JOB | jq -r .id)

# 3. Poll until completed (should take < 5s)
for i in $(seq 1 10); do
  STATUS=$(curl -sf $BASE/jobs/$JOB_ID | jq -r .status)
  echo "Status: $STATUS"
  [ "$STATUS" = "completed" ] && break
  sleep 1
done

# 4. Idempotency — same key returns same job
curl -sf -X POST $BASE/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke","type":"inline","queue_name":"default","priority":5,"max_retries":1,"idempotency_key":"smoke-001","payload":{}}' \
  | jq .id   # must match $JOB_ID

# 5. List jobs
curl -sf "$BASE/jobs?limit=5" | jq .

# 6. Prometheus metrics
curl -sf http://localhost:9091/metrics | grep orion_jobs_submitted_total
```

---

## Troubleshooting

**Services can't connect to Postgres/Redis**
- Check `docker compose ps` — all infra containers must be healthy before starting services.
- Verify `ORION_DATABASE_DSN` and `ORION_REDIS_ADDR` in `.env` match the running containers.

**Migrations fail**
- Ensure `golang-migrate` is installed: `migrate -version`
- Check Postgres is reachable: `psql postgres://orion:orion@localhost:5432/orion`

**Jobs stuck in `scheduled` state**
- The worker is not running or not connected to Redis. Check worker logs.
- Verify `ORION_REDIS_ADDR` is correct.

**Grafana shows no data**
- Prometheus must be scraping the services. Check `http://localhost:9090/targets`.
- Services expose metrics on port `9091` — ensure `ORION_METRICS_PORT=9091` is set.

**Helm install fails**
- Run `make helm-lint` first to catch template errors.
- Confirm the image registry is accessible from the cluster: `kubectl describe pod <pod> -n ml-platform`.

---

## What to Show During a Demo

1. **Submit a job** via `curl` or Postman → show `201 Created` with `job_id`
2. **Watch it move** through states: `queued → scheduled → running → completed`
3. **Grafana dashboard** — live job throughput, queue depth, worker active jobs
4. **Jaeger traces** — drill into a single job's trace across API → scheduler → worker
5. **Retry behaviour** — submit a job that fails, watch it retry with backoff
6. **Idempotency** — submit the same `idempotency_key` twice, show same `job_id` returned
7. **Dead letter** — exhaust retries, show job in `dead` state and `orion:queue:dead` stream
