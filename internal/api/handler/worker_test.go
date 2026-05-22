package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"
	"os"

	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/domain"
)

func newWorkerStore(workers []*domain.Worker, err error) *fakeStore {
	return &fakeStore{
		listActiveWorkersFn: func(_ context.Context, _ time.Duration) ([]*domain.Worker, error) {
			return workers, err
		},
	}
}

func TestListWorkers_ReturnsActiveWorkers(t *testing.T) {
	now := time.Now()
	workers := []*domain.Worker{
		{
			ID:            "worker-1",
			Hostname:      "host-a",
			QueueNames:    []string{"orion:queue:high"},
			Concurrency:   5,
			ActiveJobs:    2,
			Status:        domain.WorkerStatusIdle,
			LastHeartbeat: now,
			RegisteredAt:  now.Add(-time.Hour),
		},
	}
	h := handler.NewWorkerHandler(newWorkerStore(workers, nil), slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	w := httptest.NewRecorder()
	h.ListWorkers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"].(float64) != 1 {
		t.Errorf("expected count=1, got %v", resp["count"])
	}
}

func TestListWorkers_EmptyList(t *testing.T) {
	h := handler.NewWorkerHandler(newWorkerStore([]*domain.Worker{}, nil), slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	w := httptest.NewRecorder()
	h.ListWorkers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"].(float64) != 0 {
		t.Errorf("expected count=0, got %v", resp["count"])
	}
}

func TestListWorkers_StoreError(t *testing.T) {
	h := handler.NewWorkerHandler(newWorkerStore(nil, errors.New("db down")), slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	w := httptest.NewRecorder()
	h.ListWorkers(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}
