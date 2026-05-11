# ADR-005: Accept `kubernetes.Interface` Instead of `*kubernetes.Clientset`

**Date:** 2024-02-01
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Phase 4 introduced `KubernetesExecutor` — a component that creates `batchv1.Job` resources in Kubernetes and watches them to completion. During development, a critical question arose: **how do we test the executor without a real Kubernetes cluster?**

The `client-go` library provides two ways to hold a Kubernetes client:

1. `*kubernetes.Clientset` — the concrete struct returned by `kubernetes.NewForConfig()`
2. `kubernetes.Interface` — the interface that `*kubernetes.Clientset` implements

If `KubernetesExecutor` accepts `*kubernetes.Clientset`, tests must either:
- Connect to a real cluster (slow, environment-dependent, requires kubeconfig)
- Run a local `envtest` API server (300ms startup overhead per test)
- Mock at the HTTP transport layer (brittle, verbose)

## Decision

**Accept `kubernetes.Interface` in all constructors and function signatures** that interact with the Kubernetes API:

```go
// Correct — accepts the interface
type KubernetesExecutor struct {
    client kubernetes.Interface
}

func NewKubernetesExecutor(client kubernetes.Interface, cfg ExecutorConfig, logger *slog.Logger) *KubernetesExecutor

// Wrong — too concrete
type KubernetesExecutor struct {
    client *kubernetes.Clientset  // ← cannot inject fake in tests
}
```

`client-go` ships `k8s.io/client-go/kubernetes/fake` which provides `fake.NewSimpleClientset()` — a complete in-memory implementation of `kubernetes.Interface` that stores objects in memory and supports watchers:

```go
// In tests: no cluster needed
fakeClient := fake.NewSimpleClientset()
watcher := watch.NewFake()
fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
    return true, watcher, nil
})
executor := NewKubernetesExecutor(fakeClient, cfg, logger)

// Simulate pod succeeding
watcher.Modify(succeededJob("orion-abc12345", "test-ns"))
```

Evaluated options:

| Option | Test isolation | Runtime cost | Coupling |
|---|---|---|---|
| Accept `*kubernetes.Clientset` | ❌ requires real cluster | Zero | High |
| HTTP transport mock | Partial | Low | Very high |
| `envtest` local API server | ✅ | 300ms/test | Medium |
| **Accept `kubernetes.Interface`** | ✅ | Zero | Low |

## Consequences

**Positive:**
- All 23 Kubernetes executor tests run in under 50ms total — no cluster, no network
- The fake client exercises the exact same code paths as the real client
- Tests can simulate every failure mode: pod failures, network drops (channel close), context cancellation, RBAC errors
- `BuildK8sClient()` is the only function that constructs a real `*kubernetes.Clientset`, and it is only called in `cmd/worker/main.go`

**Negative:**
- The fake client does not simulate Kubernetes scheduler behaviour (node selection, resource pressure). Integration tests against a real cluster (kind) are still needed for GPU scheduling scenarios.
- Developers must remember to use the interface type — a linter rule enforcing this would help.

## Implementation Notes

Located in `internal/worker/k8s/executor.go`. The same pattern applies to any future component interacting with Kubernetes: always accept the narrowest interface (e.g., `kubernetes.Interface`, `dynamic.Interface`) rather than the concrete struct. This is the standard pattern used throughout the Kubernetes ecosystem, including Kubeflow Pipelines (the upstream project Orion contributes to).