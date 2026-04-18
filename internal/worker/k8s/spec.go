// Package k8s implements the KubernetesExecutor for Orion.
// This file translates domain.KubernetesSpec into a batchv1.Job resource.
//
// The only coupling between Orion's domain model and the Kubernetes API
// lives here. If the K8s API changes, only this file needs updating.
package k8s

import (
	"fmt"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/shreeharshshinde/orion/internal/domain"
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
//
//	orion-job-id  : full UUID of the Orion job (for querying / filtering)
//	orion-job-name: human-readable name
//	managed-by    : "orion" (for RBAC and resource policies)
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
//
//	Guaranteed: Requests == Limits → never killed for OOM unless node is full
//	Burstable:  Requests < Limits  → may be killed if node is under pressure
//	BestEffort: No requests/limits → first to be killed
//
// For ML training, Guaranteed is the right choice — we don't want the training
// pod killed mid-run because another workload spiked its memory.
func buildResourceRequirements(r domain.ResourceRequest) corev1.ResourceRequirements {
	requests := corev1.ResourceList{}
	limits := corev1.ResourceList{}

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
