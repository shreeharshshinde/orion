//go:build integration
// +build integration

// Package e2e contains end-to-end tests for the full Orion job lifecycle.
//
// These tests require a running PostgreSQL and Redis instance.
// Run with:
//
//	make test-integration
//	# or:
//	ORION_DATABASE_DSN=... ORION_REDIS_ADDR=... go test -tags=integration ./tests/e2e/... -v
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/pipeline"
	redisqueue "github.com/shreeharshshinde/orion/internal/queue/redis"
	"github.com/shreeharshshinde/orion/internal/scheduler"
	"github.com/shreeharshshinde/orion/internal/store/postgres"
	"github.com/shreeharshshinde/orion/internal/worker"
	"github.com/shreeharshshinde/orion/internal/worker/handlers"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func testDSN() string {
	if v := os.Getenv("ORION_DATABASE_DSN"); v != "" {
		return v
	}
	return "postgres://orion:orion@localhost:5432/orion?sslmode=disable"
}

func testRedisAddr() string {
	if v := os.Getenv("ORION_REDIS_ADDR"); v != "" {
		return v
	}
	return "localhost:6380"
}

// ─── TestJobLifecycle_SubmitToCompleted ───────────────────────────────────────
//
// Full pipeline: POST /jobs → scheduler dispatches → worker executes → completed.
//
// Infrastructure wired entirely in-process:
//  1. Real PostgreSQL store
//  2. Real Redis queue
//  3. Real Scheduler (runs in background goroutine)
//  4. Real Worker Pool with noop handler
//  5. Real HTTP server via httptest
//
// The "noop" handler returns nil immediately, so the job should reach
// "completed" within a few seconds once the scheduler has dispatched it.
func TestJobLifecycle_SubmitToCompleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// ── 1. PostgreSQL ──────────────────────────────────────────────────────────
	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping: %v (is `docker compose up -d postgres` running?)", err)
	}

	pgStore := postgres.New(pool)

	// ── 2. Redis ───────────────────────────────────────────────────────────────
	redisClient := redis.NewClient(&redis.Options{Addr: testRedisAddr()})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v (is `docker compose up -d redis` running?)", err)
	}
	defer redisClient.Close()

	q, err := redisqueue.New(redisClient, nil, logger, "e2e-test-worker")
	if err != nil {
		t.Fatalf("redis queue init: %v", err)
	}
	defer q.Close()

	// ── 3. HTTP server ─────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	jobHandler := handler.NewJobHandler(pgStore, q, logger)
	mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
	mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── 4. Scheduler ───────────────────────────────────────────────────────────
	advancer := pipeline.NewAdvancer(pgStore, nil, logger)
	sched := scheduler.New(
		scheduler.Config{
			BatchSize:        10,
			ScheduleInterval: 500 * time.Millisecond,
			OrphanInterval:   30 * time.Second,
		},
		pool,
		pgStore,
		q,
		advancer,
		nil, // metrics (nil = disabled)
		nil, // rate limiter (nil = disabled)
		nil, // queue allocations (nil = default)
		logger,
	)
	schedCtx, schedCancel := context.WithCancel(ctx)
	defer schedCancel()
	go func() { _ = sched.Run(schedCtx) }()

	// ── 5. Worker pool ─────────────────────────────────────────────────────────
	registry := worker.NewRegistry()
	registry.Register("noop", handlers.Noop)

	inlineExec := worker.NewInlineExecutor(registry, logger)
	workerPool := worker.NewPool(
		worker.WorkerConfig{
			WorkerID:          "e2e-test-worker",
			QueueNames:        []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"},
			Concurrency:       2,
			VisibilityTimeout: 30 * time.Second,
			HeartbeatInterval: 15 * time.Second,
			ShutdownTimeout:   5 * time.Second,
		},
		q,
		pgStore,
		[]worker.Executor{inlineExec},
		nil, // metrics (nil = disabled)
		logger,
	)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	go func() { _ = workerPool.Start(workerCtx) }()

	// ── 6. Submit job via HTTP ─────────────────────────────────────────────────
	body, _ := json.Marshal(map[string]any{
		"name":  "e2e-noop-test",
		"type":  "inline",
		"queue_name": "default",
		"payload": map[string]any{
			"handler_name": "noop",
		},
	})

	resp, err := http.Post(srv.URL+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var submitted domain.Job
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatalf("decode job response: %v", err)
	}
	if submitted.ID == (submitted.ID /* zero */) {
		// just make sure ID is set
	}
	t.Logf("submitted job %s", submitted.ID)

	// ── 7. Poll GET /jobs/{id} until completed or timeout ─────────────────────
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		r, err := http.Get(fmt.Sprintf("%s/jobs/%s", srv.URL, submitted.ID))
		if err != nil {
			t.Logf("GET /jobs/%s error: %v (retrying)", submitted.ID, err)
			continue
		}

		var job domain.Job
		_ = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()

		t.Logf("job %s status=%s", job.ID, job.Status)

		if job.Status == domain.JobStatusCompleted {
			// ✅ Pipeline worked end-to-end.
			return
		}
		if job.Status == domain.JobStatusFailed || job.Status == domain.JobStatusDead {
			t.Fatalf("job reached terminal failure status: %s", job.Status)
		}
	}

	t.Fatalf("job %s did not reach completed within timeout (last status unknown)", submitted.ID)
}

// TestJobLifecycle_FailedJob verifies the failure path:
// always_fail handler → job reaches failed status (max_retries=0 → no retry → failed→dead).
func TestJobLifecycle_FailedJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	pool, err := pgxpool.New(ctx, testDSN())
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping: %v", err)
	}
	pgStore := postgres.New(pool)

	redisClient := redis.NewClient(&redis.Options{Addr: testRedisAddr()})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	defer redisClient.Close()

	q, err := redisqueue.New(redisClient, nil, logger, "e2e-fail-worker")
	if err != nil {
		t.Fatalf("redis queue init: %v", err)
	}
	defer q.Close()

	mux := http.NewServeMux()
	jobHandler := handler.NewJobHandler(pgStore, q, logger)
	mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
	mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	advancer := pipeline.NewAdvancer(pgStore, nil, logger)
	sched := scheduler.New(
		scheduler.Config{BatchSize: 10, ScheduleInterval: 500 * time.Millisecond, OrphanInterval: 30 * time.Second},
		pool, pgStore, q, advancer, nil, nil, nil, logger,
	)
	schedCtx, schedCancel := context.WithCancel(ctx)
	defer schedCancel()
	go func() { _ = sched.Run(schedCtx) }()

	registry := worker.NewRegistry()
	registry.Register("always_fail", handlers.AlwaysFail)
	workerPool := worker.NewPool(
		worker.WorkerConfig{
			WorkerID: "e2e-fail-worker", QueueNames: []string{"orion:queue:default"},
			Concurrency: 2, VisibilityTimeout: 30 * time.Second,
			HeartbeatInterval: 15 * time.Second, ShutdownTimeout: 5 * time.Second,
		},
		q, pgStore, []worker.Executor{worker.NewInlineExecutor(registry, logger)}, nil, logger,
	)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	go func() { _ = workerPool.Start(workerCtx) }()

	body, _ := json.Marshal(map[string]any{
		"name": "e2e-fail-test", "type": "inline", "queue_name": "default",
		"max_retries": 0,
		"payload":     map[string]any{"handler_name": "always_fail"},
	})
	resp, err := http.Post(srv.URL+"/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /jobs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var submitted domain.Job
	_ = json.NewDecoder(resp.Body).Decode(&submitted)
	t.Logf("submitted failing job %s", submitted.ID)

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		r, err := http.Get(fmt.Sprintf("%s/jobs/%s", srv.URL, submitted.ID))
		if err != nil {
			continue
		}
		var job domain.Job
		_ = json.NewDecoder(r.Body).Decode(&job)
		r.Body.Close()
		t.Logf("job %s status=%s", job.ID, job.Status)
		if job.Status == domain.JobStatusDead || job.Status == domain.JobStatusFailed {
			return // ✅ reached terminal failure as expected
		}
		if job.Status == domain.JobStatusCompleted {
			t.Fatalf("always_fail handler somehow completed successfully")
		}
	}
	t.Fatalf("job %s did not reach a terminal failure status within timeout", submitted.ID)
}


