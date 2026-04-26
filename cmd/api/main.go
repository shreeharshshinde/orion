package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/store/postgres"
)

func main() {
	// ── 1. Config ─────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// ── 2. Logger ─────────────────────────────────────────────────────────────
	// JSON in production/staging, human-readable text in development.
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-api", cfg.Service.Environment)

	// ── 3. Tracing ────────────────────────────────────────────────────────────
	// Non-critical: if Jaeger is not running, warn and continue without traces.
	ctx := context.Background()
	shutdownTracing, err := observability.SetupTracing(
		ctx,
		"orion-api",
		cfg.Observability.ServiceVersion,
		cfg.Observability.OTLPEndpoint,
		cfg.Observability.TracingSampleRate,
	)
	if err != nil {
		logger.Warn("tracing setup failed, continuing without traces", "err", err)
	} else {
		defer func() {
			tCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTracing(tCtx)
		}()
	}

	// ── 4. PostgreSQL connection pool ─────────────────────────────────────────
	// pgxpool.New dials PostgreSQL and verifies the connection.
	// If the DB is unreachable at startup, fail fast rather than serving
	// requests that will all fail immediately.
	poolCfg, err := pgxpool.ParseConfig(cfg.Database.DSN)
	if err != nil {
		logger.Error("invalid database DSN", "err", err)
		os.Exit(1)
	}
	poolCfg.MaxConns = cfg.Database.MaxConns
	poolCfg.MinConns = cfg.Database.MinConns
	poolCfg.MaxConnIdleTime = cfg.Database.MaxConnIdleTime
	poolCfg.MaxConnLifetime = cfg.Database.MaxConnLifetime

	db, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		logger.Error("failed to connect to postgres", "dsn", cfg.Database.DSN, "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Ping to verify the pool can actually reach the database.
	if err := db.Ping(ctx); err != nil {
		logger.Error("postgres ping failed", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to postgres")

	// ── 5. Store ──────────────────────────────────────────────────────────────
	// postgres.New wraps the pool. All SQL lives in the postgres package.
	// Handlers only see the store.Store interface — no SQL, no pgx.
	// Phase 5: postgres.DB now implements PipelineStore in addition to the
	// existing JobStore, ExecutionStore, and WorkerStore.
	pgStore := postgres.New(db)

	// ── 6. HTTP routes ────────────────────────────────────────────────────────
	// Go 1.22 pattern-based routing — no third-party router needed.
	mux := http.NewServeMux()

	// ── Job routes (Phases 1-4, unchanged) ───────────────────────────────────
	jobHandler := handler.NewJobHandler(pgStore, logger)
	mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
	mux.HandleFunc("GET /jobs", jobHandler.ListJobs)
	mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
	mux.HandleFunc("GET /jobs/{id}/executions", jobHandler.GetExecutions)

	// ── Pipeline routes [Phase 5] ─────────────────────────────────────────────
	// POST /pipelines           — submit a new DAG pipeline
	// GET  /pipelines           — list all pipelines (optional ?status= filter)
	// GET  /pipelines/{id}      — get pipeline status + DAG spec
	// GET  /pipelines/{id}/jobs — get all node→job mappings with live status
	//
	// The handler is thin — all DAG advancement logic lives in
	// internal/pipeline/advancement.go, called by the scheduler on every tick.
	pipelineHandler := handler.NewPipelineHandler(pgStore, logger)
	mux.HandleFunc("POST /pipelines", pipelineHandler.CreatePipeline)
	mux.HandleFunc("GET /pipelines", pipelineHandler.ListPipelines)
	mux.HandleFunc("GET /pipelines/{id}", pipelineHandler.GetPipeline)
	mux.HandleFunc("GET /pipelines/{id}/jobs", pipelineHandler.GetPipelineJobs)

	// ── Health probes ─────────────────────────────────────────────────────────
	// Liveness probe: Kubernetes calls this to know the process is running.
	// Returns 200 if the process is alive, regardless of DB state.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Readiness probe: Kubernetes calls this before sending traffic.
	// Returns 200 only if we can reach PostgreSQL (the only hard dependency
	// for the API — Redis is the scheduler's concern, not the API's).
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			logger.Warn("readiness check failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable","reason":"postgres unreachable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// ── 7. HTTP server with explicit timeouts ─────────────────────────────────
	// Without timeouts, a slow client can hold a connection open forever
	// ("slow loris" attack). These bounds protect against that.
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Service.HTTPPort),
		Handler:      mux,
		ReadTimeout:  10 * time.Second, // max time to read the full request body
		WriteTimeout: 30 * time.Second, // max time to write the full response
		IdleTimeout:  60 * time.Second, // max keep-alive connection idle time
	}

	// ── 8. Graceful shutdown ──────────────────────────────────────────────────
	// SIGTERM: Kubernetes sends this when stopping a pod.
	// SIGINT:  Ctrl+C during local development.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("API server listening",
			"port", cfg.Service.HTTPPort,
			"env", cfg.Service.Environment,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server exited unexpectedly", "err", err)
			os.Exit(1)
		}
	}()

	// Block here until OS signal.
	<-stop
	logger.Info("shutdown signal received, draining connections")

	// Give in-flight requests up to 15 seconds to complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	} else {
		logger.Info("API server stopped cleanly")
	}
}
