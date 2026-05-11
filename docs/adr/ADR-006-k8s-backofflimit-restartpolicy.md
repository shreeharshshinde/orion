# ADR-006: `backoffLimit=0` and `RestartPolicy=Never` on Kubernetes Jobs

**Date:** 2024-02-08
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

When `KubernetesExecutor` submits a `batchv1.Job` to Kubernetes, both Kubernetes and Orion have retry mechanisms:

- **Kubernetes retry**: controlled by `spec.backoffLimit` (number of pod restart attempts) and `spec.template.spec.restartPolicy` (whether the container restarts in-place on failure)
- **Orion retry**: controlled by `job.MaxRetries` and the scheduler's `promoteRetryableJobs` loop with full-jitter exponential backoff

If both retry independently, they conflict. Consider `backoffLimit=3` and `MaxRetries=3`:

```
Pod fails once → Kubernetes retries pod 3 more times (backoffLimit)
→ Pod fails again × 3 → Kubernetes marks Job Failed
→ Orion detects Job Failed → Orion retries (MaxRetries)
→ Cycle repeats 3 × 3 = 9 actual execution attempts instead of the expected 3
```

Worse: with `restartPolicy: OnFailure`, Kubernetes restarts the container inside the same pod. Orion's Watch loop sees the pod still `Running` — it never detects the failure. The job appears stuck in `running` state in Orion indefinitely.

## Decision

**Set `backoffLimit=0` and `restartPolicy: Never` on every `batchv1.Job` created by Orion.** Kubernetes is completely removed from the retry loop. Orion owns the entire retry lifecycle.

```go
var backoffLimit int32 = 0  // non-negotiable constant

return &batchv1.Job{
    Spec: batchv1.JobSpec{
        BackoffLimit: &backoffLimit,           // Kubernetes never retries
        Template: corev1.PodTemplateSpec{
            Spec: corev1.PodSpec{
                RestartPolicy: corev1.RestartPolicyNever, // pod dies immediately on failure
            },
        },
    },
}
```

With `backoffLimit=0`: Kubernetes gives up after the first container failure and marks the Job `Failed`. Orion's Watch loop detects `condition.Type=JobFailed`, returns an error to the pool, and the pool calls `MarkJobFailed`. The scheduler's retry loop then re-enqueues the job with backoff — creating a fresh Kubernetes Job on the next attempt.

With `restartPolicy: Never`: the container exits non-zero → the pod immediately enters `Failed` phase → the Watch event fires → Orion detects the failure within seconds.

Evaluated options:

| Configuration | Retry ownership | State machine correctness | Attempt counting |
|---|---|---|---|
| `backoffLimit=3`, `RestartPolicy=OnFailure` | Split (K8s + Orion) | ❌ Broken | ❌ Multiplied |
| `backoffLimit=3`, `RestartPolicy=Never` | Split | ❌ Broken | ❌ Multiplied |
| `backoffLimit=0`, `RestartPolicy=OnFailure` | Orion | ❌ Broken (Watch never fires) | ❌ Invisible |
| **`backoffLimit=0`, `RestartPolicy=Never`** | **Orion only** | **✅ Correct** | **✅ Exact** |

## Consequences

**Positive:**
- Orion's state machine is always authoritative — `job.Attempt` exactly reflects the number of execution attempts
- Retry timing uses Orion's full-jitter backoff — avoids thundering herd when many GPU jobs fail simultaneously
- Kubernetes Job conditions (`Complete`, `Failed`) fire immediately and cleanly
- `GET /jobs/{id}/executions` always shows the correct number of rows

**Negative:**
- A transient infrastructure blip (OOMKill, node eviction) is treated as a job failure and uses one retry slot. Operators must set `MaxRetries` high enough to absorb infrastructure noise.
- Kubernetes cannot batch-retry multiple pods within one Job resource — each Orion retry creates a new `batchv1.Job`. This is intentional: each attempt is an independent, tracked unit.

## Implementation Notes

Located in `internal/worker/k8s/spec.go`. The values are constants set unconditionally — there is no configuration knob to override them. Any future operator that wants Kubernetes-native retry is incompatible with Orion's state machine and must not be added without revisiting this ADR.