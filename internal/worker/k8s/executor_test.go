package k8s_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/worker/k8s"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// makeK8sJob builds a minimal k8s_job domain.Job for testing.
func makeK8sJob(overrides ...func(*domain.Job)) *domain.Job {
	j := &domain.Job{
		ID:      uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		Name:    "test-training-job",
		Type:    domain.JobTypeKubernetes,
		Attempt: 0,
		Payload: domain.JobPayload{
			KubernetesSpec: &domain.KubernetesSpec{
				Image:     "busybox:latest",
				Command:   []string{"sh", "-c"},
				Args:      []string{"echo hello && exit 0"},
				Namespace: "test-ns",
				ResourceRequest: domain.ResourceRequest{
					CPU:    "100m",
					Memory: "64Mi",
				},
			},
		},
	}
	for _, fn := range overrides {
		fn(j)
	}
	return j
}

// testExecutor creates a KubernetesExecutor backed by a fake client.
func testExecutor(fakeClient *fake.Clientset) *k8s.KubernetesExecutor {
	return k8s.NewKubernetesExecutor(fakeClient, k8s.ExecutorConfig{
		DefaultNamespace: "test-ns",
		PollInterval:     50 * time.Millisecond,
	}, testLogger())
}

// succeededJob returns a batchv1.Job with the Succeeded condition set.
func succeededJob(name, namespace string) *batchv1.Job {
	condTrue := corev1.ConditionStatus("True")
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: batchv1.JobStatus{
			Succeeded: 1,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: condTrue},
			},
		},
	}
}

// failedJob returns a batchv1.Job with the Failed condition set.
func failedJob(name, namespace string, msg string) *batchv1.Job {
	condTrue := corev1.ConditionStatus("True")
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: batchv1.JobStatus{
			Failed: 1,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: condTrue, Message: msg},
			},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CanExecute
// ─────────────────────────────────────────────────────────────────────────────

func TestKubernetesExecutor_CanExecute_K8sJob(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	if !e.CanExecute(domain.JobTypeKubernetes) {
		t.Error("KubernetesExecutor must return true for JobTypeKubernetes")
	}
}

func TestKubernetesExecutor_CanExecute_Inline(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	if e.CanExecute(domain.JobTypeInline) {
		t.Error("KubernetesExecutor must return false for JobTypeInline (handled by InlineExecutor)")
	}
}

func TestKubernetesExecutor_NilClientPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil client")
		}
	}()
	k8s.NewKubernetesExecutor(nil, k8s.ExecutorConfig{}, testLogger())
}

// ─────────────────────────────────────────────────────────────────────────────
// Input validation
// ─────────────────────────────────────────────────────────────────────────────

func TestExecute_NilSpec_ReturnsError(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec = nil
	})
	err := e.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error for nil kubernetes_spec")
	}
}

func TestExecute_EmptyImage_ReturnsError(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.Image = ""
	})
	err := e.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestExecute_EmptyCommand_ReturnsError(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.Command = nil
	})
	err := e.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error for nil command")
	}
}

func TestExecute_EmptyCommandSlice_ReturnsError(t *testing.T) {
	e := testExecutor(fake.NewSimpleClientset())
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.Command = []string{}
	})
	err := e.Execute(context.Background(), job)
	if err == nil {
		t.Fatal("expected error for empty command slice")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// spec.go — buildK8sJob field verification
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildK8sJob_JobName(t *testing.T) {
	job := makeK8sJob()
	// Job ID is "550e8400-e29b-41d4-a716-446655440000" → first 8 chars = "550e8400"
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	// Trigger Create by using the Watch path; intercept at Create level
	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	// Give Execute() time to call Create
	time.Sleep(30 * time.Millisecond)

	// Inspect what was created
	jobs, err := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs: %v", err)
	}
	if len(jobs.Items) == 0 {
		t.Fatal("expected at least one job to be created")
	}

	got := jobs.Items[0].Name
	want := "orion-550e8400"
	if got != want {
		t.Errorf("job name = %q, want %q", got, want)
	}

	// Signal success so Execute returns
	watcher.Modify(succeededJob(want, "test-ns"))
	<-done
}

func TestBuildK8sJob_BackoffLimitIsZero(t *testing.T) {
	job := makeK8sJob()
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	bl := jobs.Items[0].Spec.BackoffLimit
	if bl == nil || *bl != 0 {
		t.Errorf("backoffLimit = %v, want pointer to 0 (Orion must own all retries)", bl)
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_RestartPolicyNever(t *testing.T) {
	job := makeK8sJob()
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	rp := jobs.Items[0].Spec.Template.Spec.RestartPolicy
	if rp != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want %q (pod must not restart — Orion owns retries)", rp, corev1.RestartPolicyNever)
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_ResourceRequirements_CPU_Memory(t *testing.T) {
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.ResourceRequest = domain.ResourceRequest{
			CPU:    "4",
			Memory: "16Gi",
		}
	})
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	container := jobs.Items[0].Spec.Template.Spec.Containers[0]
	wantCPU := resource.MustParse("4")
	gotCPU := container.Resources.Requests[corev1.ResourceCPU]
	if gotCPU.Cmp(wantCPU) != 0 {
		t.Errorf("CPU request = %v, want %v", gotCPU.String(), wantCPU.String())
	}

	wantMem := resource.MustParse("16Gi")
	gotMem := container.Resources.Requests[corev1.ResourceMemory]
	if gotMem.Cmp(wantMem) != 0 {
		t.Errorf("Memory request = %v, want %v", gotMem.String(), wantMem.String())
	}

	// Guaranteed QoS: requests == limits
	gotCPULimit := container.Resources.Limits[corev1.ResourceCPU]
	if gotCPULimit.Cmp(wantCPU) != 0 {
		t.Errorf("CPU limit = %v, want %v (Guaranteed QoS requires requests==limits)", gotCPULimit.String(), wantCPU.String())
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_GPU_Resource(t *testing.T) {
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.ResourceRequest = domain.ResourceRequest{
			CPU:    "8",
			Memory: "32Gi",
			GPU:    2,
		}
	})
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	container := jobs.Items[0].Spec.Template.Spec.Containers[0]
	wantGPU := resource.MustParse("2")
	gotGPU := container.Resources.Requests["nvidia.com/gpu"]
	if gotGPU.Cmp(wantGPU) != 0 {
		t.Errorf("GPU request = %v, want %v", gotGPU.String(), wantGPU.String())
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_EnvVarsSetInSortedOrder(t *testing.T) {
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.EnvVars = map[string]string{
			"ZEBRA": "z",
			"APPLE": "a",
			"MANGO": "m",
		}
	})
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	envs := jobs.Items[0].Spec.Template.Spec.Containers[0].Env
	if len(envs) != 3 {
		t.Fatalf("expected 3 env vars, got %d", len(envs))
	}
	// Sorted: APPLE, MANGO, ZEBRA
	if envs[0].Name != "APPLE" || envs[1].Name != "MANGO" || envs[2].Name != "ZEBRA" {
		t.Errorf("env vars not sorted: %v", envNames(envs))
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_TTLDefaultApplied(t *testing.T) {
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.TTLSeconds = 0 // should default to 86400
	})
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	ttl := jobs.Items[0].Spec.TTLSecondsAfterFinished
	if ttl == nil || *ttl != 86400 {
		t.Errorf("TTLSecondsAfterFinished = %v, want 86400 (24h default)", ttl)
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

func TestBuildK8sJob_OrionLabelsPresent(t *testing.T) {
	job := makeK8sJob()
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Fatal("no job created")
	}

	labels := jobs.Items[0].Labels
	if labels["managed-by"] != "orion" {
		t.Errorf("expected label managed-by=orion, got %q", labels["managed-by"])
	}
	if labels["orion-job-id"] != job.ID.String() {
		t.Errorf("expected label orion-job-id=%s, got %q", job.ID, labels["orion-job-id"])
	}

	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))
	<-done
}

// ─────────────────────────────────────────────────────────────────────────────
// Execution paths
// ─────────────────────────────────────────────────────────────────────────────

func TestExecute_SuccessPath(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(context.Background(), makeK8sJob())
	}()

	// Simulate: job transitions to Succeeded
	time.Sleep(30 * time.Millisecond)
	watcher.Modify(succeededJob("orion-550e8400", "test-ns"))

	if err := <-errCh; err != nil {
		t.Errorf("Execute on success should return nil, got: %v", err)
	}
}

func TestExecute_FailurePath(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(context.Background(), makeK8sJob())
	}()

	time.Sleep(30 * time.Millisecond)
	watcher.Modify(failedJob("orion-550e8400", "test-ns", "OOMKilled: exceeded memory limit"))

	err := <-errCh
	if err == nil {
		t.Fatal("Execute on pod failure should return non-nil error")
	}
	if !containsString(err.Error(), "failed") {
		t.Errorf("error should mention failure, got: %v", err)
	}
}

func TestExecute_ContextCancellation_ReturnsContextError(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	// Watch reactor that never sends any events (job stays pending)
	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(ctx, makeK8sJob())
	}()

	time.Sleep(30 * time.Millisecond)
	cancel() // cancel the context

	err := <-errCh
	if err == nil {
		t.Fatal("Execute with cancelled context should return non-nil error")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestExecute_WatchChannelClose_FallsBackToPoll(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(context.Background(), makeK8sJob())
	}()

	time.Sleep(30 * time.Millisecond)

	// Close the watch channel — simulates network drop
	watcher.Stop()

	// Poll fallback should kick in. Give it a tick to call Get().
	// Pre-create the successful job so Get() returns it.
	time.Sleep(20 * time.Millisecond)
	_ = fakeClient.Tracker().Add(succeededJob("orion-550e8400", "test-ns"))

	err := <-errCh
	if err != nil {
		t.Errorf("expected nil after poll fallback found success, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobStatus (internal logic — tested via black-box Execute tests above,
// plus direct condition-parsing tests below for completeness)
// ─────────────────────────────────────────────────────────────────────────────

func TestExecute_StatusCounters_SucceededCounter(t *testing.T) {
	// Simulate job where conditions are not yet set but Succeeded counter is 1
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(context.Background(), makeK8sJob())
	}()

	time.Sleep(30 * time.Millisecond)

	// Emit a job with Succeeded>0 but no conditions (counter-based detection)
	watcher.Modify(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "orion-550e8400", Namespace: "test-ns"},
		Status:     batchv1.JobStatus{Succeeded: 1},
	})

	if err := <-errCh; err != nil {
		t.Errorf("expected nil when Succeeded=1, got: %v", err)
	}
}

func TestExecute_StatusCounters_FailedCounter(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := testExecutor(fakeClient)

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Execute(context.Background(), makeK8sJob())
	}()

	time.Sleep(30 * time.Millisecond)

	watcher.Modify(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "orion-550e8400", Namespace: "test-ns"},
		Status:     batchv1.JobStatus{Failed: 1},
	})

	if err := <-errCh; err == nil {
		t.Error("expected error when Failed=1")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Namespace resolution
// ─────────────────────────────────────────────────────────────────────────────

func TestExecute_SpecNamespaceTakesPrecedence(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	// Executor default namespace is "executor-ns"
	e := k8s.NewKubernetesExecutor(fakeClient, k8s.ExecutorConfig{
		DefaultNamespace: "executor-ns",
		PollInterval:     50 * time.Millisecond,
	}, testLogger())

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	// Job spec says "ml-training" — should override executor default
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.Namespace = "ml-training"
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	// Check job was created in "ml-training", not "executor-ns"
	jobs, _ := fakeClient.BatchV1().Jobs("ml-training").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Error("expected job in namespace ml-training, found none")
	}

	watcher.Modify(succeededJob("orion-550e8400", "ml-training"))
	<-done
}

func TestExecute_EmptySpecNamespaceFallsBackToDefault(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	e := k8s.NewKubernetesExecutor(fakeClient, k8s.ExecutorConfig{
		DefaultNamespace: "my-default-ns",
		PollInterval:     50 * time.Millisecond,
	}, testLogger())

	watcher := watch.NewFake()
	fakeClient.PrependWatchReactor("jobs", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})

	// Job spec has no namespace
	job := makeK8sJob(func(j *domain.Job) {
		j.Payload.KubernetesSpec.Namespace = ""
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Execute(context.Background(), job)
	}()

	time.Sleep(30 * time.Millisecond)

	jobs, _ := fakeClient.BatchV1().Jobs("my-default-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) == 0 {
		t.Error("expected job in my-default-ns when spec.Namespace is empty")
	}

	watcher.Modify(succeededJob("orion-550e8400", "my-default-ns"))
	<-done
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func envNames(envs []corev1.EnvVar) []string {
	names := make([]string, len(envs))
	for i, e := range envs {
		names[i] = e.Name
	}
	return names
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
