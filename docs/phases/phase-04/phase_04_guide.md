# Orion — Phase 4 Master Guide
## Kubernetes Executor: Real ML Workloads on GPU Pods

> **What this document is:** Everything you need to understand, plan, and build Phase 4 completely — the mental model for how Orion interacts with Kubernetes, every file to create, every design decision with rationale, the exact code to write, RBAC configuration, local testing strategy, and the precise output that proves Phase 4 is done.

---

## Table of Contents

1. [Why Phase 4 Exists — The Gap It Closes](#1-why-phase-4-exists--the-gap-it-closes)
2. [What Changes: Before and After](#2-what-changes-before-and-after)
3. [Mental Model — How Orion Talks to Kubernetes](#3-mental-model--how-orion-talks-to-kubernetes)
4. [The client-go Library — What We Actually Use](#4-the-client-go-library--what-we-actually-use)
5. [Complete File Plan](#5-complete-file-plan)
6. [File 1: `internal/worker/k8s/spec.go` — Domain → K8s Translation](#6-file-1-internalworkerk8sspecgo--domain--k8s-translation)
7. [File 2: `internal/worker/k8s/executor.go` — The Executor](#7-file-2-internalworkerk8sexecutorgo--the-executor)
8. [File 3: `internal/worker/k8s/executor_test.go` — Tests with Fake Client](#8-file-3-internalworkerk8sexecutor_testgo--tests-with-fake-client)
9. [File 4: `cmd/worker/main.go` — Updated Wiring](#9-file-4-cmdworkermainago--updated-wiring)
10. [File 5: `deploy/k8s/rbac.yaml` — Kubernetes RBAC](#10-file-5-deployk8srbacyaml--kubernetes-rbac)
11. [The Watch Loop — Waiting for Pod Completion](#11-the-watch-loop--waiting-for-pod-completion)
12. [Error Taxonomy — Every Failure Mode](#12-error-taxonomy--every-failure-mode)
13. [GPU Resource Scheduling](#13-gpu-resource-scheduling)
14. [Context and Deadline Handling](#14-context-and-deadline-handling)
15. [Local Testing Strategy — kind Cluster](#15-local-testing-strategy--kind-cluster)
16. [Step-by-Step Build Order](#16-step-by-step-build-order)
17. [Complete End-to-End Test Sequence](#17-complete-end-to-end-test-sequence)
18. [Common Mistakes and How to Avoid Them](#18-common-mistakes-and-how-to-avoid-them)
19. [Phase 5 Preview](#19-phase-5-preview)

---

## 1. Why Phase 4 Exists — The Gap It Closes

After Phase 3, Orion can run inline Go functions. That covers preprocessing, validation, feature engineering, and lightweight model operations. But real ML training — fine-tuning a 7B parameter LLM, training ResNet-50 on ImageNet, running hyperparameter sweeps — requires:

- **GPUs** (A100, V100, T4) that only Kubernetes nodes have
- **Large RAM** (16Gi, 32Gi, 64Gi) beyond what a worker process can allocate
- **Specific container images** (`pytorch/pytorch:2.0-cuda11.7`, `tensorflow/tensorflow:2.13-gpu`)
- **Isolated compute** — training should not compete with the Orion worker for CPU/RAM

Phase 3 jobs run *inside* the worker process. Phase 4 jobs run *outside* — in their own Kubernetes pod, with dedicated resources, on specialized hardware. The worker creates the pod, watches it, and reports the result. That's it.

```
Phase 3 flow:                         Phase 4 flow:
─────────────────────────────         ──────────────────────────────────────
Worker process                        Worker process
  → fn(ctx, job) runs here              → creates K8s Job (batchv1.Job)
  → shares worker's CPU/RAM             → watches pod via K8s Watch API
  → no GPU access                       → pod runs on GPU node with 32Gi RAM
  → returns nil/error                   → pod exits 0 → completed
                                        → pod exits non-zero → failed
```

---

## 2. What Changes: Before and After

### Phase 3 state (what you have now)

```go
// cmd/worker/main.go
executors := []worker.Executor{
    inlineExecutor,
    // k8s.NewKubernetesExecutor(k8sClient, cfg.Kubernetes, logger)  ← Phase 4
}
```

A `k8s_job` type submission:
```
POST /jobs {"type":"k8s_job", "payload":{"kubernetes_spec":{...}}}
→ Worker: resolveExecutor("k8s_job") → nil
→ MarkJobFailed("no executor registered for job type k8s_job")
→ retry cycle starts
```

### Phase 4 state (what you will have)

```go
// cmd/worker/main.go
k8sClient, err := buildK8sClient(cfg.Kubernetes)
k8sExecutor := k8s.NewKubernetesExecutor(k8sClient, cfg.Kubernetes, logger)

executors := []worker.Executor{
    inlineExecutor,    // Phase 3: "inline" jobs
    k8sExecutor,       // Phase 4: "k8s_job" jobs
}
```

A `k8s_job` submission:
```
POST /jobs {"type":"k8s_job", "payload":{"kubernetes_spec":{"image":"pytorch/pytorch:2.0",...}}}
→ Worker: resolveExecutor("k8s_job") → KubernetesExecutor ✓
→ spec.go: domain.KubernetesSpec → batchv1.Job
→ client.BatchV1().Jobs(ns).Create(...) → pod scheduled on GPU node
→ Watch loop: pod status → Running → Succeeded
→ MarkJobCompleted ← first k8s job completed ✓
```

---

## 3. Mental Model — How Orion Talks to Kubernetes

### The Kubernetes control plane conversation

Kubernetes has a central API server. You talk to it via HTTP (the `kubectl` tool does exactly this). `client-go` is the official Go library that wraps those HTTP calls.

```
KubernetesExecutor                    Kubernetes API Server
        |                                      |
        |── BatchV1().Jobs(ns).Create(job) ──▶|
        |                                      |── schedules pod on node
        |                                      |── node pulls image
        |                                      |── container starts
        |── Watch(job-name) ─────────────────▶|
        |◀── event: status=Running ────────────|
        |◀── event: status=Succeeded ──────────|
        |                                      |
   return nil (success)
```

### The two client-go calls we make

**1. Create the Job:**
```go
createdJob, err := client.BatchV1().Jobs(namespace).Create(
    ctx,
    k8sJobSpec,       // batchv1.Job struct
    metav1.CreateOptions{},
)
```

This tells Kubernetes: "create a Job resource with these specifications." Kubernetes schedules a pod to run the container. This call returns immediately — the pod is not running yet.

**2. Watch for completion:**
```go
watcher, err := client.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
    FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
})
for event := range watcher.ResultChan() {
    job := event.Object.(*batchv1.Job)
    if isComplete(job) { return nil }
    if isFailed(job) { return error }
}
```

This opens a long-lived HTTP connection to the Kubernetes API (a "watch"). Kubernetes sends events as the Job status changes: `Pending → Running → Succeeded` or `Pending → Running → Failed`.

### What a `batchv1.Job` looks like vs our `KubernetesSpec`

```
Our domain.KubernetesSpec          Kubernetes batchv1.Job
──────────────────────────         ──────────────────────────────────────
Image: "pytorch/pytorch:2.0"  →   spec.template.spec.containers[0].image
Command: ["python","train.py"] →   spec.template.spec.containers[0].command
Args: ["--epochs=50"]          →   spec.template.spec.containers[0].args
EnvVars: {"KEY":"val"}         →   spec.template.spec.containers[0].env
CPU: "4", Memory: "16Gi"       →   containers[0].resources.requests/limits
GPU: 2                         →   containers[0].resources.limits["nvidia.com/gpu"]
TTLSeconds: 3600               →   spec.ttlSecondsAfterFinished
Namespace: "ml-training"       →   namespace in Create() call
ServiceAccount: "orion-worker" →   spec.template.spec.serviceAccountName
```

`spec.go` handles this entire translation. It is the only file that knows both the Orion domain types and the Kubernetes API types.

---

## 4. The client-go Library — What We Actually Use

`k8s.io/client-go` is the official Go client for Kubernetes. It is already in `go.mod` from Phase 1. Phase 4 is the first time we actually use it.

### Building the client — two modes

```go
// Mode 1: In-cluster (when Orion worker runs inside Kubernetes)
// Reads ServiceAccount token mounted at /var/run/secrets/kubernetes.io/serviceaccount/
config, err := rest.InClusterConfig()

// Mode 2: Out-of-cluster (local dev, reads ~/.kube/config)
config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)

// Either way, create the typed client:
client, err := kubernetes.NewForConfig(config)
```

The `cfg.Kubernetes.InCluster` flag (env: `ORION_K8S_IN_CLUSTER`) switches between the two. In `cmd/worker/main.go`, we call `buildK8sClient(cfg.Kubernetes)` which wraps this choice.

### The fake client for tests

The single most important thing about `client-go` for testing: it ships a **fake client** that implements the exact same interface but stores objects in memory:

```go
import "k8s.io/client-go/kubernetes/fake"

// Real client (production):
client, _ := kubernetes.NewForConfig(config)

// Fake client (tests) — same interface, no network:
client := fake.NewSimpleClientset()
```

Because `KubernetesExecutor` takes `kubernetes.Interface` (not `*kubernetes.Clientset`), you can inject the fake in tests. This means every test runs without a real Kubernetes cluster.

---

## 5. Complete File Plan

Phase 4 touches exactly 5 locations:

```
internal/worker/k8s/
├── spec.go           ← NEW: domain.KubernetesSpec → batchv1.Job translation
├── executor.go       ← NEW: KubernetesExecutor, Create + Watch loop
└── executor_test.go  ← NEW: unit tests using fake.NewSimpleClientset()

cmd/worker/
└── main.go           ← UPDATED: wire k8sClient + KubernetesExecutor

deploy/k8s/
└── rbac.yaml         ← NEW: ServiceAccount + ClusterRole + ClusterRoleBinding
```

### What is NOT changing

| File | Why unchanged |
|---|---|
| `internal/worker/pool.go` | `resolveExecutor()` already loops executors. Adding KubernetesExecutor makes it find a match for `k8s_job`. Zero code changes. |
| `internal/domain/job.go` | `KubernetesSpec` and `ResourceRequest` already exist. |
| `internal/worker/inline.go` | Completely unrelated to Phase 4. |
| `internal/store/postgres/db.go` | No new SQL needed. `MarkJobCompleted`, `RecordExecution` already exist. |
| `internal/scheduler/scheduler.go` | Dispatcher doesn't know or care about job type. |
| `internal/api/handler/job.go` | Already validates `kubernetes_spec` for `k8s_job` type. |

---

## 6. File 1: `internal/worker/k8s/spec.go` — Domain → K8s Translation

This file has one job: translate our clean domain type into the verbose Kubernetes API struct. It is the only file that imports both `domain` and Kubernetes API types.

### Why a separate file?

`executor.go` would become unmaintainable if it mixed execution logic with struct construction. `spec.go` is purely a mapping function — no goroutines, no watches, no error handling. It is trivially testable.

### Complete code to write

```go
// Package k8s implements the KubernetesExecutor for Orion.
// This file translates domain.KubernetesSpec into a batchv1.Job resource.
//
// The only coupling between Orion's domain model and the Kubernetes API
// lives here. If the K8s API changes, only this file needs updating.
package k8s

import (
    "fmt"

    batchv1 "k8s.io/api/batch/v1"
    corev1  "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/resource"
    metav1  "k8s.io/apimachinery/pkg/apis/meta/v1"

    "github.com/shreeharsh-a/orion/internal/domain"
)

// jobNameForOrionJob returns a stable, unique Kubernetes Job name for a given
// Orion job. K8s names must be DNS-1123 compliant (lowercase, alphanumeric, hyphens).
// We use "orion-" prefix + the first 8 chars of the UUID for readability.
func jobNameForOrionJob(job *domain.Job) string {
    return fmt.Sprintf("orion-%s", job.ID.String()[:8])
}

// buildK8sJob translates a domain.Job with KubernetesSpec into a batchv1.Job
// ready to be submitted to the Kubernetes API.
//
// Labels added to every Job:
//   orion-job-id  : full UUID of the Orion job (for querying / filtering)
//   orion-job-name: human-readable name
//   managed-by    : "orion" (for RBAC and resource policies)
func buildK8sJob(job *domain.Job, spec *domain.KubernetesSpec) *batchv1.Job {
    jobName := jobNameForOrionJob(job)

    // Standard labels applied to both the Job and its Pod template.
    labels := map[string]string{
        "orion-job-id":   job.ID.String(),
        "orion-job-name": job.Name,
        "managed-by":     "orion",
    }

    // Build resource requirements from our ResourceRequest type.
    resources := buildResourceRequirements(spec.ResourceRequest)

    // Build environment variables from the flat map.
    envVars := buildEnvVars(spec.EnvVars)

    // TTL after completion — auto-delete the K8s Job (and its pod) this many
    // seconds after it finishes. Prevents accumulation of completed Jobs.
    // Default 24h (86400s) if not specified; callers can set 0 for immediate cleanup.
    ttl := spec.TTLSeconds
    if ttl == 0 {
        ttl = 86400
    }

    // backoffLimit=0: Kubernetes itself should NOT retry on failure.
    // Orion owns the retry logic via its state machine and backoff policy.
    // If K8s retried independently, we'd get double-counting of attempts.
    var backoffLimit int32 = 0

    // completions=1, parallelism=1: exactly one pod runs to completion.
    // Distributed training (multiple pods) is a Phase 5+ concern.
    var completions int32 = 1
    var parallelism int32 = 1

    return &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      jobName,
            Namespace: spec.Namespace,
            Labels:    labels,
            Annotations: map[string]string{
                "orion-job-id":  job.ID.String(),
                "orion-attempt": fmt.Sprintf("%d", job.Attempt),
            },
        },
        Spec: batchv1.JobSpec{
            Completions:             &completions,
            Parallelism:             &parallelism,
            BackoffLimit:            &backoffLimit,
            TTLSecondsAfterFinished: &ttl,
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{
                    Labels: labels,
                },
                Spec: corev1.PodSpec{
                    ServiceAccountName: spec.ServiceAccount,
                    RestartPolicy:      corev1.RestartPolicyNever,
                    // RestartPolicyNever: if the container exits non-zero, the pod
                    // fails immediately. RestartPolicyOnFailure would restart in-place,
                    // conflicting with Orion's retry logic.
                    Containers: []corev1.Container{
                        {
                            Name:            "orion-job",
                            Image:           spec.Image,
                            Command:         spec.Command,
                            Args:            spec.Args,
                            Env:             envVars,
                            Resources:       resources,
                            ImagePullPolicy: corev1.PullIfNotPresent,
                        },
                    },
                },
            },
        },
    }
}

// buildResourceRequirements converts our ResourceRequest into K8s ResourceRequirements.
// We set both Requests and Limits to the same values (Guaranteed QoS class).
//
// QoS classes in Kubernetes:
//   Guaranteed: Requests == Limits → never killed for OOM unless node is full
//   Burstable:  Requests < Limits  → may be killed if node is under pressure
//   BestEffort: No requests/limits → first to be killed
//
// For ML training, Guaranteed is the right choice — we don't want the training
// pod killed mid-run because another workload spiked its memory.
func buildResourceRequirements(r domain.ResourceRequest) corev1.ResourceRequirements {
    requests := corev1.ResourceList{}
    limits   := corev1.ResourceList{}

    // CPU: e.g., "500m" (500 millicores = 0.5 CPU) or "4" (4 cores)
    if r.CPU != "" {
        q := resource.MustParse(r.CPU)
        requests[corev1.ResourceCPU] = q
        limits[corev1.ResourceCPU] = q
    }

    // Memory: e.g., "1Gi", "16Gi", "512Mi"
    if r.Memory != "" {
        q := resource.MustParse(r.Memory)
        requests[corev1.ResourceMemory] = q
        limits[corev1.ResourceMemory] = q
    }

    // GPU: uses the NVIDIA device plugin resource name.
    // Requires the NVIDIA GPU Operator or device plugin running on the cluster.
    // GPU resources cannot be fractional — must be an integer.
    if r.GPU > 0 {
        q := resource.MustParse(fmt.Sprintf("%d", r.GPU))
        requests["nvidia.com/gpu"] = q
        limits["nvidia.com/gpu"] = q
    }

    return corev1.ResourceRequirements{
        Requests: requests,
        Limits:   limits,
    }
}

// buildEnvVars converts a flat map[string]string into []corev1.EnvVar.
// Order is sorted alphabetically for deterministic output (useful for tests and diffs).
func buildEnvVars(envMap map[string]string) []corev1.EnvVar {
    if len(envMap) == 0 {
        return nil
    }
    // Sort keys for deterministic ordering
    keys := make([]string, 0, len(envMap))
    for k := range envMap {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    vars := make([]corev1.EnvVar, 0, len(envMap))
    for _, k := range keys {
        vars = append(vars, corev1.EnvVar{Name: k, Value: envMap[k]})
    }
    return vars
}
```

### Key decisions in spec.go

**`backoffLimit: 0`** — Critical. If Kubernetes retried the pod automatically, Orion would see the job still running (pod restarting) even after a failure, breaking the state machine. With `backoffLimit=0`, Kubernetes gives up after the first container failure. Orion handles all retry logic.

**`RestartPolicy: Never`** — Same reason as above. `OnFailure` would restart the container in the same pod; Orion would never see a failed pod, just a pod that keeps restarting.

**Guaranteed QoS** — Setting requests == limits for CPU and memory. ML training is latency-sensitive: we don't want the pod killed or throttled mid-epoch because another pod spiked. Guaranteed QoS means Kubernetes will never evict this pod unless the entire node is full.

**`TTLSecondsAfterFinished`** — Auto-cleanup. Without this, completed K8s Jobs accumulate indefinitely. The default 86400 (24 hours) gives a window for log inspection before cleanup.

---

## 7. File 2: `internal/worker/k8s/executor.go` — The Executor

This is the most complex file in Phase 4. It handles:
1. Validating the spec
2. Creating the K8s Job
3. Watching for completion with timeout
4. Cleaning up on failure

### Complete code to write

```go
package k8s

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "time"

    batchv1 "k8s.io/api/batch/v1"
    metav1  "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/watch"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"

    "github.com/shreeharsh-a/orion/internal/config"
    "github.com/shreeharsh-a/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// KubernetesExecutor
// ─────────────────────────────────────────────────────────────────────────────

// KubernetesExecutor implements worker.Executor for jobs with type = "k8s_job".
//
// Execution flow:
//   1. Validate KubernetesSpec is present and complete
//   2. Translate to batchv1.Job via spec.go
//   3. Create the Job in the cluster
//   4. Watch the Job status until: Succeeded, Failed, or ctx cancelled
//   5. Return nil (success) or error (failure — pool handles retry)
//
// The executor uses kubernetes.Interface (not *kubernetes.Clientset) so tests
// can inject a fake client with identical behaviour and no network calls.
type KubernetesExecutor struct {
    client           kubernetes.Interface
    defaultNamespace string
    pollInterval     time.Duration // how often to poll if watch fails
    logger           *slog.Logger
}

// ExecutorConfig holds configuration for the KubernetesExecutor.
type ExecutorConfig struct {
    DefaultNamespace string
    PollInterval     time.Duration // default 5s; used as fallback if Watch returns early
}

// NewKubernetesExecutor creates a KubernetesExecutor.
// client must be a real *kubernetes.Clientset or a fake.NewSimpleClientset() for tests.
func NewKubernetesExecutor(client kubernetes.Interface, cfg ExecutorConfig, logger *slog.Logger) *KubernetesExecutor {
    if client == nil {
        panic("k8s.NewKubernetesExecutor: client must not be nil")
    }
    ns := cfg.DefaultNamespace
    if ns == "" {
        ns = "orion-jobs" // safe default namespace
    }
    interval := cfg.PollInterval
    if interval == 0 {
        interval = 5 * time.Second
    }
    return &KubernetesExecutor{
        client:           client,
        defaultNamespace: ns,
        pollInterval:     interval,
        logger:           logger,
    }
}

// CanExecute returns true only for k8s_job type.
func (e *KubernetesExecutor) CanExecute(jobType domain.JobType) bool {
    return jobType == domain.JobTypeKubernetes
}

// Execute creates a Kubernetes Job and waits for it to complete.
//
// The Orion job ID is embedded in the K8s Job name and labels for traceability.
// If the context is cancelled (deadline, SIGTERM), we attempt to delete the
// K8s Job before returning — leaving no orphaned pods consuming GPU resources.
func (e *KubernetesExecutor) Execute(ctx context.Context, job *domain.Job) error {
    spec := job.Payload.KubernetesSpec
    if spec == nil {
        return fmt.Errorf("job %s has type k8s_job but kubernetes_spec is nil in payload", job.ID)
    }
    if spec.Image == "" {
        return fmt.Errorf("job %s: kubernetes_spec.image is required", job.ID)
    }
    if len(spec.Command) == 0 {
        return fmt.Errorf("job %s: kubernetes_spec.command is required (at least one element)", job.ID)
    }

    // Resolve namespace: use spec.Namespace if set, fall back to executor default.
    namespace := spec.Namespace
    if namespace == "" {
        namespace = e.defaultNamespace
    }

    k8sJob := buildK8sJob(job, spec)
    jobName := k8sJob.Name

    e.logger.Info("creating kubernetes job",
        "orion_job_id", job.ID,
        "k8s_job_name", jobName,
        "namespace", namespace,
        "image", spec.Image,
        "cpu", spec.ResourceRequest.CPU,
        "memory", spec.ResourceRequest.Memory,
        "gpu", spec.ResourceRequest.GPU,
    )

    // ── Create ───────────────────────────────────────────────────────────────
    _, err := e.client.BatchV1().Jobs(namespace).Create(ctx, k8sJob, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("creating k8s job %s: %w", jobName, err)
    }

    e.logger.Info("kubernetes job created, watching for completion",
        "k8s_job_name", jobName,
        "namespace", namespace,
    )

    // ── Watch ────────────────────────────────────────────────────────────────
    // On success or failure, always attempt cleanup of the K8s Job resource.
    // On context cancellation, delete the pod to avoid zombie GPU usage.
    execErr := e.waitForCompletion(ctx, jobName, namespace)

    if execErr != nil && errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
        // Context was cancelled (SIGTERM or deadline). Clean up the K8s Job
        // so we don't leave GPU pods running with no one monitoring them.
        e.logger.Warn("context cancelled, deleting kubernetes job",
            "k8s_job_name", jobName,
            "namespace", namespace,
            "reason", execErr,
        )
        deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        propagation := metav1.DeletePropagationForeground
        _ = e.client.BatchV1().Jobs(namespace).Delete(deleteCtx, jobName, metav1.DeleteOptions{
            PropagationPolicy: &propagation,
        })
    }

    return execErr
}

// waitForCompletion watches the Kubernetes Job until it succeeds, fails,
// or the context is cancelled.
//
// Watch is preferred over polling because it receives events in real-time
// rather than checking every N seconds. However, Kubernetes Watch connections
// can drop (network issues, API server restart), so we include a fallback
// poll loop with a timeout.
func (e *KubernetesExecutor) waitForCompletion(ctx context.Context, jobName, namespace string) error {
    watcher, err := e.client.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
        FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
        Watch:         true,
    })
    if err != nil {
        // Watch failed to start — fall back to polling
        e.logger.Warn("watch failed to start, falling back to poll", "err", err)
        return e.pollForCompletion(ctx, jobName, namespace)
    }
    defer watcher.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        case event, ok := <-watcher.ResultChan():
            if !ok {
                // Watch channel closed (connection dropped, API server restart).
                // Fall back to polling for the remainder.
                e.logger.Warn("watch channel closed, falling back to poll",
                    "k8s_job_name", jobName,
                )
                return e.pollForCompletion(ctx, jobName, namespace)
            }

            if event.Type == watch.Error {
                e.logger.Error("watch error event", "k8s_job_name", jobName, "event", event)
                return e.pollForCompletion(ctx, jobName, namespace)
            }

            k8sJob, ok := event.Object.(*batchv1.Job)
            if !ok {
                continue // unexpected object type, ignore
            }

            result, message := jobStatus(k8sJob)
            e.logger.Debug("kubernetes job status update",
                "k8s_job_name", jobName,
                "result", result,
                "message", message,
            )

            switch result {
            case jobSucceeded:
                e.logger.Info("kubernetes job succeeded",
                    "k8s_job_name", jobName,
                    "namespace", namespace,
                )
                return nil

            case jobFailed:
                return fmt.Errorf("kubernetes job %s failed: %s", jobName, message)
            // jobPending / jobRunning: continue watching
            }
        }
    }
}

// pollForCompletion is the fallback when Watch is unavailable.
// Polls the Job status every pollInterval until success, failure, or ctx cancel.
func (e *KubernetesExecutor) pollForCompletion(ctx context.Context, jobName, namespace string) error {
    ticker := time.NewTicker(e.pollInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        case <-ticker.C:
            k8sJob, err := e.client.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
            if err != nil {
                e.logger.Warn("poll: failed to get job status", "k8s_job_name", jobName, "err", err)
                continue
            }

            result, message := jobStatus(k8sJob)
            switch result {
            case jobSucceeded:
                return nil
            case jobFailed:
                return fmt.Errorf("kubernetes job %s failed: %s", jobName, message)
            }
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Job status helpers
// ─────────────────────────────────────────────────────────────────────────────

type k8sJobResult int

const (
    jobPending   k8sJobResult = iota
    jobRunning
    jobSucceeded
    jobFailed
)

// jobStatus inspects a batchv1.Job's status conditions to determine
// whether it has succeeded, failed, is still running, or is pending.
//
// Kubernetes sets the following conditions on Jobs:
//   Complete: True  → job succeeded (all completions finished)
//   Failed:   True  → job failed (backoffLimit exceeded)
//
// The "succeeded" and "failed" integer counters are also useful:
//   job.Status.Succeeded > 0  → at least one pod succeeded
//   job.Status.Failed > 0     → at least one pod failed (and backoffLimit=0 means we're done)
func jobStatus(job *batchv1.Job) (k8sJobResult, string) {
    // Check conditions first — these are the authoritative completion signals.
    for _, cond := range job.Status.Conditions {
        if cond.Type == batchv1.JobComplete && cond.Status == "True" {
            return jobSucceeded, ""
        }
        if cond.Type == batchv1.JobFailed && cond.Status == "True" {
            return jobFailed, cond.Message
        }
    }

    // Check raw counters as a fallback (conditions may not be set immediately).
    if job.Status.Succeeded > 0 {
        return jobSucceeded, ""
    }
    if job.Status.Failed > 0 {
        return jobFailed, fmt.Sprintf("pod failed (%d failure(s))", job.Status.Failed)
    }
    if job.Status.Active > 0 {
        return jobRunning, ""
    }
    return jobPending, ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Client builder (used in cmd/worker/main.go)
// ─────────────────────────────────────────────────────────────────────────────

// BuildK8sClient creates a kubernetes.Interface from the Orion Kubernetes config.
// Exported so cmd/worker/main.go can call it.
//
// In-cluster mode: reads the ServiceAccount token from the pod filesystem.
// Out-of-cluster mode: reads ~/.kube/config (or KUBECONFIG env var).
func BuildK8sClient(cfg config.KubernetesConfig) (kubernetes.Interface, error) {
    var restCfg *rest.Config
    var err error

    if cfg.InCluster {
        restCfg, err = rest.InClusterConfig()
        if err != nil {
            return nil, fmt.Errorf("building in-cluster k8s config: %w", err)
        }
    } else {
        kubeconfigPath := cfg.KubeconfigPath
        if kubeconfigPath == "" {
            kubeconfigPath = clientcmd.RecommendedHomeFile // ~/.kube/config
        }
        restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
        if err != nil {
            return nil, fmt.Errorf("building k8s config from %s: %w", kubeconfigPath, err)
        }
    }

    client, err := kubernetes.NewForConfig(restCfg)
    if err != nil {
        return nil, fmt.Errorf("creating k8s client: %w", err)
    }
    return client, nil
}
```

---

## 8. File 3: `internal/worker/k8s/executor_test.go` — Tests with Fake Client

The fake client is the key to testing the Kubernetes executor without a real cluster.

### What we test

| Test | What it proves |
|---|---|
| `TestKubernetesExecutor_CanExecute` | Routes `k8s_job` to this executor, not inline jobs |
| `TestBuildK8sJob_BasicFields` | Image, command, args, labels are set correctly |
| `TestBuildK8sJob_ResourceRequirements` | CPU, memory, GPU resources translated correctly |
| `TestBuildK8sJob_EnvVars` | Environment variables passed to container |
| `TestBuildK8sJob_BackoffLimitZero` | Kubernetes not retrying independently |
| `TestBuildK8sJob_TTLDefault` | Default TTL applied when spec.TTLSeconds = 0 |
| `TestBuildK8sJob_RestartPolicyNever` | Pod doesn't restart, Orion owns retries |
| `TestKubernetesExecutor_NilSpec` | Returns error for missing kubernetes_spec |
| `TestKubernetesExecutor_EmptyImage` | Returns error for empty image |
| `TestKubernetesExecutor_EmptyCommand` | Returns error for missing command |
| `TestJobStatus_Succeeded` | Condition parsing: Complete=True → succeeded |
| `TestJobStatus_Failed` | Condition parsing: Failed=True → failed |
| `TestJobStatus_Running` | Active > 0 → running |
| `TestJobStatus_Pending` | No conditions, no active pods → pending |

### Testing strategy — the fake client

```go
import (
    "k8s.io/client-go/kubernetes/fake"
    batchv1 "k8s.io/api/batch/v1"
    metav1  "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/watch"
    k8stesting "k8s.io/client-go/testing"
)

func TestExecutor_Execute_Succeeds(t *testing.T) {
    // Create a fake client
    fakeClient := fake.NewSimpleClientset()

    // Set up a watcher that will simulate the job succeeding
    watcher := watch.NewFake()
    fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
        return true, watcher, nil
    })

    executor := NewKubernetesExecutor(fakeClient, ExecutorConfig{
        DefaultNamespace: "test-ns",
        PollInterval:     100 * time.Millisecond,
    }, testLogger())

    // Submit execution in a goroutine
    errCh := make(chan error, 1)
    go func() {
        errCh <- executor.Execute(context.Background(), makeK8sJob())
    }()

    // Simulate: job transitions to Succeeded
    time.Sleep(50 * time.Millisecond) // let Create() be called
    watcher.Modify(makeSucceededK8sJob("orion-testjob"))

    err := <-errCh
    if err != nil {
        t.Errorf("expected nil on success, got: %v", err)
    }
}
```

The fake client intercepts API calls and returns pre-configured responses. The watcher allows tests to inject status transitions in a controlled sequence.

---

## 9. File 4: `cmd/worker/main.go` — Updated Wiring

Replace the commented-out K8s block with real wiring:

```go
// ── 7b. Kubernetes Client (Phase 4) ──────────────────────────────────────────
// Build the k8s client based on config:
//   ORION_K8S_IN_CLUSTER=true  → use ServiceAccount token (in-cluster)
//   ORION_K8S_IN_CLUSTER=false → use ~/.kube/config (local dev)
k8sClient, err := k8s.BuildK8sClient(cfg.Kubernetes)
if err != nil {
    // K8s is optional — if it fails, log a warning and continue without it.
    // Workers without a k8s client can still handle inline jobs.
    // k8s_job submissions will fail with "no executor registered" and retry.
    logger.Warn("failed to build kubernetes client, k8s_job type unavailable",
        "err", err,
        "in_cluster", cfg.Kubernetes.InCluster,
    )
    k8sClient = nil
}

// Build executor list
executors := []worker.Executor{inlineExecutor}

if k8sClient != nil {
    k8sExecutor := k8s.NewKubernetesExecutor(k8sClient, k8s.ExecutorConfig{
        DefaultNamespace: cfg.Kubernetes.DefaultNamespace,
        PollInterval:     5 * time.Second,
    }, logger)
    executors = append(executors, k8sExecutor)
    logger.Info("kubernetes executor ready",
        "default_namespace", cfg.Kubernetes.DefaultNamespace,
        "in_cluster", cfg.Kubernetes.InCluster,
    )
} else {
    logger.Warn("kubernetes executor not registered — k8s_job submissions will fail")
}
```

### Why k8s failure is non-fatal

If the worker can't connect to Kubernetes (no cluster available, wrong kubeconfig path), it should still serve `inline` jobs. Failing the entire worker startup because Kubernetes is unavailable would be overly brittle for development environments where `kind` isn't running.

`k8s_job` submissions will fail with "no executor registered" and enter the retry cycle. When the cluster becomes available and the worker restarts, those jobs will succeed.

---

## 10. File 5: `deploy/k8s/rbac.yaml` — Kubernetes RBAC

The Orion worker pod needs permission to create, get, watch, and delete Jobs and Pods in the target namespace. Kubernetes denies all API calls by default — RBAC explicitly grants permissions.

### Complete RBAC manifest

```yaml
# deploy/k8s/rbac.yaml
# Kubernetes RBAC for Orion Worker
#
# The worker process creates, monitors, and deletes Kubernetes Jobs.
# This ServiceAccount + ClusterRole + Binding grants exactly those permissions.
#
# Apply with:  kubectl apply -f deploy/k8s/rbac.yaml
# Verify with: kubectl auth can-i create jobs --as=system:serviceaccount:orion:orion-worker -n ml-training

---
apiVersion: v1
kind: Namespace
metadata:
  name: orion-system     # namespace where Orion services run

---
apiVersion: v1
kind: Namespace
metadata:
  name: orion-jobs       # default namespace for ML job pods (separate from system)

---
# ServiceAccount used by the Orion worker pod.
# Referenced in pod spec: spec.serviceAccountName: orion-worker
apiVersion: v1
kind: ServiceAccount
metadata:
  name: orion-worker
  namespace: orion-system
  labels:
    app: orion
    component: worker

---
# ClusterRole grants permissions across ALL namespaces.
# This allows Orion to schedule jobs in "ml-training", "orion-jobs", or any
# namespace specified in kubernetes_spec.namespace.
#
# Principle of least privilege: we grant only the exact verbs needed.
#   create  : submit a new Job
#   get     : read Job status (poll fallback)
#   watch   : receive real-time Job status events (primary mechanism)
#   delete  : clean up on context cancellation
#   list    : needed by Watch internally
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: orion-job-manager
  labels:
    app: orion
rules:
  # Kubernetes Jobs (batchv1.Job) — the main resource we manage
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "delete"]

  # Pods — needed to read logs and exit codes from job pods
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]

  # Pod logs — for capturing training output (Phase 6 enhancement)
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]

---
# ClusterRoleBinding — attaches the role to the service account
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: orion-job-manager-binding
  labels:
    app: orion
subjects:
  - kind: ServiceAccount
    name: orion-worker
    namespace: orion-system
roleRef:
  kind: ClusterRole
  apiGroup: rbac.authorization.k8s.io
  name: orion-job-manager
```

### Why ClusterRole instead of Role?

`Role` is namespace-scoped — it would only grant permissions in one namespace. Since job specs can specify any namespace (`kubernetes_spec.namespace`), we need cross-namespace permissions. A `ClusterRole` with a `ClusterRoleBinding` grants permissions everywhere.

In a production hardened environment, you'd use a `Role` per namespace (more secure, more complex). For Orion's use case, ClusterRole is the practical choice.

---

## 11. The Watch Loop — Waiting for Pod Completion

This is the most nuanced part of Phase 4. Understanding it prevents subtle bugs.

### Why Watch beats polling

```
Polling every 5s:                    Watch:
──────────────────────────           ──────────────────────────
t=0:   job created                   t=0:   job created, watch opened
t=5:   GET job → Running             t=12:  event: Running
t=10:  GET job → Running             t=47:  event: Succeeded
t=15:  GET job → Running             → return nil immediately
t=20:  GET job → Running
t=25:  GET job → Succeeded
→ ~5s average latency overhead

For a 2-hour training run: 24 extra GET calls vs 2 Watch events.
For 100 concurrent jobs: 2400 GET calls/hour vs 200 Watch events.
```

Watch is dramatically more efficient. But it can fail.

### When the Watch channel closes unexpectedly

The Watch HTTP connection is long-lived. It can close because:
- Network blip between worker and K8s API server
- K8s API server restarted
- Load balancer timeout on idle connection
- Watch resource version too old (cluster resynced)

When `watcher.ResultChan()` closes, we fall back to polling. Polling is correct, just less efficient. The job will not be lost — it continues running in Kubernetes regardless of whether we're watching it.

### The watch timeout edge case

If the Orion job's context is cancelled (SIGTERM or deadline) *while* we're watching, we:
1. Return `ctx.Err()` from `waitForCompletion`
2. Delete the Kubernetes Job with a fresh context (30s timeout)
3. Return the error to the pool

The pool then calls `MarkJobFailed` and the job enters the retry cycle. The next attempt will create a new K8s Job. This is correct — we don't want to leave zombie GPU pods running.

### Pod exit codes

When a pod fails, the exit code is in `pod.Status.ContainerStatuses[0].State.Terminated.ExitCode`. We don't read this in Phase 4 (the Job's Failed condition is sufficient), but Phase 6 will capture it in the `job_executions.exit_code` field for debugging.

---

## 12. Error Taxonomy — Every Failure Mode

| Error | Cause | Pool Response | Job Outcome |
|---|---|---|---|
| `kubernetes_spec is nil` | Missing payload | `MarkJobFailed` | Retried → dead (spec never fixes itself) |
| `image is empty` | Invalid spec | `MarkJobFailed` | Retried → dead |
| `Create() returns error` | K8s API unreachable, RBAC denied, quota exceeded | `MarkJobFailed` | Retried (transient errors may resolve) |
| `ImagePullBackOff` | Wrong image name, private registry auth missing | Watch sees Failed condition → `MarkJobFailed` | Retried → dead (image doesn't fix itself without redeployment) |
| `OOMKilled` | Pod exceeded memory limit | Watch sees Failed → `MarkJobFailed` | Retried (may succeed with different data slice) |
| `Container exited 1` | Training script error | Watch sees Failed → `MarkJobFailed` with message | Retried up to max_retries |
| `context.DeadlineExceeded` | Job's deadline field passed | `MarkJobFailed`, K8s Job deleted | Retried |
| `context.Canceled` | Worker received SIGTERM | `MarkJobFailed`, K8s Job deleted | Retried (job was fine, worker was killed) |
| Watch channel closes | Network/API server issue | Falls back to polling → continues | No impact on outcome |

### GPU-specific failures

`nvidia.com/gpu` resource allocation can fail with:
- `Insufficient nvidia.com/gpu` — no GPU nodes with free GPUs. Pod stays `Pending`. Orion will watch forever (or until deadline).
- This is a scheduling failure, not a job failure. Phase 8 adds timeout-based handling for stuck-pending pods.

For Phase 4: set a reasonable `job.Deadline` on GPU jobs to avoid infinite pending waits.

---

## 13. GPU Resource Scheduling

### How GPU resources work in Kubernetes

Kubernetes treats GPUs as schedulable resources, just like CPU and memory. The NVIDIA device plugin (or GPU Operator) registers `nvidia.com/gpu` as a resource on GPU-capable nodes.

```yaml
# What gets set on the container (from spec.go):
resources:
  requests:
    nvidia.com/gpu: "2"
    cpu: "4"
    memory: "16Gi"
  limits:
    nvidia.com/gpu: "2"
    cpu: "4"
    memory: "16Gi"
```

Kubernetes only schedules this pod on a node that has at least 2 free GPUs. If no such node exists, the pod stays `Pending`.

### GPU job submission example

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "train-resnet50",
    "type": "k8s_job",
    "queue_name": "high",
    "priority": 9,
    "max_retries": 2,
    "deadline": "2024-01-16T10:00:00Z",
    "payload": {
      "kubernetes_spec": {
        "image": "pytorch/pytorch:2.0-cuda11.7-cudnn8-runtime",
        "command": ["python", "-m", "train"],
        "args": ["--model=resnet50", "--epochs=50", "--batch-size=256"],
        "namespace": "ml-training",
        "resources": {
          "cpu": "8",
          "memory": "32Gi",
          "gpu": 2
        },
        "env_vars": {
          "WANDB_PROJECT": "orion-training",
          "S3_BUCKET": "my-ml-artifacts",
          "CUDA_VISIBLE_DEVICES": "0,1"
        },
        "service_account": "ml-training-sa",
        "ttl_seconds": 3600
      }
    }
  }'
```

---

## 14. Context and Deadline Handling

Phase 4 introduces a critical timeout scenario that didn't exist in Phase 3.

### Inline (Phase 3) — context cancellation is instant

```go
// Handler returns immediately when ctx is cancelled
select {
case <-time.After(duration):
    return nil
case <-ctx.Done():
    return ctx.Err()  // instant
}
```

### Kubernetes (Phase 4) — context cancellation requires cleanup

```go
// Execute is watching a remote pod. When ctx is cancelled:
case <-ctx.Done():
    // 1. Return from waitForCompletion
    // 2. In Execute(): detect it's a context error
    // 3. DELETE the K8s Job (so the GPU pod stops)
    // 4. Return ctx.Err() to pool
    // 5. Pool calls MarkJobFailed
```

The additional step is the K8s Job deletion. Without it, the training pod keeps running on the GPU node consuming resources with no one monitoring it.

### Why we use a fresh context for deletion

```go
deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
_ = e.client.BatchV1().Jobs(namespace).Delete(deleteCtx, jobName, ...)
```

We cannot use the original `ctx` — it's already cancelled. We use `context.Background()` with a 30-second timeout to give the deletion call time to succeed.

---

## 15. Local Testing Strategy — kind Cluster

For local development without a real Kubernetes cluster with GPUs:

### Step 1 — Install kind

```bash
# kind = Kubernetes IN Docker — runs a full K8s cluster as Docker containers
go install sigs.k8s.io/kind@latest
# or: brew install kind
```

### Step 2 — Create a local cluster

```bash
kind create cluster --name orion-dev
# Creates a 1-node Kubernetes cluster inside Docker
# Automatically updates ~/.kube/config

kubectl cluster-info --context kind-orion-dev
# Kubernetes control plane is running at https://127.0.0.1:PORT
```

### Step 3 — Apply RBAC

```bash
kubectl apply -f deploy/k8s/rbac.yaml

# Verify:
kubectl get serviceaccount orion-worker -n orion-system
kubectl get clusterrolebinding orion-job-manager-binding
```

### Step 4 — Submit a CPU-only test job (no GPU needed for smoke tests)

```bash
# Set config
export ORION_K8S_IN_CLUSTER=false
export KUBECONFIG=~/.kube/config
export ORION_K8S_NAMESPACE=orion-jobs

# Start worker
make run-worker

# Submit a simple k8s_job that just echoes something
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "k8s-smoke-test",
    "type": "k8s_job",
    "payload": {
      "kubernetes_spec": {
        "image": "busybox:latest",
        "command": ["sh", "-c"],
        "args": ["echo Hello from Orion K8s Job && sleep 2 && exit 0"],
        "namespace": "orion-jobs",
        "resources": {"cpu": "100m", "memory": "64Mi"}
      }
    },
    "max_retries": 1
  }' | jq -r .id)

echo "Job ID: $JOB_ID"
```

### Step 5 — Watch pod creation in kind

```bash
# In another terminal:
watch kubectl get pods -n orion-jobs
# NAME                    READY   STATUS    RESTARTS   AGE
# orion-abc12345-...      0/1     Pending   0          1s
# orion-abc12345-...      1/1     Running   0          5s
# orion-abc12345-...      0/1     Completed 0          8s
```

### Step 6 — Watch job status in Orion

```bash
watch -n1 "curl -s http://localhost:8080/jobs/$JOB_ID | jq '{status,attempt}'"
# status=queued → scheduled → running → completed
```

### Simulating GPU without real GPUs

For CI and local tests, use `busybox` or `alpine` images with CPU-only resource specs. The GPU path in `spec.go` is tested via the fake client — you don't need a real GPU to test the code that generates GPU resource specs.

---

## 16. Step-by-Step Build Order

### Step 1 — Create `internal/worker/k8s/` directory and `spec.go`

```bash
mkdir -p internal/worker/k8s
# Write spec.go
go build ./internal/worker/k8s/...
# Must compile with zero errors
```

### Step 2 — Create `executor.go`

```bash
# Write executor.go
go build ./internal/worker/k8s/...
# Must compile
```

### Step 3 — Write `executor_test.go`

```bash
# Write tests
go test -race ./internal/worker/k8s/... -v
# All tests pass, no data races
```

### Step 4 — Update `cmd/worker/main.go`

```bash
go build ./cmd/worker/...
# Must compile
```

### Step 5 — Apply RBAC to your cluster

```bash
kubectl apply -f deploy/k8s/rbac.yaml
kubectl get clusterrolebinding orion-job-manager-binding  # verify
```

### Step 6 — Build all binaries

```bash
make build
# All 3 binaries compile
```

### Step 7 — Run integration verification (next section)

---

## 17. Complete End-to-End Test Sequence

### Prerequisites

```bash
# Orion infrastructure + kind cluster running
docker compose ps          # postgres, redis, jaeger healthy
kubectl get nodes          # kind cluster node Ready
make migrate-up            # tables exist
make build                 # fresh binaries
```

### Test 1 — K8s smoke test (CPU only, no GPU)

```bash
# Start services (3 terminals)
make run-api
make run-scheduler
ORION_K8S_IN_CLUSTER=false make run-worker
# Worker log: INFO msg="kubernetes executor ready" default_namespace=orion-jobs

# Submit
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "k8s-smoke-test",
    "type": "k8s_job",
    "payload": {
      "kubernetes_spec": {
        "image": "busybox:latest",
        "command": ["sh", "-c"],
        "args": ["echo test && exit 0"],
        "namespace": "orion-jobs",
        "resources": {"cpu": "100m", "memory": "64Mi"}
      }
    }
  }' | jq -r .id)

# Watch completion
for i in $(seq 1 30); do
  STATUS=$(curl -s http://localhost:8080/jobs/$JOB_ID | jq -r .status)
  echo "$(date +%H:%M:%S) $STATUS"
  [ "$STATUS" = "completed" ] && echo "✅ TEST 1 PASSED" && break
  sleep 2
done
```

Expected timeline:
```
10:00:00 queued
10:00:02 scheduled       ← scheduler dispatch
10:00:03 running         ← worker created K8s Job, watching
10:00:15 completed       ← pod ran, exited 0, MarkJobCompleted
✅ TEST 1 PASSED
```

### Test 2 — Verify K8s pod was created and deleted

```bash
# Check pod was created (may be deleted already if TTL passed)
kubectl get pods -n orion-jobs -l orion-job-id=$JOB_ID
# If TTL hasn't passed: Completed
# If TTL passed: already deleted (expected)

# Check execution audit log
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .
# [{"attempt":1,"status":"completed","started_at":"...","finished_at":"..."}]
echo "✅ TEST 2 PASSED — audit log written"
```

### Test 3 — Failure path (container exits non-zero)

```bash
JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "k8s-fail-test",
    "type": "k8s_job",
    "max_retries": 1,
    "payload": {
      "kubernetes_spec": {
        "image": "busybox:latest",
        "command": ["sh", "-c"],
        "args": ["echo failing && exit 1"],
        "namespace": "orion-jobs",
        "resources": {"cpu": "100m", "memory": "64Mi"}
      }
    }
  }' | jq -r .id)

# Watch the retry cycle
watch -n2 "curl -s http://localhost:8080/jobs/$JOB_ID | jq '{status,attempt,error_message}'"
# failed(1) → retrying → queued → failed(2) → dead
echo "✅ TEST 3 PASSED — failure and retry cycle"
```

### Test 4 — Verify RBAC is correct

```bash
kubectl auth can-i create jobs \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# yes

kubectl auth can-i delete jobs \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# yes

kubectl auth can-i create pods \
  --as=system:serviceaccount:orion-system:orion-worker \
  -n orion-jobs
# no  ← correct, we create Jobs not Pods directly
echo "✅ TEST 4 PASSED — RBAC configured correctly"
```

### Test 5 — Both executor types work simultaneously

```bash
# Submit inline and k8s jobs at the same time
INLINE_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -d '{"type":"inline","payload":{"handler_name":"noop"},"name":"inline-concurrent"}' \
  -H "Content-Type: application/json" | jq -r .id)

K8S_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -d '{"type":"k8s_job","payload":{"kubernetes_spec":{"image":"busybox","command":["echo"],"args":["hello"],"namespace":"orion-jobs","resources":{"cpu":"100m","memory":"64Mi"}}},"name":"k8s-concurrent"}' \
  -H "Content-Type: application/json" | jq -r .id)

sleep 20

INLINE_STATUS=$(curl -s http://localhost:8080/jobs/$INLINE_ID | jq -r .status)
K8S_STATUS=$(curl -s http://localhost:8080/jobs/$K8S_ID | jq -r .status)

echo "Inline: $INLINE_STATUS   K8s: $K8S_STATUS"
[ "$INLINE_STATUS" = "completed" ] && [ "$K8S_STATUS" = "completed" ] && \
  echo "✅ TEST 5 PASSED — both executor types work together"
```

---

## 18. Common Mistakes and How to Avoid Them

| Mistake | Symptom | Fix |
|---|---|---|
| `backoffLimit > 0` | K8s retries pod independently, Orion sees job stuck in `running`, attempts count double | Always set `backoffLimit = 0` in spec.go |
| `RestartPolicy: OnFailure` | Failed pod restarts in-place, Orion never sees failure, job stays `running` | Use `RestartPolicy: Never` |
| No `TTLSecondsAfterFinished` | Completed Jobs accumulate until node runs out of resources | Always set TTL (default 86400 = 24h) |
| Not deleting Job on ctx cancel | Zombie GPU pods run forever with no owner | Delete K8s Job in Execute() when `ctx.Err() != nil` |
| Using `ctx` for deletion after cancel | Deletion call fails immediately (ctx already done) | Use `context.WithTimeout(context.Background(), 30s)` |
| RBAC not applied | `Forbidden` errors on Job creation | `kubectl apply -f deploy/k8s/rbac.yaml` |
| Wrong namespace in spec | Pod scheduled in wrong namespace, RBAC may deny it | Verify `kubernetes_spec.namespace` or set `ORION_K8S_NAMESPACE` |
| No deadline on GPU jobs | Pod stays Pending forever waiting for GPU availability | Always set `job.Deadline` for GPU workloads |
| `resource.MustParse` panics | Invalid CPU/memory format (e.g., `"4 cores"` not `"4"`) | Validate format before calling MustParse; use `resource.ParseQuantity` + check error |

---

## 19. Phase 5 Preview

After Phase 4, both executor types work:

```
"inline"   → InlineExecutor  → Go function runs  → completed ✓
"k8s_job"  → K8sExecutor     → K8s pod runs      → completed ✓
```

Phase 5 adds **pipeline orchestration** — chaining jobs together so that `evaluate` only starts after `train` completes, and `deploy` only starts after `evaluate` succeeds.

```
POST /pipelines -d '{
  "nodes": [
    {"id": "preprocess", "job_template": {"type": "inline", ...}},
    {"id": "train",      "job_template": {"type": "k8s_job", ...}},
    {"id": "evaluate",   "job_template": {"type": "k8s_job", ...}},
    {"id": "deploy",     "job_template": {"type": "inline", ...}}
  ],
  "edges": [
    {"source": "preprocess", "target": "train"},
    {"source": "train",      "target": "evaluate"},
    {"source": "evaluate",   "target": "deploy"}
  ]
}'
```

The DAG advancement logic already exists in `domain/pipeline.go` (`ReadyNodes()`). Phase 5 adds:
- `POST /pipelines` handler
- Pipeline advancement loop in the scheduler (after each node completes, check `ReadyNodes()` and enqueue the next)
- `PipelineStore` methods in `postgres/db.go`

---

## Summary Checklist

```
□ internal/worker/k8s/spec.go
    □ jobNameForOrionJob() — "orion-" + UUID[:8]
    □ buildK8sJob() — full translation including labels + annotations
    □ buildResourceRequirements() — CPU, Memory, GPU (nvidia.com/gpu)
    □ buildEnvVars() — sorted []corev1.EnvVar from map[string]string
    □ backoffLimit = 0  (K8s must NOT retry independently)
    □ RestartPolicy = Never  (Orion owns all retries)
    □ Guaranteed QoS (requests == limits)
    □ Default TTL = 86400s if spec.TTLSeconds == 0

□ internal/worker/k8s/executor.go
    □ KubernetesExecutor struct with kubernetes.Interface (not Clientset)
    □ NewKubernetesExecutor(client, cfg, logger) — panics on nil client
    □ CanExecute() — true only for JobTypeKubernetes
    □ Execute() — validate spec, buildK8sJob, Create, waitForCompletion
    □ Execute() — delete K8s Job when ctx cancelled (fresh context)
    □ waitForCompletion() — Watch loop with fallback to poll
    □ pollForCompletion() — fallback with pollInterval ticker
    □ jobStatus() — reads Conditions then counters
    □ BuildK8sClient() — InCluster vs kubeconfig path

□ internal/worker/k8s/executor_test.go
    □ CanExecute tests for inline and k8s_job
    □ spec validation tests (nil spec, empty image, empty command)
    □ buildK8sJob field verification tests
    □ jobStatus tests for all four states
    □ Execute success test via fake client + watcher
    □ Execute failure test via fake client
    □ Context cancellation test (job deleted on cancel)

□ cmd/worker/main.go (update)
    □ Import k8s package
    □ BuildK8sClient() called; failure is non-fatal (logs warning)
    □ KubernetesExecutor appended to executors slice if client built
    □ Startup log confirms executor registered

□ deploy/k8s/rbac.yaml
    □ Namespace: orion-system
    □ Namespace: orion-jobs
    □ ServiceAccount: orion-worker in orion-system
    □ ClusterRole: create/get/list/watch/delete on batch/jobs
    □ ClusterRole: get/list/watch on pods and pods/log
    □ ClusterRoleBinding: orion-worker → orion-job-manager

□ Verification
    □ go test -race ./internal/worker/k8s/... → all pass
    □ make build → zero errors
    □ kubectl apply -f deploy/k8s/rbac.yaml → no errors
    □ Test 1 (busybox smoke test) → status=completed ← FIRST K8S JOB
    □ Test 2 (audit log) → 1 execution record with started_at + finished_at
    □ Test 3 (failure path) → exit 1 → retry → dead
    □ Test 4 (RBAC) → can-i create jobs = yes
    □ Test 5 (concurrent) → inline + k8s both complete simultaneously
```

---

## File Locations After Phase 4

```
orion/
├── internal/
│   └── worker/
│       ├── pool.go              ← unchanged
│       ├── inline.go            ← unchanged (Phase 3)
│       ├── inline_test.go       ← unchanged
│       ├── handlers/
│       │   └── handlers.go      ← unchanged
│       └── k8s/
│           ├── spec.go          ← NEW: KubernetesSpec → batchv1.Job
│           ├── executor.go      ← NEW: Watch loop, Create, cleanup
│           └── executor_test.go ← NEW: fake client tests
├── cmd/
│   └── worker/
│       └── main.go              ← UPDATED: K8s client + executor wired
└── deploy/
    └── k8s/
        └── rbac.yaml            ← NEW: ServiceAccount + ClusterRole
```