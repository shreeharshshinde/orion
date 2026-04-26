package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// PipelineHandler
// ─────────────────────────────────────────────────────────────────────────────

// PipelineHandler handles HTTP requests for pipeline CRUD operations.
// Design principle: handlers are thin — parse request, validate, call store,
// write response. All business logic lives in internal/pipeline/advancement.go.
type PipelineHandler struct {
	store  store.Store
	logger *slog.Logger
}

// NewPipelineHandler creates a PipelineHandler backed by the given store.
func NewPipelineHandler(s store.Store, logger *slog.Logger) *PipelineHandler {
	return &PipelineHandler{store: s, logger: logger}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response types
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipelineRequest is the JSON body for POST /pipelines.
//
// Example:
//
//	{
//	  "name": "train-and-evaluate",
//	  "dag_spec": {
//	    "nodes": [
//	      {"id": "preprocess", "job_template": {"handler_name": "preprocess_dataset"}},
//	      {"id": "train",      "job_template": {"handler_name": "train_model"}},
//	      {"id": "evaluate",   "job_template": {"handler_name": "evaluate_model"}}
//	    ],
//	    "edges": [
//	      {"source": "preprocess", "target": "train"},
//	      {"source": "train",      "target": "evaluate"}
//	    ]
//	  }
//	}
type CreatePipelineRequest struct {
	Name    string         `json:"name"`
	DAGSpec domain.DAGSpec `json:"dag_spec"`
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /pipelines — CreatePipeline
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipeline handles POST /pipelines.
//
// Validates the DAG spec (non-empty nodes, valid edge references, no self-loops,
// no duplicate node IDs, each node has a handler or kubernetes_spec), then
// persists the pipeline in pending status.
//
// The scheduler picks up the new pipeline within 2 seconds on its next tick,
// creates jobs for the root nodes, and transitions it to running.
//
// Responses:
//
//	201 Created      → pipeline created successfully (body: domain.Pipeline)
//	400 Bad Request  → invalid JSON body
//	422 Unprocessable → validation failure (missing name, bad DAG spec, etc.)
//	500 Internal     → store failure
func (h *PipelineHandler) CreatePipeline(w http.ResponseWriter, r *http.Request) {
	var req CreatePipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required and must not be blank")
		return
	}
	if len(req.DAGSpec.Nodes) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "dag_spec.nodes must contain at least one node")
		return
	}
	if err := validateDAGSpec(req.DAGSpec); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid dag_spec: "+err.Error())
		return
	}

	p := &domain.Pipeline{
		Name:    strings.TrimSpace(req.Name),
		Status:  domain.PipelineStatusPending,
		DAGSpec: req.DAGSpec,
	}

	created, err := h.store.CreatePipeline(r.Context(), p)
	if err != nil {
		h.logger.Error("failed to create pipeline", "name", req.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create pipeline")
		return
	}

	h.logger.Info("pipeline created",
		"pipeline_id", created.ID,
		"name", created.Name,
		"nodes", len(created.DAGSpec.Nodes),
		"edges", len(created.DAGSpec.Edges),
	)
	writeJSON(w, http.StatusCreated, created)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id} — GetPipeline
// ─────────────────────────────────────────────────────────────────────────────

// GetPipeline handles GET /pipelines/{id}.
// Returns the pipeline record including its DAG spec and current status.
// Poll this endpoint to track overall pipeline progress.
//
// Responses:
//
//	200 OK        → pipeline found (body: domain.Pipeline)
//	400 Bad Request → id is not a valid UUID
//	404 Not Found → no pipeline with that ID
//	500 Internal  → store failure
func (h *PipelineHandler) GetPipeline(w http.ResponseWriter, r *http.Request) {
	id, err := parsePipelineID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pipeline ID: must be a UUID")
		return
	}

	p, err := h.store.GetPipeline(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		h.logger.Error("failed to get pipeline", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines — ListPipelines
// ─────────────────────────────────────────────────────────────────────────────

// ListPipelines handles GET /pipelines.
// Optional query param: ?status=pending|running|completed|failed|cancelled
// Returns pipelines ordered newest first, max 50 per call.
//
// Responses:
//
//	200 OK       → {"pipelines": [...], "count": N}
//	422          → invalid status query param
//	500 Internal → store failure
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
	filter := store.PipelineFilter{Limit: 50}

	if s := r.URL.Query().Get("status"); s != "" {
		status := domain.PipelineStatus(s)
		if !isValidPipelineStatus(status) {
			writeError(w, http.StatusUnprocessableEntity,
				"invalid status: must be one of pending, running, completed, failed, cancelled")
			return
		}
		filter.Status = &status
	}

	pipelines, err := h.store.ListPipelines(r.Context(), filter)
	if err != nil {
		h.logger.Error("failed to list pipelines", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pipelines": pipelines,
		"count":     len(pipelines),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id}/jobs — GetPipelineJobs
// ─────────────────────────────────────────────────────────────────────────────

// GetPipelineJobs handles GET /pipelines/{id}/jobs.
// Returns every node that has been created as a job, with its current status.
// Nodes whose dependencies haven't completed yet do not appear in this list
// (they have no job yet). This gives a live view of pipeline execution progress.
//
// Response body:
//
//	{
//	  "pipeline_id": "...",
//	  "jobs": [
//	    {"node_id": "preprocess", "job_id": "...", "job_name": "...", "job_status": "completed"},
//	    {"node_id": "train",      "job_id": "...", "job_name": "...", "job_status": "running"}
//	  ],
//	  "count": 2
//	}
//
// Responses:
//
//	200 OK        → list of pipeline job statuses (may be empty if just created)
//	400 Bad Request → id is not a valid UUID
//	404 Not Found → no pipeline with that ID
//	500 Internal  → store failure
func (h *PipelineHandler) GetPipelineJobs(w http.ResponseWriter, r *http.Request) {
	id, err := parsePipelineID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pipeline ID: must be a UUID")
		return
	}

	// Verify pipeline exists before querying pipeline_jobs.
	// Without this, an invalid UUID that coincidentally has pipeline_jobs rows
	// would return 200 with stale data rather than 404.
	if _, err := h.store.GetPipeline(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		h.logger.Error("failed to get pipeline", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	jobs, err := h.store.GetPipelineJobs(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get pipeline jobs", "pipeline_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pipeline_id": id,
		"jobs":        jobs,
		"count":       len(jobs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DAG validation
// ─────────────────────────────────────────────────────────────────────────────

// validateDAGSpec checks a DAG spec for structural correctness before persisting.
// It catches the most common mistakes without requiring a full cycle-detection
// algorithm (though a DFS cycle check could be added for extra safety).
//
// Checks performed:
//  1. Every node has a non-empty, unique ID
//  2. Every node has either handler_name (inline) or kubernetes_spec (k8s_job)
//  3. Every edge references node IDs that exist in the nodes list
//  4. No self-loops (source == target)
func validateDAGSpec(dag domain.DAGSpec) error {
	if len(dag.Nodes) == 0 {
		return errors.New("at least one node is required")
	}

	// Build a set of all node IDs for O(1) edge validation.
	nodeIDs := make(map[string]bool, len(dag.Nodes))
	for _, n := range dag.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return errors.New("all nodes must have a non-empty id")
		}
		if nodeIDs[n.ID] {
			return errors.New("duplicate node id: " + n.ID)
		}
		nodeIDs[n.ID] = true

		// Every node must have exactly one execution backend specified.
		hasInline := strings.TrimSpace(n.JobTemplate.HandlerName) != ""
		hasK8s := n.JobTemplate.KubernetesSpec != nil

		if !hasInline && !hasK8s {
			return errors.New("node \"" + n.ID + "\": job_template must specify " +
				"handler_name (inline) or kubernetes_spec (k8s_job)")
		}
		if hasInline && hasK8s {
			return errors.New("node \"" + n.ID + "\": job_template must specify " +
				"either handler_name or kubernetes_spec, not both")
		}
	}

	// Validate all edges reference real nodes and have no self-loops.
	for _, e := range dag.Edges {
		if !nodeIDs[e.Source] {
			return errors.New("edge source \"" + e.Source + "\" is not a known node id")
		}
		if !nodeIDs[e.Target] {
			return errors.New("edge target \"" + e.Target + "\" is not a known node id")
		}
		if e.Source == e.Target {
			return errors.New("self-loop detected on node \"" + e.Source + "\"")
		}
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// parsePipelineID extracts and validates the {id} path parameter as a UUID.
func parsePipelineID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue("id"))
}

// isValidPipelineStatus returns true if s is a known PipelineStatus constant.
func isValidPipelineStatus(s domain.PipelineStatus) bool {
	switch s {
	case domain.PipelineStatusPending,
		domain.PipelineStatusRunning,
		domain.PipelineStatusCompleted,
		domain.PipelineStatusFailed,
		domain.PipelineStatusCancelled:
		return true
	}
	return false
}
