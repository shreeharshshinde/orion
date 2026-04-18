# Phase 4 Wiring — RBAC + `cmd/worker/main.go`
## `deploy/k8s/rbac.yaml` · `cmd/worker/main.go`

---

## `deploy/k8s/rbac.yaml` — Kubernetes RBAC

### Why RBAC is needed

Kubernetes denies all API calls by default. The Orion worker process needs permission to:
- **create** `batchv1.Job` resources (to launch training pods)
- **watch** `batchv1.Job` resources (to receive completion events)
- **get/list** `batchv1.Job` resources (poll fallback)
- **delete** `batchv1.Job` resources (cleanup on cancellation)
- **get/list/watch** `pods` (read logs and exit codes)

Without RBAC applied, every `client.BatchV1().Jobs(ns).Create(...)` call returns:
```
Error creating kubernetes job: jobs.batch is forbidden:
  User "system:serviceaccount:orion-system:orion-worker" cannot create resource "jobs"
  in API group "batch" in the namespace "orion-jobs"
```

### What the manifest creates

```
Namespace: orion-system     ← where Orion services run
Namespace: orion-jobs       ← default namespace for ML pods

ServiceAccount: orion-worker (in orion-system)

ClusterRole: orion-job-manager
  batch/jobs:    create, get, list, watch, delete
  pods:          get, list, watch
  pods/log:      get

ClusterRoleBinding: orion-job-manager-binding
  orion-worker  →  orion-job-manager
```

### Why ClusterRole not Role

`Role` is namespace-scoped — it grants permissions in one namespace only. Since `kubernetes_spec.namespace` can be any namespace (e.g., `"ml-training"`, `"research"`, `"gpu-pool"`), we need cluster-wide permissions via `ClusterRole`.

In a hardened production environment, you'd create a `Role` per namespace and give operators explicit approval per namespace. For Orion's typical use case, `ClusterRole` is the right tradeoff.

### Apply and verify

```bash
# Apply once
kubectl apply -f deploy/k8s/rbac.yaml

# Verify permissions (all must print "yes")
kubectl auth can-i create jobs \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# yes

kubectl auth can-i watch jobs \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n ml-training
# yes

kubectl auth can-i delete jobs \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# yes

# This must print "no" — we do NOT create pods directly
kubectl auth can-i create pods \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# no
```

---

## `cmd/worker/main.go` — what changed in Phase 4

### The exact change

**Before (Phase 3):**
```go
executors := []worker.Executor{
    inlineExecutor,
    // k8s.NewKubernetesExecutor(k8sClient, cfg.Kubernetes, logger)  ← Phase 4
}
```

**After (Phase 4):**
```go
executors := []worker.Executor{inlineExecutor}

k8sClient, err := k8s.BuildK8sClient(cfg.Kubernetes)
if err != nil {
    logger.Warn("kubernetes client unavailable — k8s_job type will not be served", "err", err)
} else {
    k8sExecutor := k8s.NewKubernetesExecutor(k8sClient, k8s.ExecutorConfig{
        DefaultNamespace: cfg.Kubernetes.DefaultNamespace,
        PollInterval:     5 * time.Second,
    }, logger)
    executors = append(executors, k8sExecutor)
    logger.Info("kubernetes executor ready", "default_namespace", cfg.Kubernetes.DefaultNamespace)
}
```

### Why K8s failure is non-fatal

If `BuildK8sClient` fails (no cluster, wrong kubeconfig path, RBAC not applied), the worker:
- Logs a `WARN` with the error
- Continues starting with only the `InlineExecutor`
- Serves all `inline` jobs normally
- `k8s_job` submissions fail with "no executor registered" and retry

When the cluster is fixed and the worker restarts, those retried jobs succeed. This design is intentional: a deployment where Kubernetes is optional (e.g., developers without a local cluster) should not break inline job processing.

### New environment variables

| Variable | Default | Meaning |
|---|---|---|
| `ORION_K8S_IN_CLUSTER` | `false` | `true` = use pod's ServiceAccount token; `false` = use kubeconfig |
| `KUBECONFIG` | `~/.kube/config` | Path to kubeconfig file when `IN_CLUSTER=false` |
| `ORION_K8S_NAMESPACE` | `orion-jobs` | Default namespace for ML pods when spec.Namespace is empty |

### Startup log when both executors are running

```
INFO msg="connected to postgres"
INFO msg="connected to redis"
INFO msg="inline executor ready" registered_handlers=[always_fail echo noop slow] handler_count=4
INFO msg="kubernetes executor ready" default_namespace=orion-jobs in_cluster=false
INFO msg="starting worker pool" worker_id=my-hostname concurrency=10 queues=[...] executors=2
```

`executors=2` confirms both InlineExecutor and KubernetesExecutor are registered.

### Startup log when K8s is unavailable

```
INFO msg="connected to postgres"
INFO msg="connected to redis"
INFO msg="inline executor ready" registered_handlers=[...] handler_count=4
WARN msg="kubernetes client unavailable — k8s_job type will not be served"
     err="building kubernetes config from ~/.kube/config: ..."
INFO msg="starting worker pool" executors=1
```

`executors=1` — only InlineExecutor. Inline jobs still work; K8s jobs retry.

---

## Local dev quick-start with kind

```bash
# 1. Install kind (Kubernetes IN Docker)
go install sigs.k8s.io/kind@latest   # or: brew install kind

# 2. Create a local cluster
kind create cluster --name orion-dev
# Creates ~/.kube/config entry for context "kind-orion-dev"

# 3. Apply RBAC
kubectl apply -f deploy/k8s/rbac.yaml

# 4. Set environment
export ORION_K8S_IN_CLUSTER=false
export KUBECONFIG=~/.kube/config
export ORION_K8S_NAMESPACE=orion-jobs

# 5. Start worker (in addition to API + Scheduler from Phase 2)
make run-worker
# INFO msg="kubernetes executor ready" default_namespace=orion-jobs

# 6. Submit a CPU-only smoke test (no GPU needed)
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "k8s-smoke-test",
    "type": "k8s_job",
    "payload": {
      "kubernetes_spec": {
        "image": "busybox:latest",
        "command": ["sh", "-c"],
        "args": ["echo Hello from Orion && sleep 2 && exit 0"],
        "namespace": "orion-jobs",
        "resources": {"cpu": "100m", "memory": "64Mi"}
      }
    }
  }' | jq -r .id)

# 7. Watch it complete
watch -n1 "curl -s localhost:8080/jobs/$JOB_ID | jq '{status,attempt}'"
# status=queued → scheduled → running → completed

# 8. Watch the pod in Kubernetes
kubectl get pods -n orion-jobs -w
# NAME                   READY   STATUS      RESTARTS   AGE
# orion-XXXXXXXX-...     0/1     Pending     0          1s
# orion-XXXXXXXX-...     1/1     Running     0          4s
# orion-XXXXXXXX-...     0/1     Completed   0          7s

# 9. Verify audit log
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .
# [{"attempt":1,"status":"completed","started_at":"...","finished_at":"..."}]
```

---

## File location

```
orion/
├── cmd/
│   └── worker/
│       └── main.go        ← Updated in Phase 4 (7b block added)
└── deploy/
    └── k8s/
        └── rbac.yaml      ← New in Phase 4
```