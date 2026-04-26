package pipeline_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/pipeline"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake store
// ─────────────────────────────────────────────────────────────────────────────

// fakeStore implements store.Store with in-memory state.
// Only the PipelineStore and minimal JobStore methods are implemented;
// others panic if called (test will fail loudly instead of silently).
type fakeStore struct {
	mu sync.Mutex

	// Pipeline state
	pipelines    map[uuid.UUID]*domain.Pipeline
	pipelineJobs map[uuid.UUID][]*store.PipelineJobStatus // pipeline_id → rows

	// Job creation tracking
	createdJobs  []*domain.Job
	jobIDCounter int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		pipelines:    make(map[uuid.UUID]*domain.Pipeline),
		pipelineJobs: make(map[uuid.UUID][]*store.PipelineJobStatus),
	}
}

// ── PipelineStore ─────────────────────────────────────────────────────────────

func (f *fakeStore) CreatePipeline(_ context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	f.pipelines[p.ID] = p
	return p, nil
}

func (f *fakeStore) GetPipeline(_ context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pipelines[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListPipelines(_ context.Context, _ store.PipelineFilter) ([]*domain.Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []*domain.Pipeline
	for _, p := range f.pipelines {
		result = append(result, p)
	}
	return result, nil
}

func (f *fakeStore) ListPipelinesByStatus(_ context.Context, status domain.PipelineStatus, _ int) ([]*domain.Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []*domain.Pipeline
	for _, p := range f.pipelines {
		if p.Status == status {
			result = append(result, p)
		}
	}
	return result, nil
}

func (f *fakeStore) UpdatePipelineStatus(_ context.Context, id uuid.UUID, status domain.PipelineStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pipelines[id]
	if !ok {
		return store.ErrNotFound
	}
	p.Status = status
	if status == domain.PipelineStatusCompleted || status == domain.PipelineStatusFailed {
		now := time.Now()
		p.CompletedAt = &now
	}
	return nil
}

func (f *fakeStore) AddPipelineJob(_ context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// ON CONFLICT DO NOTHING semantics
	for _, pj := range f.pipelineJobs[pipelineID] {
		if pj.NodeID == nodeID {
			return nil // already exists, idempotent skip
		}
	}

	pjs := pipeline.MakePipelineJobStatus(nodeID, jobID, domain.JobStatusQueued)
	f.pipelineJobs[pipelineID] = append(f.pipelineJobs[pipelineID], pjs)
	return nil
}

func (f *fakeStore) GetPipelineJobs(_ context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pipelineJobs[pipelineID], nil
}

// ── Minimal JobStore ───────────────────────────────────────────────────────────

func (f *fakeStore) CreateJob(_ context.Context, job *domain.Job) (*domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job.ID = uuid.New()
	job.Status = domain.JobStatusQueued
	f.createdJobs = append(f.createdJobs, job)
	return job, nil
}

// ── Unimplemented stubs — panic so tests fail loudly ─────────────────────────

func (f *fakeStore) GetJob(_ context.Context, _ uuid.UUID) (*domain.Job, error) {
	panic("not implemented")
}
func (f *fakeStore) GetJobByIdempotencyKey(_ context.Context, _ string) (*domain.Job, error) {
	panic("not implemented")
}
func (f *fakeStore) TransitionJobState(_ context.Context, _ uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
	panic("not implemented")
}
func (f *fakeStore) ListJobs(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
	panic("not implemented")
}
func (f *fakeStore) ClaimPendingJobs(_ context.Context, _, _ string, _ int) ([]*domain.Job, error) {
	panic("not implemented")
}
func (f *fakeStore) MarkJobRunning(_ context.Context, _ uuid.UUID, _ string) error {
	panic("not implemented")
}
func (f *fakeStore) MarkJobCompleted(_ context.Context, _ uuid.UUID) error {
	panic("not implemented")
}
func (f *fakeStore) MarkJobFailed(_ context.Context, _ uuid.UUID, _ string, _ *time.Time) error {
	panic("not implemented")
}
func (f *fakeStore) ReclaimOrphanedJobs(_ context.Context, _ time.Duration) (int, error) {
	panic("not implemented")
}
func (f *fakeStore) DeleteJob(_ context.Context, _ uuid.UUID) error { panic("not implemented") }
func (f *fakeStore) RecordExecution(_ context.Context, _ *domain.JobExecution) error {
	panic("not implemented")
}
func (f *fakeStore) GetExecutions(_ context.Context, _ uuid.UUID) ([]*domain.JobExecution, error) {
	panic("not implemented")
}
func (f *fakeStore) RegisterWorker(_ context.Context, _ *domain.Worker) error {
	panic("not implemented")
}
func (f *fakeStore) Heartbeat(_ context.Context, _ string) error { panic("not implemented") }
func (f *fakeStore) ListActiveWorkers(_ context.Context, _ time.Duration) ([]*domain.Worker, error) {
	panic("not implemented")
}
func (f *fakeStore) DeregisterWorker(_ context.Context, _ string) error { panic("not implemented") }

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// seedPipeline adds a pipeline to the fake store and returns it.
func seedPipeline(fs *fakeStore, name string, status domain.PipelineStatus, dag domain.DAGSpec) *domain.Pipeline {
	p := &domain.Pipeline{
		ID:        uuid.New(),
		Name:      name,
		Status:    status,
		DAGSpec:   dag,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	fs.pipelines[p.ID] = p
	return p
}

// markNodeCompleted simulates a node's job having completed.
func markNodeCompleted(fs *fakeStore, pipelineID uuid.UUID, nodeID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, pj := range fs.pipelineJobs[pipelineID] {
		if pj.NodeID == nodeID {
			pj.JobStatus = domain.JobStatusCompleted
			return
		}
	}
}

// markNodeDead simulates a node's job having exhausted all retries.
func markNodeDead(fs *fakeStore, pipelineID uuid.UUID, nodeID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	// Find existing or add a dead entry
	for _, pj := range fs.pipelineJobs[pipelineID] {
		if pj.NodeID == nodeID {
			pj.JobStatus = domain.JobStatusDead
			return
		}
	}
	// Node has no job yet — add a dead entry to simulate failure
	pj := pipeline.MakePipelineJobStatus(nodeID, uuid.New(), domain.JobStatusDead)
	fs.pipelineJobs[pipelineID] = append(fs.pipelineJobs[pipelineID], pj)
}

func countCreatedJobs(fs *fakeStore) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.createdJobs)
}

func getPipelineStatus(fs *fakeStore, id uuid.UUID) domain.PipelineStatus {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.pipelines[id].Status
}

func getPipelineJobCount(fs *fakeStore, id uuid.UUID) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.pipelineJobs[id])
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests — AdvanceAll and advanceOne
// ─────────────────────────────────────────────────────────────────────────────

func TestAdvanceAll_NoActivePipelines_DoesNothing(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	if err := adv.AdvanceAll(context.Background()); err != nil {
		t.Errorf("AdvanceAll with no pipelines should return nil, got: %v", err)
	}
	if countCreatedJobs(fs) != 0 {
		t.Error("no jobs should be created when no pipelines exist")
	}
}

func TestAdvanceAll_PendingPipeline_CreatesRootJobs(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "preprocess", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "train", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: []domain.DAGEdge{
			{Source: "preprocess", Target: "train"},
		},
	}
	p := seedPipeline(fs, "test-pipeline", domain.PipelineStatusPending, dag)

	if err := adv.AdvanceAll(context.Background()); err != nil {
		t.Fatalf("AdvanceAll error: %v", err)
	}

	// Only root node (preprocess) should have a job created
	if countCreatedJobs(fs) != 1 {
		t.Errorf("expected 1 job created (root node only), got %d", countCreatedJobs(fs))
	}

	// Pipeline should now be running
	if status := getPipelineStatus(fs, p.ID); status != domain.PipelineStatusRunning {
		t.Errorf("expected pipeline status=running, got %q", status)
	}
}

func TestAdvanceAll_LinearPipeline_CompletesInSteps(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "a", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "b", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "c", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: []domain.DAGEdge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "c"},
		},
	}
	p := seedPipeline(fs, "linear-pipeline", domain.PipelineStatusPending, dag)

	// Tick 1: pending → running, creates job for "a" only
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 1 {
		t.Fatalf("tick 1: expected 1 job (node a), got %d", countCreatedJobs(fs))
	}
	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusRunning {
		t.Fatal("tick 1: pipeline should be running")
	}

	// Simulate "a" completing
	markNodeCompleted(fs, p.ID, "a")

	// Tick 2: creates job for "b", "a" is in completed set
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 2 {
		t.Fatalf("tick 2: expected 2 total jobs (a+b), got %d", countCreatedJobs(fs))
	}
	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusRunning {
		t.Fatal("tick 2: pipeline should still be running")
	}

	// Simulate "b" completing
	markNodeCompleted(fs, p.ID, "b")

	// Tick 3: creates job for "c"
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 3 {
		t.Fatalf("tick 3: expected 3 total jobs (a+b+c), got %d", countCreatedJobs(fs))
	}

	// Simulate "c" completing
	markNodeCompleted(fs, p.ID, "c")

	// Tick 4: all nodes completed → pipeline completed
	_ = adv.AdvanceAll(context.Background())
	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusCompleted {
		t.Errorf("tick 4: pipeline should be completed, got %q", getPipelineStatus(fs, p.ID))
	}
}

func TestAdvanceAll_DiamondDAG_FanOutThenFanIn(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "preprocess", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "train_a", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "train_b", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "merge", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: []domain.DAGEdge{
			{Source: "preprocess", Target: "train_a"},
			{Source: "preprocess", Target: "train_b"},
			{Source: "train_a", Target: "merge"},
			{Source: "train_b", Target: "merge"},
		},
	}
	p := seedPipeline(fs, "diamond-pipeline", domain.PipelineStatusPending, dag)

	// Tick 1: creates preprocess job only
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 1 {
		t.Fatalf("tick 1: expected 1 job (preprocess), got %d", countCreatedJobs(fs))
	}

	markNodeCompleted(fs, p.ID, "preprocess")

	// Tick 2: creates BOTH train_a and train_b (fan-out)
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 3 {
		t.Fatalf("tick 2: expected 3 total jobs (preprocess+train_a+train_b), got %d", countCreatedJobs(fs))
	}

	// Only train_a completes — merge must NOT start yet (train_b still running)
	markNodeCompleted(fs, p.ID, "train_a")
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 3 {
		t.Errorf("merge should NOT start with only train_a done; got %d jobs", countCreatedJobs(fs))
	}
	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusRunning {
		t.Error("pipeline should still be running while train_b is incomplete")
	}

	// train_b completes — NOW merge can start (fan-in gate satisfied)
	markNodeCompleted(fs, p.ID, "train_b")
	_ = adv.AdvanceAll(context.Background())
	if countCreatedJobs(fs) != 4 {
		t.Fatalf("tick 4: expected 4 total jobs (preprocess+train_a+train_b+merge), got %d", countCreatedJobs(fs))
	}

	markNodeCompleted(fs, p.ID, "merge")
	_ = adv.AdvanceAll(context.Background())
	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusCompleted {
		t.Errorf("all nodes done — pipeline should be completed, got %q", getPipelineStatus(fs, p.ID))
	}
}

func TestAdvanceAll_FailedNode_PipelineMarkedFailed(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "preprocess", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "train", JobTemplate: domain.JobPayload{HandlerName: "always_fail"}},
			{ID: "evaluate", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: []domain.DAGEdge{
			{Source: "preprocess", Target: "train"},
			{Source: "train", Target: "evaluate"},
		},
	}
	p := seedPipeline(fs, "failure-pipeline", domain.PipelineStatusRunning, dag)

	// Seed: preprocess completed, train is dead
	markNodeCompleted(fs, p.ID, "preprocess")
	markNodeDead(fs, p.ID, "train")

	_ = adv.AdvanceAll(context.Background())

	if getPipelineStatus(fs, p.ID) != domain.PipelineStatusFailed {
		t.Errorf("pipeline with dead node should be failed, got %q", getPipelineStatus(fs, p.ID))
	}

	// evaluate should never have been created (train blocked it)
	if countCreatedJobs(fs) != 0 {
		t.Errorf("evaluate should never be created after train fails, got %d jobs", countCreatedJobs(fs))
	}
}

func TestAdvanceAll_Idempotent_DoubleTickNoExtraJobs(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "root", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: nil,
	}
	seedPipeline(fs, "idempotent-pipeline", domain.PipelineStatusPending, dag)

	// Two consecutive ticks before anything completes
	_ = adv.AdvanceAll(context.Background())
	_ = adv.AdvanceAll(context.Background())

	if countCreatedJobs(fs) != 1 {
		t.Errorf("double tick should create exactly 1 job (idempotency), got %d", countCreatedJobs(fs))
	}
}

func TestAdvanceAll_MultiplePipelines_AllAdvanced(t *testing.T) {
	fs := newFakeStore()
	adv := pipeline.NewAdvancer(fs, testLogger())

	dag1 := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "n1", JobTemplate: domain.JobPayload{HandlerName: "noop"}}},
	}
	dag2 := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "n2", JobTemplate: domain.JobPayload{HandlerName: "noop"}}},
	}
	seedPipeline(fs, "pipeline-1", domain.PipelineStatusPending, dag1)
	seedPipeline(fs, "pipeline-2", domain.PipelineStatusPending, dag2)

	_ = adv.AdvanceAll(context.Background())

	// Each pipeline has one root node → 2 jobs total
	if countCreatedJobs(fs) != 2 {
		t.Errorf("expected 2 jobs (one per pipeline), got %d", countCreatedJobs(fs))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests — graph helpers (white-box)
// ─────────────────────────────────────────────────────────────────────────────

func TestFindDownstream_LinearChain(t *testing.T) {
	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.DAGEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}},
	}
	down := pipeline.FindDownstream(dag, "a")
	if len(down) != 2 {
		t.Errorf("expected 2 downstream nodes from a, got %d: %v", len(down), down)
	}
}

func TestFindDownstream_DiamondFromRoot(t *testing.T) {
	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		Edges: []domain.DAGEdge{
			{Source: "a", Target: "b"},
			{Source: "a", Target: "c"},
			{Source: "b", Target: "d"},
			{Source: "c", Target: "d"},
		},
	}
	down := pipeline.FindDownstream(dag, "a")
	if len(down) != 3 {
		t.Errorf("expected 3 downstream nodes from a, got %d: %v", len(down), down)
	}
}

func TestFindDownstream_LeafNode_Empty(t *testing.T) {
	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}},
		Edges: []domain.DAGEdge{{Source: "a", Target: "b"}},
	}
	down := pipeline.FindDownstream(dag, "b") // leaf has no children
	if len(down) != 0 {
		t.Errorf("leaf node should have no downstream, got %v", down)
	}
}

func TestAllNodesCompleted_AllInSet(t *testing.T) {
	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}},
	}
	completed := map[string]bool{"a": true, "b": true}
	if !pipeline.AllNodesCompleted(dag, completed) {
		t.Error("all nodes completed but AllNodesCompleted returned false")
	}
}

func TestAllNodesCompleted_OneOutstanding(t *testing.T) {
	dag := domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}},
	}
	completed := map[string]bool{"a": true}
	if pipeline.AllNodesCompleted(dag, completed) {
		t.Error("one node outstanding but AllNodesCompleted returned true")
	}
}

func TestAllNodesCompleted_EmptyDAG_ReturnsFalse(t *testing.T) {
	dag := domain.DAGSpec{}
	if pipeline.AllNodesCompleted(dag, map[string]bool{}) {
		t.Error("empty DAG should never return true (guard against malformed spec)")
	}
}

func TestJobTypeFromPayload_InlineWhenNoK8sSpec(t *testing.T) {
	payload := domain.JobPayload{HandlerName: "noop"}
	if got := pipeline.JobTypeFromPayload(payload); got != domain.JobTypeInline {
		t.Errorf("expected inline, got %q", got)
	}
}

func TestJobTypeFromPayload_K8sWhenSpecPresent(t *testing.T) {
	payload := domain.JobPayload{
		KubernetesSpec: &domain.KubernetesSpec{Image: "pytorch:2.0"},
	}
	if got := pipeline.JobTypeFromPayload(payload); got != domain.JobTypeKubernetes {
		t.Errorf("expected k8s_job, got %q", got)
	}
}
