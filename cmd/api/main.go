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

	"github.com/shreeharshshinde/orion/internal/api/handler"
	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
)

func main() {
	// Step 1: Load all config from environment variables
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Step 2: Set up structured logging
	// JSON format in production, human-readable text in development
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-api", cfg.Service.Environment)

	// Step 3: Set up distributed tracing (sends spans to Jaeger)
	ctx := context.Background()
	shutdownTracing, err := observability.SetupTracing(
		ctx,
		"orion-api",
		cfg.Observability.ServiceVersion,
		cfg.Observability.OTLPEndpoint,
		cfg.Observability.TracingSampleRate,
	)
	if err != nil {
		// Tracing is not critical — log the warning and continue
		logger.Warn("tracing setup failed, continuing without traces", "err", err)
	} else {
		// Flush remaining spans before the process exits
		defer func() {
			tCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTracing(tCtx)
		}()
	}

	// ─────────────────────────────────────────────────────────
	// TODO Phase 2: Connect to PostgreSQL and wire into handler
	// ─────────────────────────────────────────────────────────
	// db, err := pgxpool.New(ctx, cfg.Database.DSN)
	// if err != nil {
	//     logger.Error("failed to connect to postgres", "err", err)
	//     os.Exit(1)
	// }
	// defer db.Close()
	// store := postgres.New(db)
	// ─────────────────────────────────────────────────────────

	// Step 4: Register HTTP routes (Go 1.22 pattern-based routing)
	mux := http.NewServeMux()

	jobHandler := handler.NewJobHandler(nil, logger) // nil → replace with store in Phase 2
	mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
	mux.HandleFunc("GET /jobs", jobHandler.ListJobs)
	mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)

	// Health check — Kubernetes liveness probe calls this
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Readiness check — Kubernetes readiness probe calls this
	// TODO Phase 2: ping DB + Redis here before returning 200
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	// Step 5: Create HTTP server with explicit timeouts
	// These prevent slow clients from holding connections open forever
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Service.HTTPPort),
		Handler:      mux,
		ReadTimeout:  10 * time.Second, // max time to read request body
		WriteTimeout: 30 * time.Second, // max time to write response
		IdleTimeout:  60 * time.Second, // max time to keep idle connections open
	}

	// Step 6: Set up graceful shutdown on SIGTERM / SIGINT (Ctrl+C)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Start server in background goroutine
	go func() {
		logger.Info("API server listening", "port", cfg.Service.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Block here until SIGTERM or Ctrl+C
	<-stop
	logger.Info("received shutdown signal")

	// Give in-flight requests up to 15 seconds to complete
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	} else {
		logger.Info("server shut down cleanly")
	}
}