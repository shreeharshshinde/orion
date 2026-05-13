# ADR-008: Guaranteed QoS Class for Kubernetes ML Job Pods

**Date:** 2024-02-22
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

When `KubernetesExecutor` submits a `batchv1.Job`, each pod has a Quality of Service (QoS) class determined by how resource requests and limits are set. Kubernetes defines three QoS classes:

| Class | Condition | Eviction priority |
|---|---|---|
| **BestEffort** | No requests or limits set | Evicted first |
| **Burstable** | Requests < Limits, or only some resources set | Evicted second |
| **Guaranteed** | Requests == Limits for every resource (CPU, memory, GPU) | Evicted last |

ML training jobs have a critical operational property: **evicting a training pod mid-run wastes all computation completed so far.** A ResNet-50 training run that is 80% complete and gets evicted must restart from its last checkpoint — losing hours of GPU time and compute cost.

The question: which QoS class should Orion assign to ML job pods, and how?

## Decision

**Always set `requests == limits` for every resource (CPU, memory, GPU) on every Kubernetes Job pod created by Orion.** This places all pods in the `Guaranteed` QoS class.

```go
func buildResourceRequirements(r domain.ResourceRequest) corev1.ResourceRequirements {
    requests := corev1.ResourceList{}
    limits   := corev1.ResourceList{}

    if r.CPU != "" {
        q := resource.MustParse(r.CPU)
        requests[corev1.ResourceCPU] = q
        limits[corev1.ResourceCPU] = q   // ← same value
    }
    if r.Memory != "" {
        q := resource.MustParse(r.Memory)
        requests[corev1.ResourceMemory] = q
        limits[corev1.ResourceMemory] = q // ← same value
    }
    if r.GPU > 0 {
        q := resource.MustParse(fmt.Sprintf("%d", r.GPU))
        requests["nvidia.com/gpu"] = q
        limits["nvidia.com/gpu"] = q      // ← same value (required by NVIDIA device plugin)
    }

    return corev1.ResourceRequirements{Requests: requests, Limits: limits}
}
```

Evaluated options:

| QoS class | How | Training job eviction risk | Memory safety |
|---|---|---|---|
| BestEffort | Set no requests/limits | Evicted first under any pressure | Pod can consume all node memory |
| Burstable | Set requests < limits | Evicted under moderate pressure | Can burst but gets killed first |
| **Guaranteed** | **requests == limits** | **Evicted only if node is completely full** | **Hard cap on memory** |

## Consequences

**Positive:**
- Training pods are never killed for OOM or resource pressure unless the entire node runs out of resources — the correct guarantee for long-running ML workloads
- Hard memory limits prevent a single runaway job from OOM-killing other pods on the same node
- GPU resource requests == limits is enforced by the NVIDIA device plugin anyway — this makes it explicit and consistent
- Operators can predict exact node utilization: a node with 32Gi RAM and 2 GPUs can hold exactly `32Gi / job.Memory` pods

**Negative:**
- A job that only needs 8Gi of memory peak but specifies 32Gi cannot "return" the unused 24Gi to other pods — the node capacity is reserved even when idle
- Operators must set accurate resource requests — over-requesting wastes cluster capacity, under-requesting risks OOM kills that bypass Kubernetes's QoS protection (the container OOM killer still fires within the pod)

## Implementation Notes

Located in `internal/worker/k8s/spec.go`. The `buildResourceRequirements` function is called unconditionally for every job. There is no configuration knob to select a different QoS class — `Guaranteed` is the only supported mode for Orion-managed pods. Operators who need `Burstable` scheduling should not use Orion's `k8s_job` type for those workloads.

GPU resources (`nvidia.com/gpu`) require `requests == limits` by the NVIDIA device plugin specification — fractional GPU allocation is not supported. This constraint aligns with Orion's choice and reinforces why `Guaranteed` is the only sensible option for GPU workloads.

## Related Decisions

- ADR-006: `backoffLimit=0` and `RestartPolicyNever` — together with Guaranteed QoS, these three settings give Orion complete control over pod lifecycle, retry timing, and resource isolation.