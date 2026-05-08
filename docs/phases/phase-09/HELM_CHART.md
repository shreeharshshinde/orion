# Helm Chart — Phase 9

## What This Document Covers

The Helm chart added in Phase 9: chart structure, what each template produces, how the helpers work, the complete `values.yaml` reference, and how to operate the chart in production.

---

## Chart Structure

```
deploy/helm/
├── Chart.yaml              — chart metadata
├── values.yaml             — all defaults
├── .helmignore             — packaging exclusions
└── templates/
    ├── _helpers.tpl        — shared name/label functions
    ├── configmap.yaml      — non-sensitive env vars
    ├── secret.yaml         — DB DSN + Redis password (conditional)
    ├── rbac.yaml           — ServiceAccounts + ClusterRole + Binding + Namespace
    ├── api-deployment.yaml — API Deployment + Service + PDB
    ├── scheduler-deployment.yaml — Scheduler Deployment + Service + PDB
    ├── worker-deployment.yaml    — Worker Deployment + Service
    ├── hpa.yaml            — HPA for workers (conditional)
    └── servicemonitor.yaml — Prometheus ServiceMonitor (conditional)
```

`helm template orion ./deploy/helm --set database.dsn="..."` renders 18 Kubernetes resources.

---

## `Chart.yaml`

```yaml
apiVersion: v2
name: orion
description: Production-grade distributed ML job orchestrator
type: application
version: 0.1.0       # chart version — bump when templates change
appVersion: "1.0.0"  # app version — matches Docker image tag default
```

`version` tracks chart changes (template edits, new values). `appVersion` tracks the Orion application version and is used as the default image tag when `image.tag` is left empty in `values.yaml`.

---

## `_helpers.tpl` — Shared Functions

All templates call these helpers to ensure consistent naming and labeling.

### `orion.fullname`

```
{{ include "orion.fullname" . }}
```

Produces `{release-name}-{chart-name}`, truncated to 63 characters. With `helm install orion`, this yields `orion-orion`. Override with `--set fullnameOverride=orion` to get cleaner names like `orion-api`.

### `orion.labels`

Applied to every resource's `metadata.labels`. Includes:
- `helm.sh/chart` — chart name + version (for `helm list` filtering)
- `app.kubernetes.io/name` — chart name
- `app.kubernetes.io/instance` — release name
- `app.kubernetes.io/version` — appVersion
- `app.kubernetes.io/managed-by` — Helm

### `orion.selectorLabels`

Used in `matchLabels` (Deployment selector) and Service `selector`. Only `name` + `instance` — stable labels that never change after initial deploy. Adding `version` to selector labels would break rolling updates.

### `orion.secretName`

```
{{ include "orion.secretName" . }}
```

Returns `database.existingSecret` if set, otherwise `{fullname}-secrets`. Used in all three Deployments' `secretRef` so they always point to the right secret regardless of which mode is active.

---

## `configmap.yaml` — Non-Sensitive Configuration

Produces one ConfigMap named `{fullname}-config`. All three Deployments mount it via `envFrom.configMapRef`.

Key env vars set:

| Env var | Source in values.yaml |
|---|---|
| `ORION_ENV` | `global.env` |
| `ORION_LOG_LEVEL` | `global.logLevel` |
| `ORION_HTTP_PORT` | `api.httpPort` |
| `ORION_GRPC_PORT` | `api.grpcPort` |
| `ORION_WORKER_CONCURRENCY` | `worker.concurrency` |
| `ORION_SCHEDULER_BATCH_SIZE` | `scheduler.batchSize` |
| `ORION_K8S_IN_CLUSTER` | hardcoded `"true"` |
| `ORION_K8S_NAMESPACE` | `worker.k8sJobNamespace` |
| `ORION_OTLP_ENDPOINT` | `observability.otlpEndpoint` |
| `ORION_QUEUE_HIGH_RATE_PER_SEC` | `queues.high.ratePerSec` |
| `ORION_QUEUE_DEFAULT_RATE_PER_SEC` | `queues.default.ratePerSec` |
| `ORION_QUEUE_LOW_RATE_PER_SEC` | `queues.low.ratePerSec` |

The `checksum/config` pod annotation (in each Deployment) is computed from this template's rendered output. When any value changes, the checksum changes, which triggers a rolling restart on `helm upgrade`.

---

## `secret.yaml` — Sensitive Configuration

Conditional — only rendered when `database.existingSecret` is **not** set:

```yaml
{{- if not .Values.database.existingSecret }}
apiVersion: v1
kind: Secret
...
stringData:
  ORION_DATABASE_DSN: {{ required "..." .Values.database.dsn | quote }}
  ORION_REDIS_ADDR: {{ .Values.redis.addr | quote }}
  ORION_REDIS_PASSWORD: {{ .Values.redis.password | default "" | quote }}
{{- end }}
```

The `helm.sh/resource-policy: keep` annotation prevents `helm uninstall` from deleting the Secret. This protects against accidental data loss — the DSN is needed to connect to the database even after the chart is removed.

The `required` function causes `helm install` to fail with a clear error if `database.dsn` is empty and no `existingSecret` is provided.

---

## `rbac.yaml` — Service Accounts and Permissions

Produces 6 resources:

**Namespace** (conditional on `jobNamespace.create: true`):
```yaml
kind: Namespace
metadata:
  name: orion-jobs   # isolated from Orion infrastructure pods
```

**Three ServiceAccounts** — one per binary:
- `{fullname}-api` — no permissions (API server doesn't touch K8s resources)
- `{fullname}-scheduler` — no permissions (scheduler only touches PostgreSQL)
- `{fullname}-worker` — bound to ClusterRole below

**ClusterRole** `{fullname}-job-manager`:
```yaml
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

ClusterRole (not Role) because `kubernetes_spec.namespace` in job payloads can target any namespace. The worker needs to create Jobs in `orion-jobs`, `ml-training`, or any other namespace the operator configures.

**ClusterRoleBinding** attaches the ClusterRole to the worker ServiceAccount.

---

## `api-deployment.yaml` — API Server

Produces three resources: Deployment, Service, PodDisruptionBudget.

### Deployment highlights

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxSurge: 1
    maxUnavailable: 0    # never reduce below replicaCount during rollout
```

```yaml
annotations:
  checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
```

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 10001
  runAsGroup: 10001
  seccompProfile:
    type: RuntimeDefault
```

```yaml
containers:
  - securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
```

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: http
  initialDelaySeconds: 10
  periodSeconds: 15

readinessProbe:
  httpGet:
    path: /readyz
    port: http
  initialDelaySeconds: 5
  periodSeconds: 10
```

`/healthz` — is the process alive? Returns 200 immediately.
`/readyz` — is the API ready to serve? Checks database connectivity. Pod is removed from Service endpoints until this passes.

```yaml
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway   # best-effort; works on single-node clusters
```

### Service

ClusterIP with three named ports: `http` (8080), `grpc` (9090), `metrics` (9091). Change `service.type` to `LoadBalancer` for external access.

### PodDisruptionBudget

```yaml
spec:
  minAvailable: 2
```

During node drains and voluntary disruptions, at least 2 API pods remain running. With 3 replicas, only 1 pod can be disrupted at a time.

---

## `scheduler-deployment.yaml` — Scheduler

Same structure as the API Deployment. Key differences:

- No HTTP port — scheduler has no API surface
- Liveness probe uses `httpGet` on the metrics port (`/healthz`) — the scheduler exposes a minimal health endpoint on its metrics port
- PDB `minAvailable: 1` — always keep at least one scheduler pod alive so the PG advisory lock can be acquired quickly after a disruption

The scheduler's HA model: 3 pods run, only 1 holds `pg_try_advisory_lock`. If the active pod dies, the lock is released (PG connection closes), and one of the standby pods acquires it within ~3 seconds.

---

## `worker-deployment.yaml` — Worker

Key differences from API:

```yaml
spec:
  {{- if not .Values.worker.autoscaling.enabled }}
  replicas: {{ .Values.worker.replicaCount }}
  {{- end }}
```

When HPA is enabled, the `replicas` field is omitted from the Deployment spec. If both HPA and static `replicas` are set, they fight each other — HPA sets replicas, then the next `helm upgrade` resets them. Omitting `replicas` when HPA is active prevents this conflict.

```yaml
terminationGracePeriodSeconds: {{ .Values.worker.shutdownTimeoutSeconds }}  # 60s
```

Workers receive SIGTERM when a pod is terminated. The Go binary catches SIGTERM and stops accepting new jobs from Redis. In-flight jobs continue until complete. After 60 seconds, SIGKILL is sent. Set this to the maximum expected job duration.

```yaml
lifecycle:
  preStop:
    exec:
      command: ["/bin/sh", "-c", "sleep 5"]
```

Adds a 5-second delay before SIGTERM. This gives the load balancer time to stop routing new connections to the pod before the process starts shutting down. Note: `/bin/sh` doesn't exist in `scratch` images — see `DOCKERFILES.md` for the distroless alternative.

---

## `hpa.yaml` — HorizontalPodAutoscaler

Conditional on `worker.autoscaling.enabled: true`.

```yaml
metrics:
  - type: External
    external:
      metric:
        name: orion_queue_depth_per_worker
      target:
        type: AverageValue
        averageValue: "5"    # scale up if > 5 jobs per worker
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70    # fallback if prometheus-adapter not installed
```

The `External` metric requires `prometheus-adapter` to translate `orion_queue_depth` from Prometheus into the Kubernetes metrics API. The CPU metric works without any additional components.

Scale-up behavior:
```yaml
behavior:
  scaleUp:
    stabilizationWindowSeconds: 30   # react within 30s of queue growth
    policies:
      - type: Pods
        value: 4                     # add at most 4 pods per 60s
        periodSeconds: 60
  scaleDown:
    stabilizationWindowSeconds: 300  # wait 5 min before scaling down
    policies:
      - type: Pods
        value: 2                     # remove at most 2 pods per 60s
        periodSeconds: 60
```

Aggressive scale-up, conservative scale-down. ML jobs are expensive to restart — it's better to keep extra workers running for a few minutes than to kill them and re-queue jobs.

---

## `servicemonitor.yaml` — Prometheus Scraping

Conditional on `observability.serviceMonitor.enabled: true`. Requires `prometheus-operator` (kube-prometheus-stack) installed in the cluster.

When enabled, creates a `ServiceMonitor` that tells Prometheus to scrape all Orion services at 15-second intervals on the `metrics` port. Without this, you must manually add scrape configs to `prometheus.yml`.

---

## `values.yaml` Reference

### Global

| Key | Default | Description |
|---|---|---|
| `global.env` | `production` | `ORION_ENV` env var |
| `global.logLevel` | `info` | `ORION_LOG_LEVEL` env var |
| `global.imagePullSecrets` | `[]` | Pull secrets for private registries |
| `nameOverride` | `""` | Override chart name in resource names |
| `fullnameOverride` | `""` | Override full resource name prefix |

### API

| Key | Default | Description |
|---|---|---|
| `api.replicaCount` | `3` | Number of API pods |
| `api.image.repository` | `ghcr.io/shreeharshshinde/orion/orion-api` | Image repository |
| `api.image.tag` | `""` | Image tag (defaults to `Chart.AppVersion`) |
| `api.image.pullPolicy` | `IfNotPresent` | Pull policy |
| `api.httpPort` | `8080` | HTTP listen port |
| `api.grpcPort` | `9090` | gRPC listen port |
| `api.metricsPort` | `9091` | Prometheus metrics port |
| `api.service.type` | `ClusterIP` | Service type (`LoadBalancer` for external) |
| `api.resources.requests.cpu` | `100m` | CPU request |
| `api.resources.requests.memory` | `128Mi` | Memory request |
| `api.resources.limits.cpu` | `500m` | CPU limit |
| `api.resources.limits.memory` | `512Mi` | Memory limit |

### Scheduler

| Key | Default | Description |
|---|---|---|
| `scheduler.replicaCount` | `3` | Pods (1 active, 2 standby) |
| `scheduler.batchSize` | `50` | Jobs dispatched per scheduler cycle |
| `scheduler.intervalSeconds` | `"2s"` | Dispatch loop interval |
| `scheduler.orphanIntervalSeconds` | `"30s"` | Orphan reclaim interval |

### Worker

| Key | Default | Description |
|---|---|---|
| `worker.replicaCount` | `5` | Initial replicas (ignored when HPA enabled) |
| `worker.concurrency` | `10` | Goroutines per worker pod |
| `worker.shutdownTimeoutSeconds` | `60` | `terminationGracePeriodSeconds` |
| `worker.k8sJobNamespace` | `orion-jobs` | Namespace for K8s Job pods |
| `worker.autoscaling.enabled` | `true` | Enable HPA |
| `worker.autoscaling.minReplicas` | `2` | HPA minimum |
| `worker.autoscaling.maxReplicas` | `50` | HPA maximum |
| `worker.autoscaling.targetQueueDepthPerWorker` | `"5"` | Scale up threshold |

### Database

| Key | Default | Description |
|---|---|---|
| `database.dsn` | `""` | PostgreSQL DSN (used when `existingSecret` is empty) |
| `database.existingSecret` | `""` | Name of pre-existing Secret with `ORION_DATABASE_DSN` key |
| `database.maxConns` | `25` | Connection pool max |
| `database.minConns` | `2` | Connection pool min |

### Redis

| Key | Default | Description |
|---|---|---|
| `redis.addr` | `redis-master:6379` | Redis address |
| `redis.password` | `""` | Redis password |
| `redis.existingSecret` | `""` | Pre-existing Secret with `ORION_REDIS_PASSWORD` key |
| `redis.poolSize` | `10` | Connection pool size |

### Queues (Phase 8 rate limiting)

| Key | Default | Description |
|---|---|---|
| `queues.high.ratePerSec` | `100` | Token bucket refill rate |
| `queues.high.burst` | `20` | Token bucket burst capacity |
| `queues.default.ratePerSec` | `50` | — |
| `queues.default.burst` | `10` | — |
| `queues.low.ratePerSec` | `10` | — |
| `queues.low.burst` | `5` | — |

### Observability

| Key | Default | Description |
|---|---|---|
| `observability.otlpEndpoint` | `jaeger-collector:4317` | OTel collector gRPC endpoint |
| `observability.tracingSampleRate` | `"0.1"` | 10% sampling in production |
| `observability.serviceMonitor.enabled` | `false` | Create Prometheus ServiceMonitor |
| `observability.serviceMonitor.namespace` | `""` | Namespace for ServiceMonitor (defaults to release namespace) |

---

## Common Operations

### Install

```bash
# Development (DSN in values — visible in helm history)
helm install orion ./deploy/helm \
  --namespace ml-platform --create-namespace \
  --set database.dsn="postgres://orion:orion@postgres:5432/orion" \
  --set redis.addr="redis-master:6379"

# Production (pre-existing secret)
helm install orion ./deploy/helm \
  --namespace ml-platform --create-namespace \
  --set database.existingSecret=orion-db-credentials \
  --set api.replicaCount=3 \
  --set worker.autoscaling.maxReplicas=50
```

### Upgrade (new image version)

```bash
helm upgrade orion ./deploy/helm \
  --namespace ml-platform \
  --reuse-values \
  --set api.image.tag=v1.1.0 \
  --set scheduler.image.tag=v1.1.0 \
  --set worker.image.tag=v1.1.0
```

`--reuse-values` keeps all existing values and only overrides the specified ones. The `checksum/config` annotation triggers rolling restarts for all three Deployments.

### Scale workers manually (HPA disabled)

```bash
helm upgrade orion ./deploy/helm \
  --namespace ml-platform \
  --reuse-values \
  --set worker.autoscaling.enabled=false \
  --set worker.replicaCount=20
```

### Rollback

```bash
helm rollback orion 1 --namespace ml-platform
# Rolls back to revision 1. Kubernetes performs a rolling rollback.
```

### Uninstall

```bash
helm uninstall orion --namespace ml-platform
# Removes all chart resources EXCEPT the Secret (helm.sh/resource-policy: keep)
# Delete the Secret manually if needed:
kubectl delete secret orion-orion-secrets -n ml-platform
```

---

## Lint and Template Validation

```bash
# Lint (checks YAML validity and Helm best practices)
make helm-lint
# 1 chart(s) linted, 0 chart(s) failed

# Render all templates to stdout
make helm-template

# Render with existingSecret to verify no Secret is created
helm template orion ./deploy/helm \
  --set database.existingSecret=my-secret \
  --namespace ml-platform | grep "kind: Secret"
# (no output — Secret is correctly suppressed)

# Render with HPA disabled to verify replicas field is present
helm template orion ./deploy/helm \
  --set database.dsn="..." \
  --set worker.autoscaling.enabled=false \
  --namespace ml-platform | grep -A2 "kind: HorizontalPodAutoscaler"
# (no output — HPA is correctly suppressed)
```
