# Phase 9 Implementation Record
## Helm Chart + Production Kubernetes Deployment

> **What this document is:** A complete record of what was built in Phase 9, why each decision was made, and how to verify the deployment. Written after the fact as a reference for operators and future contributors.

---

## What Phase 9 Added

Before Phase 9, Orion ran via three `make run-*` commands on a developer's machine. After Phase 9:

```
helm install orion ./deploy/helm \
  --namespace ml-platform \
  --create-namespace \
  --set database.existingSecret=orion-db-credentials

# Deploys:
#   3 × orion-api pods       (HTTP :8080, gRPC :9090, metrics :9091)
#   3 × orion-scheduler pods (1 active via PG advisory lock, 2 hot standbys)
#   5 × orion-worker pods    (autoscales 2–50 on queue depth)
#   HPA, PDBs, RBAC, ConfigMap, Secret, Services — all wired
```

---

## Files Created

Phase 9 adds 17 new files. Zero existing source files were modified.

### Dockerfiles

| File | Purpose |
|---|---|
| `deploy/docker/Dockerfile.api` | Multi-stage build for the API server binary |
| `deploy/docker/Dockerfile.scheduler` | Multi-stage build for the scheduler binary |
| `deploy/docker/Dockerfile.worker` | Multi-stage build for the worker binary |

### Helm Chart

| File | Purpose |
|---|---|
| `deploy/helm/Chart.yaml` | Chart metadata: name, version, appVersion, maintainers |
| `deploy/helm/values.yaml` | All default configuration — replicas, images, resources, queues, observability |
| `deploy/helm/.helmignore` | Files excluded from `helm package` |
| `deploy/helm/templates/_helpers.tpl` | Shared name/label helpers used by all templates |
| `deploy/helm/templates/configmap.yaml` | Non-sensitive env vars for all three services |
| `deploy/helm/templates/secret.yaml` | DB DSN + Redis password (conditional on `existingSecret`) |
| `deploy/helm/templates/api-deployment.yaml` | API Deployment + ClusterIP Service + PodDisruptionBudget |
| `deploy/helm/templates/scheduler-deployment.yaml` | Scheduler Deployment + metrics Service + PodDisruptionBudget |
| `deploy/helm/templates/worker-deployment.yaml` | Worker Deployment + metrics Service |
| `deploy/helm/templates/hpa.yaml` | HorizontalPodAutoscaler for workers (conditional) |
| `deploy/helm/templates/rbac.yaml` | 3 ServiceAccounts + ClusterRole + ClusterRoleBinding + job Namespace |
| `deploy/helm/templates/servicemonitor.yaml` | Prometheus ServiceMonitor (conditional on prometheus-operator) |

### Kubernetes Manifests

| File | Purpose |
|---|---|
| `deploy/k8s/migrate-job.yaml` | One-shot Job to run database migrations before first deploy |

### Makefile Additions

New targets added to the existing `Makefile`:

| Target | What it does |
|---|---|
| `docker-build` | Updated — passes `--build-arg VERSION` to all three builds |
| `docker-push` | Tags and pushes all three images to `$REGISTRY` |
| `helm-lint` | Runs `helm lint ./deploy/helm` |
| `helm-template` | Dry-run render to stdout for inspection |
| `helm-install` | `helm install` with `DB_DSN` and `REDIS_ADDR` from env |
| `helm-upgrade` | `helm upgrade --reuse-values` with new image tags |
| `helm-uninstall` | Removes the Helm release |
| `helm-status` | Shows current release status |

---

## Key Decisions

### 1. `scratch` as the final image base

The final stage uses `FROM scratch` — an empty filesystem. The binary is statically compiled (`CGO_ENABLED=0`) so it needs no libc. The only additions are CA certificates (for TLS to PostgreSQL, Redis, and the OTel collector) and timezone data (for correct `slog` timestamps).

Result: ~12–15MB images vs ~400MB if the Go toolchain were included. Smaller attack surface — no shell, no package manager, no OS utilities.

Trade-off: `kubectl exec` into a running pod gives you nothing. Debugging must happen via logs and metrics, not interactive shells. This is the correct production posture.

### 2. Three separate Deployments, not one

Each binary has fundamentally different scaling characteristics:

| Binary | Scaling model | Why |
|---|---|---|
| `orion-api` | Fixed replicas (CPU HPA optional) | Stateless HTTP/gRPC — scale on CPU |
| `orion-scheduler` | Fixed at N, only 1 active | PG advisory lock ensures single active scheduler; others are hot standbys |
| `orion-worker` | Queue-depth HPA (2–50) | Scale out when jobs pile up, scale in when idle |

Combining them into one Deployment would force all three to scale together, wasting resources and breaking the scheduler's leader-election model.

### 3. Scheduler redundancy via PG advisory lock (not leader election sidecar)

The scheduler already implements leader election via `pg_try_advisory_lock` (Phase 6). Running 3 scheduler replicas means:
- Pod 1 holds the lock → runs dispatch loops
- Pods 2 and 3 wait → acquire the lock within ~3 seconds if Pod 1 dies

No external leader election library needed. The PDB (`minAvailable: 1`) ensures at least one scheduler pod survives node drains.

### 4. HPA on queue depth, not CPU

CPU is a trailing indicator for workers — they only use CPU while actively executing. When 1000 jobs are queued but all workers are idle between jobs, CPU is 0% but you need more workers immediately.

`orion_queue_depth` is a leading indicator. The HPA uses an `External` metric type via `prometheus-adapter`, with CPU as a fallback for clusters without the adapter installed.

Scale-up stabilization: 30 seconds (react quickly to queue growth).
Scale-down stabilization: 300 seconds (avoid flapping when queue drains).

### 5. `existingSecret` pattern for production secrets

The chart supports two modes:

**Development** — provide DSN directly:
```bash
helm install orion ./deploy/helm --set database.dsn="postgres://..."
# DSN stored in chart-managed Secret. Visible in helm get values. Never in production.
```

**Production** — reference an externally managed Secret:
```bash
helm install orion ./deploy/helm --set database.existingSecret=orion-db-credentials
# Chart never touches the secret value. Works with Sealed Secrets, External Secrets Operator, Vault.
```

When `existingSecret` is set, `secret.yaml` is skipped entirely. All three Deployments reference the external secret name via `secretRef`.

### 6. `maxUnavailable: 0` on all Deployments

Rolling updates never reduce below the current replica count. Combined with `terminationGracePeriodSeconds: 60` on workers, in-flight jobs complete before pods are terminated. The `preStop` hook adds a 5-second sleep to let the load balancer drain connections before SIGTERM arrives.

### 7. `checksum/config` annotation forces pod restarts on ConfigMap changes

Kubernetes does not restart pods when a ConfigMap changes. The annotation:
```yaml
checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
```
changes whenever `values.yaml` changes any config value, which triggers a rolling restart automatically on `helm upgrade`.

### 8. `topologySpreadConstraints` on API pods

API pods use `whenUnsatisfiable: ScheduleAnyway` (not `DoNotSchedule`) so that single-node clusters (kind, minikube) still work. In multi-zone clusters, pods spread across zones for HA. The constraint is a best-effort hint, not a hard requirement.

---

## Resource Defaults

Chosen to be conservative for a medium production cluster. Tune via `values.yaml`.

| Component | CPU request | CPU limit | Memory request | Memory limit |
|---|---|---|---|---|
| API | 100m | 500m | 128Mi | 512Mi |
| Scheduler | 50m | 200m | 64Mi | 256Mi |
| Worker | 200m | 2000m | 256Mi | 2Gi |

Worker limits are intentionally wide — inline handlers and K8s Job watchers can be memory-intensive depending on workload.

---

## What Gets Deployed

```
Namespace: ml-platform
│
├── Deployment: orion-orion-api         (3 replicas)
├── Deployment: orion-orion-scheduler   (3 replicas, 1 active)
├── Deployment: orion-orion-worker      (5 initial, HPA 2–50)
│
├── Service: orion-orion-api            (ClusterIP, ports 8080/9090/9091)
├── Service: orion-orion-scheduler-metrics  (ClusterIP, port 9092)
├── Service: orion-orion-worker-metrics     (ClusterIP, port 9093)
│
├── HPA: orion-orion-worker-hpa         (External metric + CPU fallback)
├── PDB: orion-orion-api-pdb            (minAvailable: 2)
├── PDB: orion-orion-scheduler-pdb      (minAvailable: 1)
│
├── ConfigMap: orion-orion-config       (all non-sensitive env vars)
├── Secret: orion-orion-secrets         (DB DSN + Redis — skipped if existingSecret set)
│
├── ServiceAccount: orion-orion-api
├── ServiceAccount: orion-orion-scheduler
├── ServiceAccount: orion-orion-worker
├── ClusterRole: orion-orion-job-manager
├── ClusterRoleBinding: orion-orion-job-manager-binding
│
└── Namespace: orion-jobs               (for K8s Job pods — Phase 4)
```

Resource names follow the pattern `{release-name}-{chart-name}-{component}`. With `helm install orion`, all resources are prefixed `orion-orion-`. Use `--set fullnameOverride=orion` to get cleaner names like `orion-api`.

---

## Verification

### helm lint

```bash
make helm-lint
# ==> Linting ./deploy/helm
# 1 chart(s) linted, 0 chart(s) failed
```

### Template render (no cluster needed)

```bash
make helm-template
# Renders all 18 resources to stdout. Inspect for correctness.
```

### Local kind cluster test

```bash
# Create cluster
kind create cluster --name orion-test

# Install dependencies
helm repo add bitnami https://charts.bitnami.com/bitnami
helm install postgres bitnami/postgresql \
  --set auth.username=orion --set auth.password=orion --set auth.database=orion
helm install redis bitnami/redis --set auth.enabled=false

# Run migrations
kubectl apply -f deploy/k8s/migrate-job.yaml
kubectl wait --for=condition=complete job/orion-migrate --timeout=120s

# Install Orion
helm install orion ./deploy/helm \
  --namespace ml-platform --create-namespace \
  --set database.dsn="postgres://orion:orion@postgres-postgresql:5432/orion" \
  --set redis.addr="redis-master:6379" \
  --set api.image.repository=orion-api --set api.image.tag=local \
  --set scheduler.image.repository=orion-scheduler --set scheduler.image.tag=local \
  --set worker.image.repository=orion-worker --set worker.image.tag=local \
  --set worker.autoscaling.enabled=false

# Verify
kubectl get pods -n ml-platform
# NAME                                    READY   STATUS    RESTARTS
# orion-orion-api-xxx                     1/1     Running   0  (×3)
# orion-orion-scheduler-xxx               1/1     Running   0  (×3)
# orion-orion-worker-xxx                  1/1     Running   0  (×5)

# API health
kubectl port-forward -n ml-platform svc/orion-orion-api 8080:8080 &
curl http://localhost:8080/healthz
# {"status":"ok"}

# Scheduler leader election
kubectl logs -n ml-platform -l app.kubernetes.io/component=scheduler | grep "leader"
# Only ONE pod should log "acquired scheduler leader lock"
```

---

## Architecture After Phase 9

```
Developer machine
  make docker-build → 3 images (~12–15MB each)
  make docker-push  → pushed to ghcr.io/shreeharshshinde/orion/*

Kubernetes cluster (any — kind, EKS, GKE, AKS)
  helm install orion ./deploy/helm
    ↓
  ConfigMap + Secret → env vars injected into all pods
  RBAC → worker ServiceAccount can create K8s Jobs
  3 × API pods → HTTP :8080 + gRPC :9090 + Prometheus :9091
  3 × Scheduler pods → 1 active (PG lock), 2 standby
  N × Worker pods → autoscale on orion_queue_depth
```

---

## What Phase 9 Completes

Phase 9 is the final phase. Orion is now:

- **Functionally complete** — job execution, retries, pipelines, observability, gRPC streaming, rate limiting (Phases 1–8)
- **Operationally complete** — Dockerized, Helm-packaged, autoscaling, zero-downtime deployments, secret management, PodDisruptionBudgets (Phase 9)

```
Phase 1  — Domain types, interfaces, project skeleton
Phase 2  — PostgreSQL store (CAS state transitions, idempotency)
Phase 3  — Inline executor (handler registry, execution audit trail)
Phase 4  — Kubernetes executor (client-go, GPU resources, RBAC)
Phase 5  — Pipeline DAG (advancement engine, fan-out/fan-in, cascade cancel)
Phase 6  — Full observability (Prometheus, OTel traces, Grafana dashboard)
Phase 7  — gRPC streaming (proto, broadcaster, WatchJob/WatchPipeline)
Phase 8  — Rate limiting + fair scheduling (token bucket, weighted fair queue)
Phase 9  — Helm + production Kubernetes ← YOU ARE HERE
```
