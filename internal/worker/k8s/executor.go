package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// KubernetesExecutor
// ─────────────────────────────────────────────────────────────────────────────

// KubernetesExecutor implements worker.Executor for jobs with type = "k8s_job".
//
// Execution flow:
//  1. Validate KubernetesSpec is present and complete
//  2. Translate to batchv1.Job via spec.go
//  3. Create the Job in the cluster
//  4. Watch the Job status until: Succeeded, Failed, or ctx cancelled
//  5. Return nil (success) or error (failure — pool handles retry)
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
	jobPending k8sJobResult = iota
	jobRunning
	jobSucceeded
	jobFailed
)

// jobStatus inspects a batchv1.Job's status conditions to determine
// whether it has succeeded, failed, is still running, or is pending.
//
// Kubernetes sets the following conditions on Jobs:
//
//	Complete: True  → job succeeded (all completions finished)
//	Failed:   True  → job failed (backoffLimit exceeded)
//
// The "succeeded" and "failed" integer counters are also useful:
//
//	job.Status.Succeeded > 0  → at least one pod succeeded
//	job.Status.Failed > 0     → at least one pod failed (and backoffLimit=0 means we're done)
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
