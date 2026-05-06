package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake store — implements store.Store, returns controlled values
// ─────────────────────────────────────────────────────────────────────────────

type fakeStore struct {
	createJobFn           func(ctx context.Context, job *domain.Job) (*domain.Job, error)
	getJobFn              func(ctx context.Context, id uuid.UUID) (*domain.Job, error)
	getJobByIdempotencyFn func(ctx context.Context, key string) (*domain.Job, error)
	listJobsFn            func(ctx context.Context, filter store.JobFilter) ([]*domain.Job, error)
	getExecutionsFn       func(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error)
}

func (f *fakeStore) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
	if f.createJobFn != nil {
		return f.createJobFn(ctx, job)
	}
	job.ID = uuid.New()
	now := time.Now()
	job.CreatedAt = now
	job.UpdatedAt = now
	job.Status = domain.JobStatusQueued
	return job, nil
}
func (f *fakeStore) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	if f.getJobFn != nil {
		return f.getJobFn(ctx, id)
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error) {
	if f.getJobByIdempotencyFn != nil {
		return f.getJobByIdempotencyFn(ctx, key)
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListJobs(ctx context.Context, filter store.JobFilter) ([]*domain.Job, error) {
	if f.listJobsFn != nil {
		return f.listJobsFn(ctx, filter)
	}
	return []*domain.Job{}, nil
}
func (f *fakeStore) GetExecutions(ctx context.Context, jobID uuid.UUID) ([]*domain.JobExecution, error) {
	if f.getExecutionsFn != nil {
		return f.getExecutionsFn(ctx, jobID)
	}
	return []*domain.JobExecution{}, nil
}

// Store interface stubs — unused in handler tests
func (f *fakeStore) TransitionJobState(ctx context.Context, id uuid.UUID, exp, new domain.JobStatus, opts ...store.TransitionOption) error {
	return nil
}
func (f *fakeStore) ClaimPendingJobs(ctx context.Context, q, w string, limit int) ([]*domain.Job, error) {
	return nil, nil
}
func (f *fakeStore) MarkJobRunning(ctx context.Context, id uuid.UUID, w string) error { return nil }
func (f *fakeStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error         { return nil }
func (f *fakeStore) MarkJobFailed(ctx context.Context, id uuid.UUID, msg string, t *time.Time) error {
	return nil
}
func (f *fakeStore) ReclaimOrphanedJobs(ctx context.Context, d time.Duration) (int, error) {
	return 0, nil
}
func (f *fakeStore) DeleteJob(ctx context.Context, id uuid.UUID) error { return nil }
func (f *fakeStore) RecordExecution(ctx context.Context, exec *domain.JobExecution) error {
	return nil
}
func (f *fakeStore) RegisterWorker(ctx context.Context, w *domain.Worker) error { return nil }
func (f *fakeStore) Heartbeat(ctx context.Context, id string) error             { return nil }
func (f *fakeStore) ListActiveWorkers(ctx context.Context, ttl time.Duration) ([]*domain.Worker, error) {
	return nil, nil
}
func (f *fakeStore) DeregisterWorker(ctx context.Context, id string) error { return nil }

// PipelineStore interface stubs
func (f *fakeStore) CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) ListPipelines(ctx context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error {
	return nil
}
func (f *fakeStore) AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
	return nil
}
func (f *fakeStore) GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
	return nil, nil
}

// QueueConfigStore stubs — Phase 8 (not exercised by handler tests)
func (f *fakeStore) ListQueueConfigs(_ context.Context) ([]*store.QueueConfig, error) {
	return nil, nil
}
func (f *fakeStore) GetQueueConfig(_ context.Context, _ string) (*store.QueueConfig, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) UpsertQueueConfig(_ context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
	return cfg, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func postJob(t *testing.T, h *handler.JobHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/jobs", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.SubmitJob(rr, req)
	return rr
}

func getJob(t *testing.T, h *handler.JobHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.GetJob(rr, req)
	return rr
}

func getExecutions(t *testing.T, h *handler.JobHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/jobs/"+id+"/executions", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.GetExecutions(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /jobs — SubmitJob
// ─────────────────────────────────────────────────────────────────────────────

func TestSubmitJob_ValidInlineJob_Returns201(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":    "test-job",
		"type":    "inline",
		"payload": map[string]any{"handler_name": "noop"},
	})

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["id"] == nil {
		t.Error("response should contain 'id'")
	}
}

func TestSubmitJob_ValidK8sJob_Returns201(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name": "gpu-training",
		"type": "k8s_job",
		"payload": map[string]any{
			"kubernetes_spec": map[string]any{
				"image":   "pytorch/pytorch:2.0",
				"command": []string{"python", "train.py"},
				"resources": map[string]any{
					"cpu": "4", "memory": "16Gi",
				},
			},
		},
	})

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSubmitJob_MissingName_Returns422(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"type":    "inline",
		"payload": map[string]any{"handler_name": "noop"},
		// name is missing
	})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestSubmitJob_InvalidType_Returns422(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":    "test",
		"type":    "invalid_type",
		"payload": map[string]any{"handler_name": "noop"},
	})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestSubmitJob_InlineMissingHandlerName_Returns422(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":    "test",
		"type":    "inline",
		"payload": map[string]any{}, // missing handler_name
	})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestSubmitJob_K8sMissingSpec_Returns422(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":    "test",
		"type":    "k8s_job",
		"payload": map[string]any{}, // missing kubernetes_spec
	})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rr.Code)
	}
}

func TestSubmitJob_InvalidJSON_Returns400(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/jobs", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.SubmitJob(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestSubmitJob_IdempotencyKey_ExistingReturns200(t *testing.T) {
	existingJob := &domain.Job{
		ID:     uuid.New(),
		Name:   "existing",
		Status: domain.JobStatusCompleted,
	}

	h := handler.NewJobHandler(&fakeStore{
		getJobByIdempotencyFn: func(_ context.Context, key string) (*domain.Job, error) {
			if key == "my-key-001" {
				return existingJob, nil
			}
			return nil, store.ErrNotFound
		},
	}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":            "new-attempt",
		"type":            "inline",
		"payload":         map[string]any{"handler_name": "noop"},
		"idempotency_key": "my-key-001",
	})

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for idempotent retry, got %d", rr.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["id"] != existingJob.ID.String() {
		t.Errorf("expected existing job ID %s, got %v", existingJob.ID, resp["id"])
	}
}

func TestSubmitJob_StoreError_Returns500(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{
		createJobFn: func(_ context.Context, _ *domain.Job) (*domain.Job, error) {
			return nil, errors.New("database connection lost")
		},
	}, testLogger())

	rr := postJob(t, h, map[string]any{
		"name":    "test",
		"type":    "inline",
		"payload": map[string]any{"handler_name": "noop"},
	})

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on store error, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /jobs/{id} — GetJob
// ─────────────────────────────────────────────────────────────────────────────

func TestGetJob_Found_Returns200(t *testing.T) {
	jobID := uuid.New()
	h := handler.NewJobHandler(&fakeStore{
		getJobFn: func(_ context.Context, id uuid.UUID) (*domain.Job, error) {
			if id == jobID {
				return &domain.Job{ID: jobID, Name: "my-job", Status: domain.JobStatusRunning}, nil
			}
			return nil, store.ErrNotFound
		},
	}, testLogger())

	rr := getJob(t, h, jobID.String())

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "running" {
		t.Errorf("expected status=running, got %v", resp["status"])
	}
}

func TestGetJob_NotFound_Returns404(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	rr := getJob(t, h, uuid.New().String())

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetJob_InvalidUUID_Returns400(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	rr := getJob(t, h, "not-a-uuid")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /jobs — ListJobs
// ─────────────────────────────────────────────────────────────────────────────

func TestListJobs_ReturnsAll(t *testing.T) {
	jobs := []*domain.Job{
		{ID: uuid.New(), Name: "job-1", Status: domain.JobStatusQueued},
		{ID: uuid.New(), Name: "job-2", Status: domain.JobStatusRunning},
	}
	h := handler.NewJobHandler(&fakeStore{
		listJobsFn: func(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
			return jobs, nil
		},
	}, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rr := httptest.NewRecorder()
	h.ListJobs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 2 {
		t.Errorf("expected count=2, got %v", resp["count"])
	}
}

func TestListJobs_EmptyList_Returns200(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rr := httptest.NewRecorder()
	h.ListJobs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for empty list, got %d", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /jobs/{id}/executions — GetExecutions
// ─────────────────────────────────────────────────────────────────────────────

func TestGetExecutions_Found_Returns200(t *testing.T) {
	jobID := uuid.New()
	now := time.Now()
	execs := []*domain.JobExecution{
		{ID: uuid.New(), JobID: jobID, Attempt: 1, Status: domain.JobStatusCompleted, StartedAt: &now},
	}

	h := handler.NewJobHandler(&fakeStore{
		getJobFn: func(_ context.Context, id uuid.UUID) (*domain.Job, error) {
			return &domain.Job{ID: jobID}, nil
		},
		getExecutionsFn: func(_ context.Context, jid uuid.UUID) ([]*domain.JobExecution, error) {
			return execs, nil
		},
	}, testLogger())

	rr := getExecutions(t, h, jobID.String())

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 1 {
		t.Errorf("expected count=1, got %v", resp["count"])
	}
}

func TestGetExecutions_JobNotFound_Returns404(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	rr := getExecutions(t, h, uuid.New().String())

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown job, got %d", rr.Code)
	}
}

func TestGetExecutions_InvalidUUID_Returns400(t *testing.T) {
	h := handler.NewJobHandler(&fakeStore{}, testLogger())
	rr := getExecutions(t, h, "bad-uuid")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid UUID, got %d", rr.Code)
	}
}

func TestGetExecutions_EmptyHistory_Returns200(t *testing.T) {
	jobID := uuid.New()
	h := handler.NewJobHandler(&fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) {
			return &domain.Job{ID: jobID}, nil
		},
		getExecutionsFn: func(_ context.Context, _ uuid.UUID) ([]*domain.JobExecution, error) {
			return []*domain.JobExecution{}, nil
		},
	}, testLogger())

	rr := getExecutions(t, h, jobID.String())

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for empty executions, got %d", rr.Code)
	}

	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 0 {
		t.Errorf("expected count=0, got %v", resp["count"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Content-Type header
// ─────────────────────────────────────────────────────────────────────────────

func TestAllRoutes_SetJSONContentType(t *testing.T) {
	jobID := uuid.New()
	h := handler.NewJobHandler(&fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) {
			return &domain.Job{ID: jobID}, nil
		},
	}, testLogger())

	rr := getJob(t, h, jobID.String())
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}
