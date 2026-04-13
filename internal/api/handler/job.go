package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// JobHandler handles all HTTP requests for job operations.
//
// Design principle: the handler is intentionally thin.
// It does exactly three things per request:
//   1. Parse and validate the incoming request
//   2. Call the store (one method call)
//   3. Write the response
//
// All business logic lives in the store, scheduler, and worker — not here.
type JobHandler struct {
	store  store.Store
	logger *slog.Logger
}

// NewJobHandler creates a JobHandler. s must be a fully initialised store.Store.
// In Phase 2, this is postgres.New(db). In tests, pass a fake implementation.
func NewJobHandler(s store.Store, logger *slog.Logger) *JobHandler {
	return &JobHandler{store: s, logger: logger}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response types
// ─────────────────────────────────────────────────────────────────────────────

// SubmitJobRequest is the JSON body expected by POST /jobs.
// All fields are optional except name and type.
type SubmitJobRequest struct {
	Name           string             `json:"name"`
	Type           domain.JobType     `json:"type"`
	QueueName      string             `json:"queue_name,omitempty"`
	Priority       domain.JobPriority `json:"priority,omitempty"`
	Payload        domain.JobPayload  `json:"payload"`
	MaxRetries     int                `json:"max_retries,omitempty"`
	ScheduledAt    *time.Time         `json:"scheduled_at,omitempty"`
	Deadline       *time.Time         `json:"deadline,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

// SubmitJob handles POST /jobs.
//
// Idempotency:
//   - If idempotency_key is provided and a job with that key exists → return it (HTTP 200)
//   - If idempotency_key is provided and no job exists → create and return (HTTP 201)
//   - If no idempotency_key → always create a new job (HTTP 201)
//
// The distinction between 200 and 201 lets clients detect whether they created
// a new job or received a previously created one.
func (h *JobHandler) SubmitJob(w http.ResponseWriter, r *http.Request) {
	var req SubmitJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if err := validateSubmitRequest(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Fast-path idempotency check before attempting any INSERT.
	// This avoids hitting the DB for the INSERT when we know the key already exists.
	if req.IdempotencyKey != "" {
		existing, err := h.store.GetJobByIdempotencyKey(r.Context(), req.IdempotencyKey)
		if err == nil && existing != nil {
			// Key found: return the existing job with 200 (not 201).
			writeJSON(w, http.StatusOK, existing)
			return
		}
		// ErrNotFound is expected (key is new) — proceed to create.
		// Any other error: also proceed to create (CreateJob handles idempotency internally too).
	}

	// Apply defaults so callers don't need to specify every field.
	priority := req.Priority
	if priority == 0 {
		priority = domain.PriorityNormal // 5
	}
	queueName := req.QueueName
	if queueName == "" {
		queueName = "default"
	}
	maxRetries := req.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	job := &domain.Job{
		ID:             uuid.New(),
		IdempotencyKey: req.IdempotencyKey,
		Name:           req.Name,
		Type:           req.Type,
		QueueName:      queueName,
		Priority:       priority,
		Status:         domain.JobStatusQueued,
		Payload:        req.Payload,
		MaxRetries:     maxRetries,
		ScheduledAt:    req.ScheduledAt,
		Deadline:       req.Deadline,
	}

	created, err := h.store.CreateJob(r.Context(), job)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			// Race condition: another concurrent request created the job between
			// our idempotency check and our INSERT. Fetch and return that job.
			if req.IdempotencyKey != "" {
				existing, fetchErr := h.store.GetJobByIdempotencyKey(r.Context(), req.IdempotencyKey)
				if fetchErr == nil {
					writeJSON(w, http.StatusOK, existing)
					return
				}
			}
		}
		h.logger.Error("failed to create job", "name", req.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// GetJob handles GET /jobs/{id}.
// Returns the current state of a job including all timing fields.
// Poll this endpoint to track job progress.
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseJobID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID: must be a UUID")
		return
	}

	job, err := h.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		h.logger.Error("failed to get job", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// ListJobs handles GET /jobs.
//
// Query parameters (all optional):
//   - status=queued|scheduled|running|completed|failed|retrying|dead|cancelled
//   - queue=default|high|low  (the logical queue name, not the Redis stream key)
//   - limit=N  (default 50, max 1000)
//   - offset=N (default 0, for pagination)
func (h *JobHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	filter := store.JobFilter{Limit: 50}

	if s := r.URL.Query().Get("status"); s != "" {
		status := domain.JobStatus(s)
		filter.Status = &status
	}
	if q := r.URL.Query().Get("queue"); q != "" {
		filter.QueueName = &q
	}

	jobs, err := h.store.ListJobs(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":  jobs,
		"count": len(jobs),
	})
}

// GetExecutions handles GET /jobs/{id}/executions.
//
// Returns the full execution history for a job — one entry per attempt.
// Each entry records: attempt number, worker, status, start/end times, error, exit code.
//
// A job with 3 attempts has 3 rows:
//   attempt=1, status=failed,    error="OOM killed"
//   attempt=2, status=failed,    error="Timeout"
//   attempt=3, status=completed, error=""
//
// This is the audit trail. Rows are immutable — nothing is ever updated or deleted.
func (h *JobHandler) GetExecutions(w http.ResponseWriter, r *http.Request) {
	id, err := parseJobID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID: must be a UUID")
		return
	}

	// Verify the job exists first, so we return 404 for unknown jobs
	// rather than an empty executions list (which would be ambiguous).
	if _, err := h.store.GetJob(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		h.logger.Error("failed to verify job", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	executions, err := h.store.GetExecutions(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get executions", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":     id,
		"executions": executions,
		"count":      len(executions),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation
// ─────────────────────────────────────────────────────────────────────────────

func validateSubmitRequest(req *SubmitJobRequest) error {
	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.Type == "" {
		return errors.New("type is required")
	}
	if req.Type != domain.JobTypeInline && req.Type != domain.JobTypeKubernetes {
		return errors.New("type must be 'inline' or 'k8s_job'")
	}
	if req.Type == domain.JobTypeKubernetes && req.Payload.KubernetesSpec == nil {
		return errors.New("kubernetes_spec required for k8s_job type")
	}
	if req.Type == domain.JobTypeInline && req.Payload.HandlerName == "" {
		return errors.New("handler_name required for inline type")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseJobID extracts and validates the {id} path parameter.
func parseJobID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue("id"))
}

// writeJSON serialises v to JSON and writes it with the given HTTP status code.
// Sets Content-Type to application/json.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response: {"error": "message"}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}