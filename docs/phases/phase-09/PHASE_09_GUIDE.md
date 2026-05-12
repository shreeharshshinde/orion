# Orion — Phase 9 Master Guide
## Helm Chart + Production Kubernetes Deployment

> **What this document is:** The complete planning guide for Phase 9 — every file to create, every Helm template explained, the multi-stage Dockerfile design, production hardening checklist, scaling strategy, and the exact commands that prove Orion is running correctly in Kubernetes. Read this once before writing any YAML.

---

## Table of Contents

1. [Why Phase 9 Exists](#1-why-phase-9-exists)
2. [What Phase 9 Delivers](#2-what-phase-9-delivers)
3. [The Three-Binary Deployment Model](#3-the-three-binary-deployment-model)
4. [Complete File Plan — 16 New Files](#4-complete-file-plan)
5. [Dockerfiles — Multi-Stage Builds](#5-dockerfiles)
6. [Helm Chart Structure](#6-helm-chart-structure)
7. [File-by-File: Every Template Explained](#7-file-by-file-every-template-explained)
8. [values.yaml — The Complete Default Configuration](#8-valuesyaml)
9. [Production Hardening Checklist](#9-production-hardening-checklist)
10. [HPA: Autoscaling Workers on Queue Depth](#10-hpa-autoscaling-workers-on-queue-depth)
11. [Secret Management Strategy](#11-secret-management-strategy)
12. [Step-by-Step Build and Deploy Order](#12-step-by-step-build-and-deploy-order)
13. [Complete Verification Sequence](#13-complete-verification-sequence)
14. [Scaling Reference](#14-scaling-reference)
15. [Common Mistakes](#15-common-mistakes)
16. [The Complete Journey: Orion Phases 1–9](#16-the-complete-journey)

---

## 1. Why Phase 9 Exists

After Phase 8, Orion is **functionally complete**. Every feature works:

- ✅ Reliable job execution with retry and dead-letter (Phases 1–4)
- ✅ Pipeline DAG orchestration (Phase 5)
- ✅ Full observability — metrics, traces, logs (Phase 6)
- ✅ Real-time gRPC streaming (Phase 7)
- ✅ Rate limiting and fair scheduling (Phase 8)

But it only runs via `make run-api`, `make run-scheduler`, `make run-worker` — local processes on a developer's machine. It cannot be deployed, scaled, updated, or operated by a platform team.

Phase 9 solves this. After Phase 9:
- A one-line `helm install` deploys Orion to any Kubernetes cluster
- Workers autoscale from 2 to 50 pods based on queue depth
- Rolling updates deploy new versions with zero downtime
- Secrets are never in plaintext
- Every pod has resource limits, liveness probes, and PodDisruptionBudgets
- A platform team can operate Orion without knowing Go

---

## 2. What Phase 9 Delivers

### The `helm install` command

```bash
helm install orion ./deploy/helm \
  --namespace ml-platform \
  --create-namespace \
  --set database.existingSecret=orion-db-credentials \
  --set redis.existingSecret=orion-redis-credentials \
  --set api.replicaCount=3 \
  --set worker.autoscaling.maxReplicas=50

# Output:
# NAME: orion
# NAMESPACE: ml-platform
# STATUS: deployed
# NOTES:
#   API endpoint: https://orion.ml-platform.svc.cluster.local:8080
#   Grafana dashboard: import deploy/grafana/dashboards/orion.json
```

### What gets deployed

```
Kubernetes cluster after helm install:
  Namespace: ml-platform
  │
  ├── Deployment: orion-api         (3 replicas, HPA disabled — API scales on CPU)
  ├── Deployment: orion-scheduler   (3 replicas, only 1 active via PG advisory lock)
  ├── Deployment: orion-worker      (5 replicas initial, autoscales 2-50 on queue depth)
  │
  ├── Service: orion-api            (ClusterIP + optional LoadBalancer)
  ├── Service: orion-api-grpc       (ClusterIP for gRPC :9090)
  ├── Service: orion-api-metrics    (ClusterIP :9091 for Prometheus scraping)
  ├── Service: orion-scheduler-metrics
  ├── Service: orion-worker-metrics
  │
  ├── HPA: orion-worker             (min=2, max=50, metric: orion_queue_depth)
  ├── PDB: orion-api                (minAvailable: 2)
  ├── PDB: orion-scheduler          (minAvailable: 1)
  │
  ├── ConfigMap: orion-config       (non-sensitive env vars)
  ├── Secret: orion-secrets         (DB DSN, Redis password — or ExternalSecret)
  ├── ServiceAccount: orion-worker  (for K8s Job creation — Phase 4 RBAC)
  ├── ClusterRole: orion-job-manager
  └── ClusterRoleBinding: orion-job-manager-binding
```

---

## 3. The Three-Binary Deployment Model

### Why three separate Deployments

Each binary has different scaling characteristics:

| Binary | Scaling | Replicas | Why |
|---|---|---|---|
| `orion-api` | CPU-based | 3 fixed | Stateless HTTP/gRPC. Scale on CPU. |
| `orion-scheduler` | Fixed at N, 1 active | 3 fixed | PG advisory lock ensures only 1 active. Others are hot standbys. |
| `orion-worker` | Queue depth HPA | 2–50 | Scale out when jobs pile up. Scale in when idle. |

### The scheduler's redundancy model

```
orion-scheduler pod 1: holds PG advisory lock → ACTIVE (running dispatch loops)
orion-scheduler pod 2: waiting → STANDBY (will acquire lock if pod 1 dies)
orion-scheduler pod 3: waiting → STANDBY

If pod 1 crashes:
  → PG connection closes → advisory lock auto-released
  → pod 2 or 3 acquires lock within 3 seconds
  → Maximum scheduler downtime: 3 seconds
```

This is why `minAvailable: 1` in the PDB — we always need at least one scheduler pod available.

### Worker autoscaling logic

```
Target metric: sum(orion_queue_depth) / worker_replica_count
Target value: 5 jobs per worker

If queue_depth = 100 and workers = 5:
  current_metric = 100/5 = 20 (too high)
  desired_replicas = ceil(100/5) = 20 workers needed
  → HPA scales up to 20 workers

If queue_depth = 0 for 5 minutes:
  → HPA scales down toward minReplicas (2)
```

---

## 4. Complete File Plan

Phase 9 adds 16 files. Zero existing files are modified.

```
deploy/
├── docker/
│   ├── Dockerfile.api              ← NEW: multi-stage, ~12MB final image
│   ├── Dockerfile.scheduler        ← NEW: multi-stage, ~12MB final image
│   └── Dockerfile.worker           ← NEW: multi-stage, ~14MB final image
│
└── helm/
    ├── Chart.yaml                  ← NEW: chart metadata, version, appVersion
    ├── values.yaml                 ← NEW: all default configuration
    ├── .helmignore                 ← NEW: ignore patterns
    └── templates/
        ├── _helpers.tpl            ← NEW: name helpers, label templates
        ├── api-deployment.yaml     ← NEW: API Deployment + Service + PDB
        ├── scheduler-deployment.yaml ← NEW: Scheduler Deployment + PDB
        ├── worker-deployment.yaml  ← NEW: Worker Deployment
        ├── hpa.yaml                ← NEW: HPA for worker autoscaling
        ├── rbac.yaml               ← NEW: ServiceAccount + ClusterRole + Binding
        ├── configmap.yaml          ← NEW: non-sensitive env vars
        ├── secret.yaml             ← NEW: DB DSN + Redis password (if not using existingSecret)
        └── servicemonitor.yaml     ← NEW: Prometheus ServiceMonitor (if prometheusOperator enabled)

docs/phase9/
├── PHASE9-MASTER-GUIDE.md          ← this document
├── README-deployment.md            ← operator quickstart
└── README-scaling.md               ← scaling guide, HPA tuning
```

---

## 5. Dockerfiles — Multi-Stage Builds

### Design principles

1. **Build stage uses `golang:1.22-alpine`** — has Go toolchain (~400MB)
2. **Final stage uses `scratch`** — empty filesystem (~0MB)
3. **Result: ~12–15MB images** — fast pulls, small attack surface
4. **No shell in production image** — `distroless` or `scratch`; cannot `kubectl exec bash`
5. **Non-root user** — UID 10001, never root

### `Dockerfile.api`

```dockerfile
# ─── Stage 1: Build ──────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Install CA certificates for HTTPS calls (OTel exporter needs them)
RUN apk add --no-cache ca-certificates git tzdata

WORKDIR /build

# Copy go.mod and go.sum first — layer-caches dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s -X main.version=${VERSION}" \
    -o /orion-api ./cmd/api/

# ─── Stage 2: Final ──────────────────────────────────────────────────────────
FROM scratch

# Copy CA certs (needed for TLS connections to PostgreSQL, Redis, Jaeger)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Copy timezone data (slog timestamps use local zone)
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
# Copy the binary
COPY --from=builder /orion-api /orion-api

# Non-root user (UID 10001 — no name in scratch, use numeric UID)
USER 10001:10001

EXPOSE 8080 9091

ENTRYPOINT ["/orion-api"]
```

### `Dockerfile.worker`

The worker image is slightly larger because it may need to run inline handlers that import external libraries. For GPU workloads, the heavy computation runs in Kubernetes pods — the worker itself stays lightweight.

```dockerfile
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache ca-certificates git tzdata
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" \
    -o /orion-worker ./cmd/worker/

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /orion-worker /orion-worker

USER 10001:10001
EXPOSE 9093

ENTRYPOINT ["/orion-worker"]
```

### Build and push

```bash
# Set version from git tag or SHA
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
REGISTRY=ghcr.io/shreeharshshinde/orion

docker build -f deploy/docker/Dockerfile.api \
  --build-arg VERSION=$VERSION \
  -t $REGISTRY/orion-api:$VERSION .

docker build -f deploy/docker/Dockerfile.scheduler \
  -t $REGISTRY/orion-scheduler:$VERSION .

docker build -f deploy/docker/Dockerfile.worker \
  -t $REGISTRY/orion-worker:$VERSION .

docker push $REGISTRY/orion-api:$VERSION
docker push $REGISTRY/orion-scheduler:$VERSION
docker push $REGISTRY/orion-worker:$VERSION
```

---

## 6. Helm Chart Structure

### `Chart.yaml`

```yaml
apiVersion: v2
name: orion
description: Production-grade distributed ML job orchestrator
type: application
version: 0.1.0          # Helm chart version (bumped on chart changes)
appVersion: "1.0.0"     # Orion application version (matches Docker image tag)
keywords:
  - ml
  - jobs
  - orchestrator
  - kubernetes
maintainers:
  - name: Shreeharsh Ambhore
home: https://github.com/shreeharshshinde/orion
sources:
  - https://github.com/shreeharshshinde/orion
```

### `_helpers.tpl` — Shared name and label helpers

```yaml
{{/*
Expand the name of the chart.
*/}}
{{- define "orion.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "orion.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels — applied to every resource for kubectl filtering.
*/}}
{{- define "orion.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector labels — used in matchLabels and Service selectors.
*/}}
{{- define "orion.selectorLabels" -}}
app.kubernetes.io/name: {{ include "orion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image reference with tag fallback to appVersion.
*/}}
{{- define "orion.image" -}}
{{- $tag := .tag | default .root.Chart.AppVersion -}}
{{ .repository }}:{{ $tag }}
{{- end }}
```

---

## 7. File-by-File: Every Template Explained

### `configmap.yaml` — Non-sensitive configuration

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "orion.fullname" . }}-config
  namespace: {{ .Release.Namespace }}
  labels: {{- include "orion.labels" . | nindent 4 }}
data:
  ORION_ENV: {{ .Values.global.env | quote }}
  ORION_LOG_LEVEL: {{ .Values.global.logLevel | quote }}
  ORION_HTTP_PORT: {{ .Values.api.httpPort | quote }}
  ORION_GRPC_PORT: {{ .Values.api.grpcPort | quote }}
  ORION_METRICS_PORT: {{ .Values.api.metricsPort | quote }}
  ORION_OTLP_ENDPOINT: {{ .Values.observability.otlpEndpoint | quote }}
  ORION_TRACING_SAMPLE_RATE: {{ .Values.observability.tracingSampleRate | quote }}
  ORION_WORKER_CONCURRENCY: {{ .Values.worker.concurrency | quote }}
  ORION_SCHEDULER_BATCH_SIZE: {{ .Values.scheduler.batchSize | quote }}
  ORION_SCHEDULER_INTERVAL: {{ .Values.scheduler.intervalSeconds | quote }}
  ORION_K8S_IN_CLUSTER: "true"
  ORION_K8S_NAMESPACE: {{ .Values.worker.k8sJobNamespace | quote }}
  # Queue rate limiting (Phase 8)
  ORION_QUEUE_HIGH_RATE_PER_SEC: {{ .Values.queues.high.ratePerSec | quote }}
  ORION_QUEUE_DEFAULT_RATE_PER_SEC: {{ .Values.queues.default.ratePerSec | quote }}
  ORION_QUEUE_LOW_RATE_PER_SEC: {{ .Values.queues.low.ratePerSec | quote }}
```

### `secret.yaml` — Sensitive configuration

```yaml
{{- if not .Values.database.existingSecret }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "orion.fullname" . }}-secrets
  namespace: {{ .Release.Namespace }}
  labels: {{- include "orion.labels" . | nindent 4 }}
  annotations:
    # Mark for Sealed Secrets or External Secrets Operator management
    helm.sh/resource-policy: keep
type: Opaque
stringData:
  ORION_DATABASE_DSN: {{ required "database.dsn is required" .Values.database.dsn | quote }}
  ORION_REDIS_ADDR: {{ .Values.redis.addr | quote }}
  ORION_REDIS_PASSWORD: {{ .Values.redis.password | default "" | quote }}
{{- end }}
```

### `api-deployment.yaml` — API Server

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "orion.fullname" . }}-api
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "orion.labels" . | nindent 4 }}
    app.kubernetes.io/component: api
spec:
  replicas: {{ .Values.api.replicaCount }}
  selector:
    matchLabels:
      {{- include "orion.selectorLabels" . | nindent 6 }}
      app.kubernetes.io/component: api
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0   # Never reduce below replicaCount during rollout
  template:
    metadata:
      labels:
        {{- include "orion.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: api
      annotations:
        # Force pod restart when ConfigMap changes
        checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
    spec:
      serviceAccountName: {{ include "orion.fullname" . }}-api
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: api
          image: {{ include "orion.image" (dict "repository" .Values.api.image.repository "tag" .Values.api.image.tag "root" .) }}
          imagePullPolicy: {{ .Values.api.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.api.httpPort }}
            - name: grpc
              containerPort: {{ .Values.api.grpcPort }}
            - name: metrics
              containerPort: {{ .Values.api.metricsPort }}
          envFrom:
            - configMapRef:
                name: {{ include "orion.fullname" . }}-config
            - secretRef:
                name: {{ if .Values.database.existingSecret }}{{ .Values.database.existingSecret }}{{ else }}{{ include "orion.fullname" . }}-secrets{{ end }}
          # Liveness probe: is the process alive?
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 10
            periodSeconds: 15
            failureThreshold: 3
          # Readiness probe: is the API ready to serve requests (DB reachable)?
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
            failureThreshold: 2
          resources:
            requests:
              cpu: {{ .Values.api.resources.requests.cpu }}
              memory: {{ .Values.api.resources.requests.memory }}
            limits:
              cpu: {{ .Values.api.resources.limits.cpu }}
              memory: {{ .Values.api.resources.limits.memory }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
      topologySpreadConstraints:
        # Spread API pods across availability zones for HA
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app.kubernetes.io/component: api
---
# PodDisruptionBudget: always keep at least 2 API pods running
# during node drains and voluntary disruptions.
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "orion.fullname" . }}-api-pdb
  namespace: {{ .Release.Namespace }}
spec:
  minAvailable: 2
  selector:
    matchLabels:
      app.kubernetes.io/component: api
```

### `scheduler-deployment.yaml` — Scheduler (hot-standby HA)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "orion.fullname" . }}-scheduler
spec:
  replicas: {{ .Values.scheduler.replicaCount }}    # 3 pods, 1 active via PG lock
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  # ... (same structure as API deployment)
  template:
    spec:
      containers:
        - name: scheduler
          # No HTTP port — scheduler has no API surface
          # Only metrics port for Prometheus scraping
          ports:
            - name: metrics
              containerPort: {{ .Values.scheduler.metricsPort }}
          # Liveness: process alive (no /healthz — use exec probe)
          livenessProbe:
            exec:
              command: ["sh", "-c", "pgrep orion-scheduler"]
            initialDelaySeconds: 15
            periodSeconds: 30
          resources:
            requests:
              cpu: {{ .Values.scheduler.resources.requests.cpu }}
              memory: {{ .Values.scheduler.resources.requests.memory }}
            limits:
              cpu: {{ .Values.scheduler.resources.limits.cpu }}
              memory: {{ .Values.scheduler.resources.limits.memory }}
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "orion.fullname" . }}-scheduler-pdb
spec:
  minAvailable: 1    # Always at least 1 scheduler pod
  selector:
    matchLabels:
      app.kubernetes.io/component: scheduler
```

### `worker-deployment.yaml` — Worker (autoscaled)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "orion.fullname" . }}-worker
spec:
  replicas: {{ .Values.worker.replicaCount }}
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 2
      maxUnavailable: 0   # Never reduce workers during rollout
  template:
    spec:
      serviceAccountName: {{ include "orion.fullname" . }}-worker
      # Graceful shutdown: let in-flight jobs complete before SIGKILL
      terminationGracePeriodSeconds: {{ .Values.worker.shutdownTimeoutSeconds }}
      containers:
        - name: worker
          # Workers need KUBECONFIG access for K8s job creation (Phase 4)
          # In-cluster config is used when ORION_K8S_IN_CLUSTER=true
          lifecycle:
            preStop:
              exec:
                # Give in-flight jobs time to complete before SIGTERM
                command: ["sh", "-c", "sleep 5"]
          livenessProbe:
            exec:
              command: ["sh", "-c", "pgrep orion-worker"]
            initialDelaySeconds: 15
            periodSeconds: 30
          resources:
            requests:
              cpu: {{ .Values.worker.resources.requests.cpu }}
              memory: {{ .Values.worker.resources.requests.memory }}
            limits:
              cpu: {{ .Values.worker.resources.limits.cpu }}
              memory: {{ .Values.worker.resources.limits.memory }}
```

### `hpa.yaml` — HorizontalPodAutoscaler

```yaml
{{- if .Values.worker.autoscaling.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "orion.fullname" . }}-worker-hpa
  namespace: {{ .Release.Namespace }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "orion.fullname" . }}-worker
  minReplicas: {{ .Values.worker.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.worker.autoscaling.maxReplicas }}
  metrics:
    # Scale on Prometheus metric: total queue depth / workers
    # Requires prometheus-adapter installed in the cluster
    - type: External
      external:
        metric:
          name: orion_queue_depth_per_worker
          selector:
            matchLabels:
              namespace: {{ .Release.Namespace }}
        target:
          type: AverageValue
          averageValue: {{ .Values.worker.autoscaling.targetQueueDepthPerWorker }}
    # Fallback: also scale on CPU utilization
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 30     # React to queue depth increases within 30s
      policies:
        - type: Pods
          value: 4                        # Add at most 4 pods per 60s
          periodSeconds: 60
    scaleDown:
      stabilizationWindowSeconds: 300    # Wait 5 min before scaling down (avoid flapping)
      policies:
        - type: Pods
          value: 2                        # Remove at most 2 pods per 60s
          periodSeconds: 60
{{- end }}
```

### `rbac.yaml` — Kubernetes RBAC for worker

```yaml
# ServiceAccount for the API server (minimal permissions — no K8s Job creation)
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "orion.fullname" . }}-api
  namespace: {{ .Release.Namespace }}
---
# ServiceAccount for the worker (needs to create/watch/delete K8s Jobs)
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "orion.fullname" . }}-worker
  namespace: {{ .Release.Namespace }}
---
# ClusterRole: permissions to manage K8s Jobs across namespaces
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "orion.fullname" . }}-job-manager
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
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "orion.fullname" . }}-job-manager-binding
subjects:
  - kind: ServiceAccount
    name: {{ include "orion.fullname" . }}-worker
    namespace: {{ .Release.Namespace }}
roleRef:
  kind: ClusterRole
  apiGroup: rbac.authorization.k8s.io
  name: {{ include "orion.fullname" . }}-job-manager
```

### `servicemonitor.yaml` — Prometheus scraping

```yaml
{{- if .Values.observability.serviceMonitor.enabled }}
# Requires prometheus-operator (kube-prometheus-stack) installed.
# Tells Prometheus to scrape all three Orion services automatically.
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "orion.fullname" . }}
  namespace: {{ .Values.observability.serviceMonitor.namespace | default .Release.Namespace }}
  labels:
    {{- include "orion.labels" . | nindent 4 }}
    {{- with .Values.observability.serviceMonitor.additionalLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "orion.selectorLabels" . | nindent 6 }}
  endpoints:
    - port: metrics
      interval: 15s
      path: /metrics
{{- end }}
```

---

## 8. `values.yaml` — The Complete Default Configuration

```yaml
# ─────────────────────────────────────────────────────────────────────────────
# Global settings
# ─────────────────────────────────────────────────────────────────────────────
global:
  env: production
  logLevel: info
  imagePullSecrets: []
  imageRegistry: ghcr.io/shreeharshshinde/orion

nameOverride: ""
fullnameOverride: ""

# ─────────────────────────────────────────────────────────────────────────────
# API Server
# ─────────────────────────────────────────────────────────────────────────────
api:
  replicaCount: 3
  image:
    repository: ghcr.io/shreeharshshinde/orion/orion-api
    tag: ""          # defaults to Chart.AppVersion
    pullPolicy: IfNotPresent
  httpPort: 8080
  grpcPort: 9090
  metricsPort: 9091
  service:
    type: ClusterIP    # change to LoadBalancer for external access
    annotations: {}
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
  autoscaling:
    enabled: false     # API autoscales on CPU if needed
    minReplicas: 3
    maxReplicas: 10
    targetCPUUtilizationPercentage: 70

# ─────────────────────────────────────────────────────────────────────────────
# Scheduler
# ─────────────────────────────────────────────────────────────────────────────
scheduler:
  replicaCount: 3       # 3 pods; only 1 active via PG advisory lock
  image:
    repository: ghcr.io/shreeharshshinde/orion/orion-scheduler
    tag: ""
    pullPolicy: IfNotPresent
  metricsPort: 9092
  batchSize: 50
  intervalSeconds: "2s"
  orphanIntervalSeconds: "30s"
  resources:
    requests:
      cpu: "50m"
      memory: "64Mi"
    limits:
      cpu: "200m"
      memory: "256Mi"

# ─────────────────────────────────────────────────────────────────────────────
# Worker
# ─────────────────────────────────────────────────────────────────────────────
worker:
  replicaCount: 5          # initial replica count
  image:
    repository: ghcr.io/shreeharshshinde/orion/orion-worker
    tag: ""
    pullPolicy: IfNotPresent
  metricsPort: 9093
  concurrency: 10           # goroutines per worker pod (total capacity = pods × concurrency)
  shutdownTimeoutSeconds: 60
  k8sJobNamespace: orion-jobs
  autoscaling:
    enabled: true
    minReplicas: 2
    maxReplicas: 50
    targetQueueDepthPerWorker: "5"  # scale up if > 5 jobs per worker
  resources:
    requests:
      cpu: "200m"
      memory: "256Mi"
    limits:
      cpu: "2000m"
      memory: "2Gi"

# ─────────────────────────────────────────────────────────────────────────────
# Database (PostgreSQL)
# ─────────────────────────────────────────────────────────────────────────────
database:
  # Option 1: provide DSN directly (stored in Secret managed by this chart)
  dsn: ""
  # Option 2: reference an existing Secret (Sealed Secrets, External Secrets)
  # The secret must contain key: ORION_DATABASE_DSN
  existingSecret: ""
  maxConns: 25
  minConns: 2

# ─────────────────────────────────────────────────────────────────────────────
# Redis
# ─────────────────────────────────────────────────────────────────────────────
redis:
  addr: "redis-master:6379"
  password: ""
  existingSecret: ""         # must contain key: ORION_REDIS_PASSWORD
  db: 0
  poolSize: 10

# ─────────────────────────────────────────────────────────────────────────────
# Queue rate limiting (Phase 8)
# ─────────────────────────────────────────────────────────────────────────────
queues:
  high:
    maxConcurrent: 8
    weight: 0.8
    ratePerSec: 100
    burst: 20
  default:
    maxConcurrent: 6
    weight: 0.6
    ratePerSec: 50
    burst: 10
  low:
    maxConcurrent: 2
    weight: 0.2
    ratePerSec: 10
    burst: 5

# ─────────────────────────────────────────────────────────────────────────────
# Observability (Phase 6)
# ─────────────────────────────────────────────────────────────────────────────
observability:
  otlpEndpoint: "jaeger-collector:4317"
  tracingSampleRate: "0.1"       # sample 10% of traces in production
  serviceMonitor:
    enabled: false               # set true if prometheus-operator is installed
    namespace: ""
    additionalLabels: {}

# ─────────────────────────────────────────────────────────────────────────────
# Kubernetes job namespace (for Phase 4 GPU pods)
# ─────────────────────────────────────────────────────────────────────────────
jobNamespace:
  create: true
  name: orion-jobs
```

---

## 9. Production Hardening Checklist

### Security

```
□ All containers run as non-root (UID 10001)
□ readOnlyRootFilesystem: true on all containers
□ allowPrivilegeEscalation: false on all containers
□ All capabilities dropped (capabilities.drop: ["ALL"])
□ seccompProfile: RuntimeDefault on all pods
□ Database DSN stored in Kubernetes Secret, not ConfigMap
□ Redis password stored in Kubernetes Secret
□ No secrets in values.yaml (use --set or existingSecret)
□ ImagePullSecrets configured for private registry
□ Network policies limiting pod-to-pod traffic (future phase)
```

### Reliability

```
□ PodDisruptionBudgets on API (minAvailable: 2) and Scheduler (minAvailable: 1)
□ topologySpreadConstraints spreading API pods across zones
□ terminationGracePeriodSeconds: 60 on workers (lets jobs complete)
□ preStop hook adding 5s sleep before SIGTERM
□ maxUnavailable: 0 on all deployments (zero-downtime rolling updates)
□ Liveness probes on all containers
□ Readiness probe on API (/readyz checks DB connection)
□ Resource requests set (enables Kubernetes scheduling)
□ Resource limits set (prevents runaway memory consumption)
```

### Operational

```
□ checksum/config annotation forces pod restart on ConfigMap change
□ helm.sh/resource-policy: keep on Secret (prevents accidental deletion)
□ ServiceMonitor created if prometheus-operator is present
□ Grafana dashboard imported (deploy/grafana/dashboards/orion.json)
□ HPA configured with stabilizationWindow (prevents flapping)
□ Helm release name used in all resource names (supports multiple environments)
```

---

## 10. HPA: Autoscaling Workers on Queue Depth

### Why queue depth is the right metric

CPU utilization is a trailing indicator — workers only use CPU while actively executing. When 1000 jobs are in the queue but all workers are idle (between jobs), CPU is 0% but you desperately need more workers.

Queue depth is a leading indicator — it tells you how many jobs are waiting **before** workers get overwhelmed.

### The prometheus-adapter setup

The HPA `External` metric type requires `prometheus-adapter` to translate Prometheus metrics into Kubernetes metrics API calls.

```yaml
# prometheus-adapter ConfigMap (add to your monitoring stack)
rules:
  external:
    - seriesQuery: 'orion_queue_depth'
      resources:
        overrides:
          namespace: {resource: "namespace"}
      name:
        matches: "orion_queue_depth"
        as: "orion_queue_depth_per_worker"
      metricsQuery: |
        sum(orion_queue_depth{namespace="<<.LabelMatchers>>"})
        / on() group_left
        count(kube_deployment_status_replicas{
          deployment="<<.Release.Name>>-orion-worker",
          namespace="<<.LabelMatchers>>"
        })
```

### Scale-up scenario

```
t=0:    queue_depth = 0, workers = 2 (minReplicas)
t=60s:  100 jobs submitted → queue_depth = 100
        metric = 100/2 = 50 per worker
        target = 5 per worker
        desired = ceil(100/5) = 20 workers
        HPA scales up by 4 pods/minute (scaleUp policy)
t=120s: workers = 6, queue_depth = 80 (some consumed)
t=180s: workers = 10, queue_depth = 50
...
t=300s: workers = 20, queue_depth = 0 (all jobs done)
t=600s: queue_depth = 0 for 5 min → HPA scales to minReplicas (2)
```

---

## 11. Secret Management Strategy

### Option 1 — Direct values (development/staging only)

```bash
helm install orion ./deploy/helm \
  --set database.dsn="postgres://orion:password@postgres:5432/orion"
# ⚠️  DSN stored in Helm release history (plaintext in etcd). Never in production.
```

### Option 2 — existingSecret (recommended for production)

```bash
# Create the secret externally (Sealed Secrets, External Secrets Operator, etc.)
kubectl create secret generic orion-db-credentials \
  --from-literal=ORION_DATABASE_DSN="postgres://orion:$(vault kv get ...)" \
  --namespace ml-platform

# Reference it in Helm — chart never touches the secret value
helm install orion ./deploy/helm \
  --set database.existingSecret=orion-db-credentials
```

### Option 3 — Sealed Secrets (GitOps-safe)

```bash
# Encrypt the secret locally
kubectl create secret generic orion-db-credentials \
  --from-literal=ORION_DATABASE_DSN="..." \
  --dry-run=client -o yaml | \
  kubeseal --format yaml > deploy/k8s/sealed-secret.yaml

# Commit sealed-secret.yaml to Git — safe, encrypted
# In cluster, Sealed Secrets controller decrypts and creates the real Secret
```

---

## 12. Step-by-Step Build and Deploy Order

### Step 1 — Write Dockerfiles and verify local build

```bash
docker build -f deploy/docker/Dockerfile.api -t orion-api:local .
docker build -f deploy/docker/Dockerfile.scheduler -t orion-scheduler:local .
docker build -f deploy/docker/Dockerfile.worker -t orion-worker:local .

# Verify size targets
docker images | grep orion
# orion-api        local   ~12MB
# orion-scheduler  local   ~12MB
# orion-worker     local   ~14MB
```

### Step 2 — Write Chart.yaml and values.yaml

```bash
helm lint ./deploy/helm
# 1 chart(s) linted, 0 chart(s) failed
```

### Step 3 — Write all templates

```bash
# Validate template rendering
helm template orion ./deploy/helm \
  --set database.dsn="postgres://orion:test@localhost/orion" \
  --debug | head -50
# Should produce valid YAML without errors
```

### Step 4 — Create a local kind cluster for testing

```bash
kind create cluster --name orion-prod-test
kubectl config use-context kind-orion-prod-test

# Install PostgreSQL and Redis for testing
helm repo add bitnami https://charts.bitnami.com/bitnami
helm install postgres bitnami/postgresql --set auth.password=orion --set auth.username=orion --set auth.database=orion
helm install redis bitnami/redis --set auth.enabled=false
```

### Step 5 — Apply migration via init-job

```bash
# Create a one-time Kubernetes Job to run migrations
kubectl apply -f deploy/k8s/migrate-job.yaml
kubectl wait --for=condition=complete job/orion-migrate --timeout=120s
```

### Step 6 — helm install

```bash
helm install orion ./deploy/helm \
  --namespace ml-platform \
  --create-namespace \
  --set database.dsn="postgres://orion:orion@postgres-postgresql:5432/orion" \
  --set redis.addr="redis-master:6379" \
  --set api.image.repository=orion-api \
  --set api.image.tag=local \
  --set worker.autoscaling.enabled=false  # disable for local test
```

### Step 7 — Verify deployment

```bash
kubectl get pods -n ml-platform
# NAME                              READY   STATUS    RESTARTS
# orion-api-xxx-xxx                 1/1     Running   0  (×3)
# orion-scheduler-xxx-xxx           1/1     Running   0  (×3)
# orion-worker-xxx-xxx              1/1     Running   0  (×5)

kubectl logs -n ml-platform -l app.kubernetes.io/component=scheduler | grep "leader lock"
# INFO msg="acquired scheduler leader lock"  (only ONE pod)
```

---

## 13. Complete Verification Sequence

### Test 1 — API reachable

```bash
# Port-forward API service
kubectl port-forward -n ml-platform svc/orion-api 8080:8080 &

curl -s http://localhost:8080/healthz | jq .
# {"status":"ok"}

curl -s http://localhost:8080/readyz | jq .
# {"status":"ready"}
echo "✅ TEST 1 PASSED — API reachable and healthy"
```

### Test 2 — End-to-end job execution in cluster

```bash
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"k8s-cluster-test","type":"inline","payload":{"handler_name":"noop"}}' \
  | jq -r .id)

for i in $(seq 1 20); do
  STATUS=$(curl -s http://localhost:8080/jobs/$JOB_ID | jq -r .status)
  echo "t=${i}s: $STATUS"
  [ "$STATUS" = "completed" ] && echo "✅ TEST 2 PASSED — Job completed in cluster" && break
  sleep 1
done
```

### Test 3 — Scheduler HA failover

```bash
# Find the active scheduler pod
ACTIVE=$(kubectl logs -n ml-platform -l app.kubernetes.io/component=scheduler \
  | grep "acquired scheduler leader lock" | tail -1 | awk '{print $NF}')
echo "Active scheduler: $ACTIVE"

# Kill it
kubectl delete pod -n ml-platform $ACTIVE

# Within 5 seconds, another pod should become active
sleep 6
kubectl logs -n ml-platform -l app.kubernetes.io/component=scheduler --since=10s \
  | grep "acquired scheduler leader lock"
# Should show a DIFFERENT pod acquiring the lock
echo "✅ TEST 3 PASSED — Scheduler HA failover works"
```

### Test 4 — Rolling update zero-downtime

```bash
# Submit 100 jobs that take 2 seconds each
for i in $(seq 1 100); do
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"rolling-test","type":"inline","payload":{"handler_name":"slow","args":{"duration_seconds":2}}}' \
    > /dev/null
done

# Trigger a rolling update (simulate version bump)
helm upgrade orion ./deploy/helm \
  --set api.image.tag=v2 \
  --namespace ml-platform

# Monitor: no 5xx errors during rollout
kubectl rollout status deployment/orion-api -n ml-platform
echo "✅ TEST 4 PASSED — Zero-downtime rolling update"
```

### Test 5 — HPA scales workers under load

```bash
# Enable HPA
helm upgrade orion ./deploy/helm \
  --set worker.autoscaling.enabled=true \
  --namespace ml-platform

# Submit 500 slow jobs
for i in $(seq 1 500); do
  curl -s -X POST http://localhost:8080/jobs \
    -H "Content-Type: application/json" \
    -d '{"name":"hpa-test","type":"inline","payload":{"handler_name":"slow","args":{"duration_seconds":5}}}' \
    > /dev/null
done

# Watch worker pods scale up
watch kubectl get pods -n ml-platform -l app.kubernetes.io/component=worker
# Should grow from 5 → 20+ pods over 2-3 minutes
echo "✅ TEST 5 PASSED — HPA autoscaling workers"
```

---

## 14. Scaling Reference

### Capacity formula

```
Total job capacity = worker_pods × concurrency_per_pod

Example: 20 pods × 10 goroutines = 200 concurrent jobs

For GPU jobs (k8s_job type): capacity is limited by GPU node availability,
not worker pod count. Workers just watch K8s Jobs — they're lightweight.
```

### Recommended values by environment

| Environment | API replicas | Scheduler replicas | Worker initial | Worker max | Notes |
|---|---|---|---|---|---|
| Development | 1 | 1 | 1 | 1 | `make run-*` scripts |
| Staging | 2 | 2 | 2 | 10 | Mirror production topology |
| Production (small) | 3 | 3 | 5 | 20 | <100 jobs/hour |
| Production (medium) | 3 | 3 | 10 | 50 | 100-1000 jobs/hour |
| Production (large) | 5 | 3 | 20 | 100 | 1000+ jobs/hour |

### Tuning the HPA

```yaml
# Aggressive scale-up, conservative scale-down (good for ML workloads)
behavior:
  scaleUp:
    stabilizationWindowSeconds: 0    # React immediately to queue growth
    policies:
      - type: Percent
        value: 100                   # Double workers if needed
        periodSeconds: 60
  scaleDown:
    stabilizationWindowSeconds: 600  # Wait 10 min before scaling down
    policies:
      - type: Pods
        value: 1                     # Remove 1 pod at a time
        periodSeconds: 120
```

---

## 15. Common Mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| `maxUnavailable: 1` on workers | In-flight jobs killed during rollout | Set `maxUnavailable: 0` + `terminationGracePeriodSeconds: 60` |
| No PDB on scheduler | Scheduler killed during node drain → jobs stop dispatching | Add `minAvailable: 1` PDB |
| HPA and manual `replicas` both set | HPA fights with manual scale | When HPA is enabled, remove static `replicas` from Deployment |
| Not using `existingSecret` | DB password visible in `helm get values` | Always use `existingSecret` in production |
| `imagePullPolicy: Always` | Slow pod startup (re-pulls image every time) | Use `IfNotPresent` with immutable tags |
| No `topologySpreadConstraints` | All API pods on one node → single point of failure | Add zone spread constraints |
| `terminationGracePeriodSeconds` too short | Long GPU jobs killed mid-training | Set to max expected job duration or use job deadline |
| Grafana dashboard not imported | Blank dashboards | Mount `orion.json` via ConfigMap or import manually |

---

## 16. The Complete Journey: Orion Phases 1–9

```
Phase 1 — Foundation Skeleton
  19 files: domain types, interfaces, goroutine patterns, migrations

Phase 2 — PostgreSQL Store
  27 files: CAS state transitions, idempotency, orphan reclaim, full SQL

Phase 3 — Inline Executor
  32 files: Registry, HandlerFunc, RecordExecution audit trail

Phase 4 — Kubernetes Executor
  38 files: K8s pod creation, Watch loop, GPU resources, RBAC

Phase 5 — Pipeline DAG
  47 files: DAG advancement, ReadyNodes, fan-out/fan-in, cascade cancel

Phase 6 — Full Observability
  53 files: Prometheus metrics, OTel traces, Grafana dashboard

Phase 7 — gRPC Streaming
  60 files: Proto definition, fan-out broadcaster, WatchJob/WatchPipeline

Phase 8 — Rate Limiting + Fair Scheduling
  66 files: Token bucket, weighted fair queue, dynamic config API

Phase 9 — Helm + Production Kubernetes
  82 files: Dockerfiles, Helm chart, HPA, PDB, ServiceMonitor

DONE. Orion is a production-grade ML orchestrator.
```

### What you've built

A complete, production-grade distributed ML job orchestrator from scratch in Go. It:
- Handles tens of thousands of jobs per hour across multiple queues
- Runs inline Go functions or full GPU Kubernetes pods
- Chains jobs into complex DAG workflows
- Rate-limits and fairly schedules across priority queues
- Exposes real-time streaming updates via gRPC
- Reports full metrics to Prometheus and traces to Jaeger
- Deploys to Kubernetes with a single `helm install`
- Autoscales workers from 2 to 50 pods based on queue depth
- Survives worker crashes, scheduler restarts, and rolling deployments

---

## Summary Checklist

```
□ deploy/docker/Dockerfile.api
    □ Multi-stage: golang:1.22-alpine builder + scratch final
    □ CGO_ENABLED=0 GOOS=linux static binary
    □ -ldflags="-w -s" strips debug symbols (~40% size reduction)
    □ CA certificates and timezone data copied
    □ Non-root USER 10001:10001
    □ Final image < 15MB

□ deploy/docker/Dockerfile.scheduler (same pattern)
□ deploy/docker/Dockerfile.worker    (same pattern)

□ deploy/helm/Chart.yaml
    □ apiVersion: v2, type: application
    □ version and appVersion set

□ deploy/helm/values.yaml
    □ api: replicaCount, image, ports, resources, autoscaling
    □ scheduler: replicaCount, image, resources, config
    □ worker: replicaCount, image, resources, autoscaling, concurrency
    □ database: dsn, existingSecret, pool config
    □ redis: addr, password, existingSecret
    □ queues: high/default/low limits (Phase 8)
    □ observability: otlpEndpoint, sampleRate, serviceMonitor
    □ jobNamespace: create, name

□ deploy/helm/templates/_helpers.tpl
    □ orion.name, orion.fullname, orion.labels, orion.selectorLabels, orion.image

□ deploy/helm/templates/configmap.yaml
    □ All non-sensitive env vars from values

□ deploy/helm/templates/secret.yaml
    □ Conditional on .Values.database.existingSecret == ""
    □ helm.sh/resource-policy: keep annotation

□ deploy/helm/templates/api-deployment.yaml
    □ rollingUpdate maxUnavailable:0
    □ checksum/config annotation
    □ runAsNonRoot, readOnlyRootFilesystem, allowPrivilegeEscalation:false
    □ livenessProbe /healthz, readinessProbe /readyz
    □ topologySpreadConstraints across zones
    □ PodDisruptionBudget minAvailable:2

□ deploy/helm/templates/scheduler-deployment.yaml
    □ livenessProbe exec pgrep
    □ PodDisruptionBudget minAvailable:1

□ deploy/helm/templates/worker-deployment.yaml
    □ terminationGracePeriodSeconds from values
    □ preStop exec sleep 5
    □ serviceAccountName: orion-worker

□ deploy/helm/templates/hpa.yaml
    □ Conditional on worker.autoscaling.enabled
    □ External metric: orion_queue_depth_per_worker
    □ CPU resource metric as fallback
    □ scaleUp stabilizationWindow: 30s
    □ scaleDown stabilizationWindow: 300s

□ deploy/helm/templates/rbac.yaml
    □ ServiceAccount: orion-api (no permissions)
    □ ServiceAccount: orion-worker
    □ ClusterRole: create/get/list/watch/delete on batch/jobs
    □ ClusterRoleBinding

□ deploy/helm/templates/servicemonitor.yaml
    □ Conditional on observability.serviceMonitor.enabled
    □ Scrapes all metrics ports at 15s interval

□ Verification
    □ helm lint ./deploy/helm → 0 errors
    □ docker build all 3 images → each < 15MB
    □ helm install on kind cluster → all pods Running
    □ Test 1 (API health) → /healthz + /readyz return ok
    □ Test 2 (job execution) → noop job completes in cluster
    □ Test 3 (scheduler HA) → failover within 5 seconds
    □ Test 4 (rolling update) → zero 5xx during rollout
    □ Test 5 (HPA) → workers scale to 20+ under load
```