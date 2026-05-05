// Package handler — queue.go
//
// REST API for per-queue rate limiting and fair scheduling configuration.
//
// Routes (registered in cmd/api/main.go):
//
//	GET  /queues                  → list all queue configurations
//	GET  /queues/{name}           → get config for one queue
//	PUT  /queues/{name}           → update queue config (live reload, no restart)
//	GET  /queues/{name}/stats     → real-time depth + token availability
//
// Design:
//   - The scheduler reloads queue_config on every tick, so a PUT takes effect
//     within one schedule interval (default 2 seconds) without a restart.
//   - The rate limiter reference is used by /stats to report current token count.
//   - The queue.Queue reference is used by /stats to report current stream depth.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/shreeharshshinde/orion/internal/queue"
	"github.com/shreeharshshinde/orion/internal/scheduler"
	"github.com/shreeharshshinde/orion/internal/store"
)

// QueueHandler handles HTTP requests for queue configuration management.
type QueueHandler struct {
	store       store.Store
	rateLimiter *scheduler.QueueRateLimiter
	queue       queue.Queue
	logger      *slog.Logger
}

// NewQueueHandler creates a QueueHandler.
//   - s: store for reading/writing queue_config rows
//   - rl: rate limiter for reporting current token availability in /stats
//   - q: queue for reporting current stream depth in /stats
func NewQueueHandler(
	s store.Store,
	rl *scheduler.QueueRateLimiter,
	q queue.Queue,
	logger *slog.Logger,
) *QueueHandler {
	return &QueueHandler{store: s, rateLimiter: rl, queue: q, logger: logger}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response types
// ─────────────────────────────────────────────────────────────────────────────

// UpdateQueueRequest is the JSON body for PUT /queues/{name}.
// All fields are optional — only provided fields are updated.
type UpdateQueueRequest struct {
	MaxConcurrent *int     `json:"max_concurrent,omitempty"`
	Weight        *float64 `json:"weight,omitempty"`
	RatePerSec    *float64 `json:"rate_per_sec,omitempty"`
	Burst         *int     `json:"burst,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

// QueueStatsResponse is the JSON body for GET /queues/{name}/stats.
type QueueStatsResponse struct {
	QueueName       string  `json:"queue_name"`
	Depth           int64   `json:"depth"`             // pending messages in Redis stream
	RateTokensAvail float64 `json:"rate_tokens_avail"` // current token bucket level
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

// ListQueues handles GET /queues.
//
// Returns all queue configurations from the queue_config table.
//
// Response 200:
//
//	{
//	  "queues": [ { "queue_name": "orion:queue:high", "max_concurrent": 8, ... } ],
//	  "count": 3
//	}
func (h *QueueHandler) ListQueues(w http.ResponseWriter, r *http.Request) {
	configs, err := h.store.ListQueueConfigs(r.Context())
	if err != nil {
		h.logger.Error("failed to list queue configs", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list queue configs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queues": configs,
		"count":  len(configs),
	})
}

// GetQueue handles GET /queues/{name}.
//
// Returns the configuration for a single queue.
//
// Response 200: store.QueueConfig JSON
// Response 404: queue not found
func (h *QueueHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "queue name is required")
		return
	}

	cfg, err := h.store.GetQueueConfig(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "queue not found: "+name)
			return
		}
		h.logger.Error("failed to get queue config", "queue", name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get queue config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// UpdateQueue handles PUT /queues/{name}.
//
// Updates queue configuration. Changes take effect within one scheduler tick
// (default 2 seconds) without a service restart.
//
// Request body (all fields optional):
//
//	{
//	  "max_concurrent": 4,
//	  "weight": 0.5,
//	  "rate_per_sec": 25.0,
//	  "burst": 8,
//	  "enabled": true
//	}
//
// Response 200: updated store.QueueConfig JSON
// Response 400: invalid request body or validation failure
// Response 404: queue not found
func (h *QueueHandler) UpdateQueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "queue name is required")
		return
	}

	// Load existing config so we can apply partial updates.
	existing, err := h.store.GetQueueConfig(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "queue not found: "+name)
			return
		}
		h.logger.Error("failed to get queue config for update", "queue", name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get queue config")
		return
	}

	var req UpdateQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Apply only the fields that were provided.
	if req.MaxConcurrent != nil {
		if *req.MaxConcurrent < 1 {
			writeError(w, http.StatusBadRequest, "max_concurrent must be >= 1")
			return
		}
		existing.MaxConcurrent = *req.MaxConcurrent
	}
	if req.Weight != nil {
		if *req.Weight < 0 || *req.Weight > 1 {
			writeError(w, http.StatusBadRequest, "weight must be between 0.0 and 1.0")
			return
		}
		existing.Weight = *req.Weight
	}
	if req.RatePerSec != nil {
		if *req.RatePerSec <= 0 {
			writeError(w, http.StatusBadRequest, "rate_per_sec must be > 0")
			return
		}
		existing.RatePerSec = *req.RatePerSec
	}
	if req.Burst != nil {
		if *req.Burst < 1 {
			writeError(w, http.StatusBadRequest, "burst must be >= 1")
			return
		}
		existing.Burst = *req.Burst
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}

	updated, err := h.store.UpsertQueueConfig(r.Context(), existing)
	if err != nil {
		h.logger.Error("failed to update queue config", "queue", name, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update queue config")
		return
	}

	// Apply the new rate limiter config immediately so the in-process scheduler
	// picks it up without waiting for the next DB reload tick.
	if h.rateLimiter != nil {
		h.rateLimiter.UpdateConfig(updated.QueueName, updated.RatePerSec, updated.Burst)
	}

	h.logger.Info("queue config updated",
		"queue", name,
		"rate_per_sec", updated.RatePerSec,
		"burst", updated.Burst,
		"max_concurrent", updated.MaxConcurrent,
	)
	writeJSON(w, http.StatusOK, updated)
}

// GetQueueStats handles GET /queues/{name}/stats.
//
// Returns real-time operational stats for a queue:
//   - depth: number of pending messages in the Redis stream
//   - rate_tokens_avail: current token bucket level (how many jobs can dispatch instantly)
//
// Response 200:
//
//	{
//	  "queue_name": "orion:queue:low",
//	  "depth": 42,
//	  "rate_tokens_avail": 3.7
//	}
func (h *QueueHandler) GetQueueStats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "queue name is required")
		return
	}

	var depth int64
	if h.queue != nil {
		depth, _ = h.queue.Len(r.Context(), name)
	}

	var tokens float64
	if h.rateLimiter != nil {
		tokens = h.rateLimiter.Available(name)
	}

	writeJSON(w, http.StatusOK, QueueStatsResponse{
		QueueName:       name,
		Depth:           depth,
		RateTokensAvail: tokens,
	})
}
