package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/shreeharshshinde/orion/internal/store"
)

const workerTTL = 45 * time.Second

// WorkerHandler handles HTTP requests for worker visibility.
type WorkerHandler struct {
	store  store.Store
	logger *slog.Logger
}

func NewWorkerHandler(s store.Store, logger *slog.Logger) *WorkerHandler {
	return &WorkerHandler{store: s, logger: logger}
}

// ListWorkers handles GET /workers.
// Returns all workers that have sent a heartbeat within the last 45 seconds.
func (h *WorkerHandler) ListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.store.ListActiveWorkers(r.Context(), workerTTL)
	if err != nil {
		h.logger.Error("failed to list workers", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workers": workers,
		"count":   len(workers),
	})
}
