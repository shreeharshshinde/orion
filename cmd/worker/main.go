package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
	redisqueue "github.com/shreeharshshinde/orion/internal/queue/redis"
	"github.com/shreeharshshinde/orion/internal/store/postgres"
	"github.com/shreeharshshinde/orion/internal/worker"
	"github.com/shreeharshshinde/orion/internal/worker/handlers"
	"github.com/shreeharshshinde/orion/internal/worker/k8s"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-worker", cfg.Service.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("shutdown signal received, draining in-flight jobs")
		cancel()
	}()

	// ── Prometheus registry + metrics [Phase 6] ───────────────────────────────
	// Worker reports: job_duration, jobs_completed, jobs_failed, jobs_dead,
	// jobs_retried, worker_active_jobs, queue_depth.
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	metricsSrv := observability.MetricsServer(cfg.Observability.MetricsPort, reg)
	go func() {
		logger.Info("metrics server listening", "port", cfg.Observability.MetricsPort)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "err", err)
		}
	}()
	defer metricsSrv.Shutdown(context.Background())

	// ── Tracing ───────────────────────────────────────────────────────────────
	shutdownTracing, err := observability.SetupTracing(
		ctx, "orion-worker",
		cfg.Observability.ServiceVersion,
		cfg.Observability.OTLPEndpoint,
		cfg.Observability.TracingSampleRate,
	)
	if err != nil {
		logger.Warn("tracing setup failed, continuing without traces", "err", err)
	} else {
		defer func() {
			tCtx, tCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer tCancel()
			_ = shutdownTracing(tCtx)
		}()
	}

	// ── PostgreSQL ────────────────────────────────────────────────────────────
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

	pgStore := postgres.New(db)

	// ── Redis ─────────────────────────────────────────────────────────────────
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Error("failed to connect to redis", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	logger.Info("connected to redis", "addr", cfg.Redis.Addr)

	// Phase 6: pass metrics so the queue can report depth
	q, err := redisqueue.New(redisClient, metrics, logger)
	if err != nil {
		logger.Error("failed to initialize redis queue", "err", err)
		os.Exit(1)
	}
	defer q.Close()

	// Start queue depth poller — updates QueueDepth gauge every 5s.
	// The worker polls depth so Grafana shows the backlog from the consumer's perspective.
	q.StartQueueDepthPoller(ctx, []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"})

	// ── Inline Handler Registry ───────────────────────────────────────────────
	registry := worker.NewRegistry()
	registry.Register("noop", handlers.Noop)
	registry.Register("echo", handlers.Echo)
	registry.Register("always_fail", handlers.AlwaysFail)
	registry.Register("slow", handlers.Slow)

	inlineExecutor := worker.NewInlineExecutor(registry, logger)
	logger.Info("inline executor ready",
		"registered_handlers", registry.List(),
		"handler_count", registry.Len(),
	)

	// ── Kubernetes Executor ───────────────────────────────────────────────────
	executors := []worker.Executor{inlineExecutor}

	k8sClient, err := k8s.BuildK8sClient(cfg.Kubernetes)
	if err != nil {
		logger.Warn("kubernetes client unavailable — k8s_job type will not be served",
			"err", err,
		)
	} else {
		k8sExecutor := k8s.NewKubernetesExecutor(k8sClient, k8s.ExecutorConfig{
			DefaultNamespace: cfg.Kubernetes.DefaultNamespace,
			PollInterval:     5 * time.Second,
		}, logger)
		executors = append(executors, k8sExecutor)
		logger.Info("kubernetes executor ready", "default_namespace", cfg.Kubernetes.DefaultNamespace)
	}

	// ── Worker Pool [Phase 6 update] ──────────────────────────────────────────
	// Pass metrics as 5th argument — new in Phase 6.
	// The pool uses metrics to record job_duration, jobs_completed, jobs_failed,
	// jobs_dead, jobs_retried, and worker_active_jobs on every executeJob call.
	queueNames := cfg.Worker.Queues
	if len(queueNames) == 0 {
		queueNames = []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"}
	}

	pool := worker.NewPool(
		worker.WorkerConfig{
			WorkerID:          cfg.Worker.WorkerID,
			QueueNames:        queueNames,
			Concurrency:       cfg.Worker.Concurrency,
			VisibilityTimeout: cfg.Worker.VisibilityTimeout,
			HeartbeatInterval: cfg.Worker.HeartbeatInterval,
			ShutdownTimeout:   cfg.Worker.ShutdownTimeout,
		},
		q,
		pgStore,
		executors,
		metrics, // ← Phase 6
		logger,
	)

	logger.Info("starting worker pool",
		"worker_id", cfg.Worker.WorkerID,
		"concurrency", cfg.Worker.Concurrency,
		"queues", queueNames,
		"executors", len(executors),
	)

	if err := pool.Start(ctx); err != nil {
		logger.Error("worker pool exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("worker pool stopped cleanly")
}
