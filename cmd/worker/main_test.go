package main

import (
	"context"
	"testing"

	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/worker"
)

// TestConfigLoad verifies config.Load() succeeds with defaults (no infra required).
func TestConfigLoad(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}
	if cfg.Worker.Concurrency <= 0 {
		t.Fatalf("expected Concurrency > 0, got %d", cfg.Worker.Concurrency)
	}
	if len(cfg.Worker.Queues) == 0 {
		t.Fatal("expected at least one queue configured")
	}
	if cfg.Worker.WorkerID == "" {
		t.Fatal("expected non-empty WorkerID")
	}
}

// TestWorkerRegistryStartup verifies the inline handler registry builds without errors.
// This covers the registry.Register + registry.List path used in main().
func TestWorkerRegistryStartup(t *testing.T) {
	registry := worker.NewRegistry()
	registry.Register("noop", func(_ context.Context, _ *domain.Job) error { return nil })

	if registry.Len() != 1 {
		t.Fatalf("expected 1 registered handler, got %d", registry.Len())
	}
	names := registry.List()
	if len(names) != 1 || names[0] != "noop" {
		t.Fatalf("unexpected handler list: %v", names)
	}
}
