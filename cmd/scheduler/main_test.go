package main

import (
	"testing"

	"github.com/shreeharshshinde/orion/internal/config"
)

// TestConfigLoad verifies config.Load() succeeds with defaults (no infra required).
func TestConfigLoad(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error: %v", err)
	}
	if cfg.Scheduler.BatchSize <= 0 {
		t.Fatalf("expected BatchSize > 0, got %d", cfg.Scheduler.BatchSize)
	}
	if cfg.Scheduler.ScheduleInterval <= 0 {
		t.Fatal("expected ScheduleInterval > 0")
	}
	if cfg.Scheduler.OrphanInterval <= 0 {
		t.Fatal("expected OrphanInterval > 0")
	}
}

// TestSchedulerConfigDefaults verifies the scheduler-specific defaults are sane.
func TestSchedulerConfigDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}

	// These are the defaults set in config.go; smoke-test that they weren't zeroed.
	if cfg.Queue.High.Weight <= 0 {
		t.Fatal("expected High queue weight > 0")
	}
	if cfg.Queue.Default.Weight <= 0 {
		t.Fatal("expected Default queue weight > 0")
	}
	if cfg.Queue.Low.Weight <= 0 {
		t.Fatal("expected Low queue weight > 0")
	}
}
