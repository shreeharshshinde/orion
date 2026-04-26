package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake pipeline store (extends the existing fakeStore in job_test.go)
// ─────────────────────────────────────────────────────────────────────────────

// pipelineFakeStore wraps fakeStore and adds pipeline methods.
// Using composition rather than embedding to avoid test file coupling.
type pipelineFakeStore struct {
	pipelines    map[uuid.UUID]*domain.Pipeline
	pipelineJobs map[uuid.UUID][]*store.PipelineJobStatus

	// Inject errors for specific test scenarios
	createErr error
	getErr    error
}

func newPipelineFakeStore() *pipelineFakeStore {
	return &pipelineFakeStore{
		pipelines:    make(map[uuid.UUID]*domain.Pipeline),
		pipelineJobs: make(map[uuid.UUID][]*store.PipelineJobStatus),
	}
}

func (f *pipelineFakeStore) CreatePipeline(_ context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	p.ID = uuid.New()
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	f.pipelines[p.ID] = p
	return p, nil
}

func (f *pipelineFakeStore) GetPipeline(_ context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.pipelines[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (f *pipelineFakeStore) ListPipelines(_ context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error) {
	var result []*domain.Pipeline
	for _, p := range f.pipelines {
		if filter.Status != nil && p.Status != *filter.Status {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (f *pipelineFakeStore) ListPipelinesByStatus(_ context.Context, status domain.PipelineStatus, _ int) ([]*domain.Pipeline, error) {
	var result []*domain.Pipeline
	for _, p := range f.pipelines {
		if p.Status == status {
			result = append(result, p)
		}
	}
	return result, nil
}

func (f *pipelineFakeStore) UpdatePipelineStatus(_ context.Context, id uuid.UUID, status domain.PipelineStatus) error {
	p, ok := f.pipelines[id]
	if !ok {
		return store.ErrNotFound
	}
	p.Status = status
	return nil
}

func (f *pipelineFakeStore) AddPipelineJob(_ context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
	f.pipelineJobs[pipelineID] = append(f.pipelineJobs[pipelineID], &store.PipelineJobStatus{
		NodeID:    nodeID,
		JobID:     jobID,
		JobStatus: domain.JobStatusQueued,
	})
	return nil
}

func (f *pipelineFakeStore) GetPipelineJobs(_ context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
	return f.pipelineJobs[pipelineID], nil
}

// Stub out JobStore/ExecutionStore/WorkerStore — pipeline handler never calls these
func (f *pipelineFakeStore) CreateJob(_ context.Context, j *domain.Job) (*domain.Job, error) {
	j.ID = uuid.New()
	return j, nil
}
func (f *pipelineFakeStore) GetJob(_ context.Context, _ uuid.UUID) (*domain.Job, error) {
	return nil, store.ErrNotFound
}
func (f *pipelineFakeStore) GetJobByIdempotencyKey(_ context.Context, _ string) (*domain.Job, error) {
	return nil, store.ErrNotFound
}
func (f *pipelineFakeStore) TransitionJobState(_ context.Context, _ uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
	return nil
}
func (f *pipelineFakeStore) ListJobs(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
	return nil, nil
}
func (f *pipelineFakeStore) ClaimPendingJobs(_ context.Context, _, _ string, _ int) ([]*domain.Job, error) {
	return nil, nil
}
func (f *pipelineFakeStore) MarkJobRunning(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (f *pipelineFakeStore) MarkJobCompleted(_ context.Context, _ uuid.UUID) error { return nil }
func (f *pipelineFakeStore) MarkJobFailed(_ context.Context, _ uuid.UUID, _ string, _ *time.Time) error {
	return nil
}
func (f *pipelineFakeStore) ReclaimOrphanedJobs(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}
func (f *pipelineFakeStore) DeleteJob(_ context.Context, _ uuid.UUID) error { return nil }
func (f *pipelineFakeStore) RecordExecution(_ context.Context, _ *domain.JobExecution) error {
	return nil
}
func (f *pipelineFakeStore) GetExecutions(_ context.Context, _ uuid.UUID) ([]*domain.JobExecution, error) {
	return nil, nil
}
func (f *pipelineFakeStore) RegisterWorker(_ context.Context, _ *domain.Worker) error { return nil }
func (f *pipelineFakeStore) Heartbeat(_ context.Context, _ string) error              { return nil }
func (f *pipelineFakeStore) ListActiveWorkers(_ context.Context, _ time.Duration) ([]*domain.Worker, error) {
	return nil, nil
}
func (f *pipelineFakeStore) DeregisterWorker(_ context.Context, _ string) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Test helper — minimal valid DAG
// ─────────────────────────────────────────────────────────────────────────────

func minimalDAG() domain.DAGSpec {
	return domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "preprocess", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
			{ID: "train", JobTemplate: domain.JobPayload{HandlerName: "noop"}},
		},
		Edges: []domain.DAGEdge{
			{Source: "preprocess", Target: "train"},
		},
	}
}

func postPipeline(t *testing.T, h *handler.PipelineHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/pipelines", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreatePipeline(rr, req)
	return rr
}

func getPipelineReq(t *testing.T, h *handler.PipelineHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/pipelines/"+id, nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.GetPipeline(rr, req)
	return rr
}

func getPipelineJobsReq(t *testing.T, h *handler.PipelineHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/pipelines/"+id+"/jobs", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.GetPipelineJobs(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /pipelines — CreatePipeline
// ─────────────────────────────────────────────────────────────────────────────

func TestCreatePipeline_ValidRequest_Returns201(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	rr := postPipeline(t, h, map[string]any{
		"name":     "my-pipeline",
		"dag_spec": minimalDAG(),
	})

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["id"] == nil {
		t.Error("response should include 'id'")
	}
	if resp["status"] != "pending" {
		t.Errorf("new pipeline should have status=pending, got %v", resp["status"])
	}
}

func TestCreatePipeline_MissingName_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"dag_spec": minimalDAG(),
		// name is missing
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestCreatePipeline_BlankName_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name":     "   ",
		"dag_spec": minimalDAG(),
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for blank name, got %d", rr.Code)
	}
}

func TestCreatePipeline_EmptyNodes_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name":     "test",
		"dag_spec": map[string]any{"nodes": []any{}, "edges": []any{}},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for empty nodes, got %d", rr.Code)
	}
}

func TestCreatePipeline_DuplicateNodeID_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name": "test",
		"dag_spec": map[string]any{
			"nodes": []map[string]any{
				{"id": "a", "job_template": map[string]any{"handler_name": "noop"}},
				{"id": "a", "job_template": map[string]any{"handler_name": "noop"}}, // duplicate
			},
			"edges": []any{},
		},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for duplicate node id, got %d", rr.Code)
	}
}

func TestCreatePipeline_NodeMissingHandlerAndK8sSpec_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name": "test",
		"dag_spec": map[string]any{
			"nodes": []map[string]any{
				{"id": "orphan", "job_template": map[string]any{}}, // no handler_name, no k8s spec
			},
			"edges": []any{},
		},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for node without execution backend, got %d", rr.Code)
	}
}

func TestCreatePipeline_EdgeReferencesUnknownNode_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name": "test",
		"dag_spec": map[string]any{
			"nodes": []map[string]any{
				{"id": "a", "job_template": map[string]any{"handler_name": "noop"}},
			},
			"edges": []map[string]any{
				{"source": "a", "target": "nonexistent"}, // nonexistent not in nodes
			},
		},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for edge referencing unknown node, got %d", rr.Code)
	}
}

func TestCreatePipeline_SelfLoop_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name": "test",
		"dag_spec": map[string]any{
			"nodes": []map[string]any{
				{"id": "a", "job_template": map[string]any{"handler_name": "noop"}},
			},
			"edges": []map[string]any{
				{"source": "a", "target": "a"}, // self-loop
			},
		},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for self-loop, got %d", rr.Code)
	}
}

func TestCreatePipeline_InvalidJSON_Returns400(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	req := httptest.NewRequest(http.MethodPost, "/pipelines", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreatePipeline(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rr.Code)
	}
}

func TestCreatePipeline_StoreError_Returns500(t *testing.T) {
	fs := newPipelineFakeStore()
	fs.createErr = store.ErrStateConflict // simulate unexpected store failure
	h := handler.NewPipelineHandler(fs, testLogger())

	rr := postPipeline(t, h, map[string]any{
		"name":     "test",
		"dag_spec": minimalDAG(),
	})
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on store error, got %d", rr.Code)
	}
}

func TestCreatePipeline_K8sNodeSpec_Returns201(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := postPipeline(t, h, map[string]any{
		"name": "gpu-pipeline",
		"dag_spec": map[string]any{
			"nodes": []map[string]any{
				{
					"id": "train-gpu",
					"job_template": map[string]any{
						"kubernetes_spec": map[string]any{
							"image":   "pytorch/pytorch:2.0",
							"command": []string{"python", "train.py"},
							"resources": map[string]any{
								"cpu": "4", "memory": "16Gi", "gpu": 1,
							},
						},
					},
				},
			},
			"edges": []any{},
		},
	})
	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201 for k8s node, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id} — GetPipeline
// ─────────────────────────────────────────────────────────────────────────────

func TestGetPipeline_Found_Returns200(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	// Create via POST so it gets an ID
	rr := postPipeline(t, h, map[string]any{
		"name":     "my-pipeline",
		"dag_spec": minimalDAG(),
	})
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr2 := getPipelineReq(t, h, id)
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr2.Body.Bytes(), &resp)
	if resp["id"] != id {
		t.Errorf("expected id=%s in response, got %v", id, resp["id"])
	}
}

func TestGetPipeline_NotFound_Returns404(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := getPipelineReq(t, h, uuid.New().String())
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetPipeline_InvalidUUID_Returns400(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := getPipelineReq(t, h, "not-a-uuid")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines — ListPipelines
// ─────────────────────────────────────────────────────────────────────────────

func TestListPipelines_ReturnsAll_Returns200(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	postPipeline(t, h, map[string]any{"name": "p1", "dag_spec": minimalDAG()})
	postPipeline(t, h, map[string]any{"name": "p2", "dag_spec": minimalDAG()})

	req := httptest.NewRequest(http.MethodGet, "/pipelines", nil)
	rr := httptest.NewRecorder()
	h.ListPipelines(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 2 {
		t.Errorf("expected count=2, got %v", resp["count"])
	}
}

func TestListPipelines_StatusFilter_Returns200(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	postPipeline(t, h, map[string]any{"name": "pending-1", "dag_spec": minimalDAG()})
	postPipeline(t, h, map[string]any{"name": "pending-2", "dag_spec": minimalDAG()})

	req := httptest.NewRequest(http.MethodGet, "/pipelines?status=pending", nil)
	rr := httptest.NewRecorder()
	h.ListPipelines(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 2 {
		t.Errorf("expected count=2 for pending pipelines, got %v", resp["count"])
	}
}

func TestListPipelines_InvalidStatus_Returns422(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/pipelines?status=bogus", nil)
	rr := httptest.NewRecorder()
	h.ListPipelines(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid status param, got %d", rr.Code)
	}
}

func TestListPipelines_Empty_Returns200WithZeroCount(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/pipelines", nil)
	rr := httptest.NewRecorder()
	h.ListPipelines(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for empty list, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id}/jobs — GetPipelineJobs
// ─────────────────────────────────────────────────────────────────────────────

func TestGetPipelineJobs_EmptyJustCreated_Returns200(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	rr := postPipeline(t, h, map[string]any{"name": "test", "dag_spec": minimalDAG()})
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)

	rr2 := getPipelineJobsReq(t, h, id)
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr2.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 0 {
		t.Errorf("newly created pipeline should have 0 jobs, got %v", resp["count"])
	}
}

func TestGetPipelineJobs_WithJobs_Returns200(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	rr := postPipeline(t, h, map[string]any{"name": "test", "dag_spec": minimalDAG()})
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	pipelineID := uuid.MustParse(created["id"].(string))

	// Manually add pipeline jobs to the fake store
	_ = fs.AddPipelineJob(context.Background(), pipelineID, "preprocess", uuid.New())
	_ = fs.AddPipelineJob(context.Background(), pipelineID, "train", uuid.New())

	rr2 := getPipelineJobsReq(t, h, pipelineID.String())
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr2.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rr2.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 2 {
		t.Errorf("expected count=2, got %v", resp["count"])
	}
}

func TestGetPipelineJobs_UnknownPipeline_Returns404(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := getPipelineJobsReq(t, h, uuid.New().String())
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetPipelineJobs_InvalidUUID_Returns400(t *testing.T) {
	h := handler.NewPipelineHandler(newPipelineFakeStore(), testLogger())
	rr := getPipelineJobsReq(t, h, "bad-id")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Content-Type header
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPipelineRoutes_SetJSONContentType(t *testing.T) {
	fs := newPipelineFakeStore()
	h := handler.NewPipelineHandler(fs, testLogger())

	rr := postPipeline(t, h, map[string]any{"name": "ct-test", "dag_spec": minimalDAG()})
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("CreatePipeline Content-Type = %q, want application/json", ct)
	}
}
