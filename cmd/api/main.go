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
	"github.com/prometheus/client_golang/prometheus"
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
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-api", cfg.Service.Environment)

	// ── 3. Prometheus registry + metrics [Phase 6] ────────────────────────────
	// Each binary creates its own isolated registry.
	// The API reports: HTTP metrics + DB create/get latency.
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	// Start the /metrics endpoint on the dedicated metrics port (:9091).
	// Prometheus scrapes this every 15s. Run in background — not critical path.
	metricsSrv := observability.MetricsServer(cfg.Observability.MetricsPort, reg)
	go func() {
		logger.Info("metrics server listening", "port", cfg.Observability.MetricsPort)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "err", err)
		}
	}()

	// ── 4. Tracing ────────────────────────────────────────────────────────────
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

	// ── 5. PostgreSQL ─────────────────────────────────────────────────────────
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
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		logger.Error("postgres ping failed", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to postgres")

	// ── 6. Store ──────────────────────────────────────────────────────────────
	pgStore := postgres.New(db)

	// ── 7. HTTP routes ────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	jobHandler := handler.NewJobHandler(pgStore, logger)
	mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
	mux.HandleFunc("GET /jobs", jobHandler.ListJobs)
	mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
	mux.HandleFunc("GET /jobs/{id}/executions", jobHandler.GetExecutions)

	pipelineHandler := handler.NewPipelineHandler(pgStore, logger)
	mux.HandleFunc("POST /pipelines", pipelineHandler.CreatePipeline)
	mux.HandleFunc("GET /pipelines", pipelineHandler.ListPipelines)
	mux.HandleFunc("GET /pipelines/{id}", pipelineHandler.GetPipeline)
	mux.HandleFunc("GET /pipelines/{id}/jobs", pipelineHandler.GetPipelineJobs)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// ── 8. Apply observability middleware [Phase 6] ───────────────────────────
	// Layer order (outer → inner):
	//   TracingMiddleware → MetricsMiddleware → mux
	//
	// TracingMiddleware runs first so the trace context is available to
	// MetricsMiddleware and all downstream handlers. The span is created
	// at the edge of the system.
	//
	// MetricsMiddleware records duration and status code AFTER the handler
	// runs (via the defer inside the middleware), so it captures the true
	// final status including errors set by the handler.
	instrumentedHandler := handler.TracingMiddleware(
		handler.MetricsMiddleware(metrics, mux),
	)

	// ── 9. HTTP server ────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Service.HTTPPort),
		Handler:      instrumentedHandler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

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

	<-stop
	logger.Info("shutdown signal received")

	// Shutdown metrics server first (not critical path)
	_ = metricsSrv.Shutdown(context.Background())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	} else {
		logger.Info("API server stopped cleanly")
	}
}
