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

// JobHandler handles HTTP requests for job CRUD operations.
// It does NOT own business logic — that lives in a service layer.
type JobHandler struct {
	store  store.Store
	logger *slog.Logger
}

func NewJobHandler(s store.Store, logger *slog.Logger) *JobHandler {
	return &JobHandler{store: s, logger: logger}
}

// SubmitJobRequest is the inbound request body for POST /jobs.
type SubmitJobRequest struct {
	Name           string             `json:"name"`
	Type           domain.JobType     `json:"type"`
	QueueName      string             `json:"queue_name"`
	Priority       domain.JobPriority `json:"priority"`
	Payload        domain.JobPayload  `json:"payload"`
	MaxRetries     int                `json:"max_retries"`
	ScheduledAt    *time.Time         `json:"scheduled_at,omitempty"`
	Deadline       *time.Time         `json:"deadline,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

// SubmitJob handles POST /jobs
// Idempotency: if idempotency_key is provided and matches an existing job,
// returns 200 with the existing job instead of creating a duplicate.
func (h *JobHandler) SubmitJob(w http.ResponseWriter, r *http.Request) {
	var req SubmitJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := validateSubmitRequest(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Check idempotency key before creating
	if req.IdempotencyKey != "" {
		existing, err := h.store.GetJobByIdempotencyKey(r.Context(), req.IdempotencyKey)
		if err == nil && existing != nil {
			// Idempotent response: return the existing job with 200
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}

	priority := req.Priority
	if priority == 0 {
		priority = domain.PriorityNormal
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
			// Race condition: another request created with same idempotency key
			existing, _ := h.store.GetJobByIdempotencyKey(r.Context(), req.IdempotencyKey)
			writeJSON(w, http.StatusOK, existing)
			return
		}
		h.logger.Error("failed to create job", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// GetJob handles GET /jobs/{id}
func (h *JobHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	job, err := h.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// ListJobs handles GET /jobs
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
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":  jobs,
		"count": len(jobs),
	})
}

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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}