# `internal/worker/k8s/` — Kubernetes Executor
## `spec.go` · `executor.go` · `executor_test.go`

---

## What this package does

Three files. One job: run ML workloads on Kubernetes pods.

| File | Role |
|---|---|
| `spec.go` | Translates `domain.KubernetesSpec` → `batchv1.Job`. Pure mapping, no network. |
| `executor.go` | Implements `worker.Executor`. Creates the Job, watches it to completion. |
| `executor_test.go` | 18 tests. Uses `fake.NewSimpleClientset()` — no real cluster needed. |

---

## `spec.go` — the translation layer

`spec.go` is the **only file in the codebase** that imports both Orion domain types and Kubernetes API types simultaneously. It performs a one-way translation.

```
domain.KubernetesSpec                 batchv1.Job
─────────────────────────             ──────────────────────────────────────────
Image         → containers[0].image
Command       → containers[0].command
Args          → containers[0].args
EnvVars       → containers[0].env     (sorted alphabetically for determinism)
ResourceRequest:
  CPU          → resources.requests/limits["cpu"]
  Memory       → resources.requests/limits["memory"]
  GPU          → resources.requests/limits["nvidia.com/gpu"]
Namespace     → namespace field in Create() call
ServiceAccount → spec.template.spec.serviceAccountName
TTLSeconds    → spec.ttlSecondsAfterFinished   (default: 86400 = 24h)
```

### Four critical decisions in spec.go

**1. `backoffLimit = 0`**

Kubernetes must not retry pods independently. Orion owns the entire retry lifecycle through its state machine and backoff policy. If K8s retried autonomously, Orion would see the job stuck in `running` status while the pod restarts, breaking state transitions.

```go
var backoffLimit int32 = 0  // never negotiable
```

**2. `RestartPolicy = Never`**

`OnFailure` restarts the container inside the same pod — Orion would never see a failed pod, only a pod that keeps restarting. `Never` causes the pod to enter the `Failed` state immediately when the container exits non-zero, which the Watch loop detects.

```go
RestartPolicy: corev1.RestartPolicyNever  // never negotiable
```

**3. Guaranteed QoS (requests == limits)**

Setting `requests == limits` places the pod in Kubernetes' Guaranteed QoS class. Kubernetes never evicts Guaranteed pods unless the node itself is entirely full. For a training run that might take 4 hours, this is essential — eviction mid-training wastes all prior computation.

```go
// CPU: requests = limits
requests[corev1.ResourceCPU] = resource.MustParse(r.CPU)
limits[corev1.ResourceCPU]   = resource.MustParse(r.CPU)
```

**4. `TTLSecondsAfterFinished`**

Auto-deletes the K8s Job and its pod N seconds after completion. Without this, completed Jobs accumulate indefinitely until the cluster's etcd runs out of space. Default: 86400 (24 hours) — enough time for log inspection before cleanup.

---

## `executor.go` — the execution engine

### The interface

```go
type Executor interface {
    Execute(ctx context.Context, job *domain.Job) error
    CanExecute(jobType domain.JobType) bool
}

func (e *KubernetesExecutor) CanExecute(jobType domain.JobType) bool {
    return jobType == domain.JobTypeKubernetes  // "k8s_job"
}
```

### Execute() — what happens in order

```
1. Validate spec (nil spec, empty image, empty command → error)
2. Resolve namespace (spec.Namespace → DefaultNamespace → "orion-jobs")
3. buildK8sJob() → batchv1.Job struct
4. client.BatchV1().Jobs(ns).Create(ctx, k8sJob) → pod scheduled
5. waitForCompletion(ctx, jobName, ns)
   └── Watch loop (preferred) → events until Succeeded/Failed
   └── Poll fallback (if Watch drops) → GET every 5s
6. If ctx cancelled: Delete K8s Job (fresh context, 30s timeout)
7. Return nil (success) or error (failure)
```

### The Watch loop

```go
watcher, err := e.client.BatchV1().Jobs(ns).Watch(ctx, metav1.ListOptions{
    FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
})
for {
    select {
    case <-ctx.Done():
        return ctx.Err()       // SIGTERM or deadline
    case event, ok := <-watcher.ResultChan():
        if !ok { return e.pollForCompletion(...) }  // watch dropped → fallback
        k8sJob := event.Object.(*batchv1.Job)
        result, msg := jobStatus(k8sJob)
        if result == jobSucceeded { return nil }
        if result == jobFailed { return fmt.Errorf(...) }
        // pending/running: continue watching
    }
}
```

Watch is a long-lived HTTP connection to the K8s API server. Kubernetes pushes an event every time the Job's status changes — no polling overhead. When the channel closes (network drop, API server restart), we fall back to polling.

### Why a fresh context for deletion

```go
// On ctx cancellation:
deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
_ = e.client.BatchV1().Jobs(ns).Delete(deleteCtx, jobName, ...)
```

The original `ctx` is already cancelled. Using it for deletion would fail immediately. We use `context.Background()` with a 30-second timeout so the deletion call has time to reach the API server, which then stops the GPU pod.

Without this cleanup: the training pod continues running on a GPU node with no owner monitoring it — GPU resources wasted until the TTL expires.

### `jobStatus()` — reading K8s conditions

Kubernetes sets two condition types on Jobs:

| Condition | Status | Meaning |
|---|---|---|
| `JobComplete` | `True` | All completions succeeded |
| `JobFailed` | `True` | BackoffLimit exhausted (one failure, in our case) |

We also check raw counters as a fallback — conditions may not be set synchronously with the pod exit:

```
Succeeded > 0  → jobSucceeded
Failed    > 0  → jobFailed
Active    > 0  → jobRunning
else           → jobPending
```

### `BuildK8sClient()` — two modes

```go
// In-cluster (worker pod running inside Kubernetes):
// Reads /var/run/secrets/kubernetes.io/serviceaccount/token automatically
restCfg, err = rest.InClusterConfig()

// Out-of-cluster (local dev, kind cluster):
// Reads ~/.kube/config or KUBECONFIG env var
restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
```

Controlled by `ORION_K8S_IN_CLUSTER=true/false`.

---

## `executor_test.go` — the fake client

All 18 tests use `fake.NewSimpleClientset()` — no cluster needed:

```go
fakeClient := fake.NewSimpleClientset()

// Inject a custom watcher (controls what events the executor sees)
watcher := watch.NewFake()
fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
    return true, watcher, nil
})

// Run executor
errCh := make(chan error, 1)
go func() { errCh <- executor.Execute(ctx, job) }()

// Simulate pod succeeding
time.Sleep(30 * time.Millisecond) // let Create() be called
watcher.Modify(succeededJob("orion-550e8400", "test-ns"))

err := <-errCh  // nil (success)
```

This approach is standard across the Kubernetes ecosystem — `client-go` ships the fake client specifically for testing.

### What the tests cover

| Group | Tests |
|---|---|
| CanExecute routing | k8s_job=true, inline=false, nil client panics |
| Input validation | nil spec, empty image, empty command (all 3 error immediately) |
| spec.go field verification | job name format, backoffLimit=0, RestartPolicy=Never, CPU/memory/GPU resources, env var sorting, TTL default, Orion labels |
| Execution paths | success via Watch, failure via Watch, context cancellation, Watch channel close → poll fallback |
| Counter-based detection | Succeeded=1, Failed=1 (without Conditions being set) |
| Namespace resolution | spec.Namespace overrides default; empty spec.Namespace falls back to default |

---

## Run the tests

```bash
# Unit tests — no cluster needed
go test -race ./internal/worker/k8s/... -v

# Expected output:
# --- PASS: TestKubernetesExecutor_CanExecute_K8sJob
# --- PASS: TestBuildK8sJob_BackoffLimitIsZero
# --- PASS: TestExecute_SuccessPath
# --- PASS: TestExecute_WatchChannelClose_FallsBackToPoll
# ... (18 tests total)
# PASS
```

---

## File location

```
orion/
└── internal/
    └── worker/
        └── k8s/
            ├── spec.go           ← domain.KubernetesSpec → batchv1.Job
            ├── executor.go       ← KubernetesExecutor + BuildK8sClient
            └── executor_test.go  ← 18 tests, fake client, no cluster
```