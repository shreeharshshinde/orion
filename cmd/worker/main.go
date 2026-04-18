package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	// ── 1. Config ─────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// ── 2. Logger ─────────────────────────────────────────────────────────────
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-worker", cfg.Service.Environment)

	// ── 3. Context + signal handling ──────────────────────────────────────────
	// Cancelling ctx triggers the worker pool's graceful drain:
	//   → dequeue goroutines stop pulling from Redis
	//   → in-flight jobs complete (up to ShutdownTimeout = 30s)
	//   → KubernetesExecutor cancels any active watches and deletes orphaned pods
	//   → pool.Start() returns cleanly
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("shutdown signal received, draining in-flight jobs")
		cancel()
	}()

	// ── 4. PostgreSQL connection pool ─────────────────────────────────────────
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

	// ── 5. Store ──────────────────────────────────────────────────────────────
	pgStore := postgres.New(db)

	// ── 6. Redis client + queue ───────────────────────────────────────────────
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

	queue, err := redisqueue.New(redisClient, logger)
	if err != nil {
		logger.Error("failed to initialize redis queue", "err", err)
		os.Exit(1)
	}
	defer queue.Close()

	// ── 7a. Inline Handler Registry (Phase 3) ─────────────────────────────────
	// Register all handlers before pool.Start() — registry is read-only at runtime.
	registry := worker.NewRegistry()

	// Built-in handlers: smoke-test utilities and code examples.
	registry.Register("noop", handlers.Noop)
	registry.Register("echo", handlers.Echo)
	registry.Register("always_fail", handlers.AlwaysFail)
	registry.Register("slow", handlers.Slow)

	// Production ML handlers — uncomment and implement as needed.
	// registry.Register("preprocess_dataset",  mlhandlers.PreprocessDataset)
	// registry.Register("validate_schema",     mlhandlers.ValidateSchema)
	// registry.Register("compute_statistics",  mlhandlers.ComputeStatistics)
	// registry.Register("train_model",         mlhandlers.TrainModel)
	// registry.Register("evaluate_model",      mlhandlers.EvaluateModel)
	// registry.Register("export_to_onnx",      mlhandlers.ExportToONNX)

	inlineExecutor := worker.NewInlineExecutor(registry, logger)
	logger.Info("inline executor ready",
		"registered_handlers", registry.List(),
		"handler_count", registry.Len(),
	)

	// ── 7b. Kubernetes Executor (Phase 4) ─────────────────────────────────────
	// Build the K8s client from config:
	//   ORION_K8S_IN_CLUSTER=true  → reads ServiceAccount token (inside cluster)
	//   ORION_K8S_IN_CLUSTER=false → reads ~/.kube/config (local dev / kind)
	//
	// Failure is non-fatal: if K8s is unavailable, inline jobs still work.
	// k8s_job submissions will fail with "no executor registered" and retry.
	// When the cluster becomes available and the worker restarts, they succeed.
	executors := []worker.Executor{inlineExecutor}

	k8sClient, err := k8s.BuildK8sClient(cfg.Kubernetes)
	if err != nil {
		logger.Warn("kubernetes client unavailable — k8s_job type will not be served",
			"err", err,
			"in_cluster", cfg.Kubernetes.InCluster,
			"kubeconfig", cfg.Kubernetes.KubeconfigPath,
		)
	} else {
		k8sExecutor := k8s.NewKubernetesExecutor(
			k8sClient,
			k8s.ExecutorConfig{
				DefaultNamespace: cfg.Kubernetes.DefaultNamespace,
				PollInterval:     5 * time.Second,
			},
			logger,
		)
		executors = append(executors, k8sExecutor)
		logger.Info("kubernetes executor ready",
			"default_namespace", cfg.Kubernetes.DefaultNamespace,
			"in_cluster", cfg.Kubernetes.InCluster,
		)
	}

	// ── 8. Worker pool ────────────────────────────────────────────────────────
	// Priority order for queues: high is checked first, then default, then low.
	// Each queue name gets its own dequeue goroutine inside pool.Start().
	queueNames := cfg.Worker.Queues
	if len(queueNames) == 0 {
		queueNames = []string{
			"orion:queue:high",
			"orion:queue:default",
			"orion:queue:low",
		}
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
		queue,
		pgStore,
		executors,
		logger,
	)

	// ── 9. Start ──────────────────────────────────────────────────────────────
	// pool.Start() blocks until ctx is cancelled, then drains gracefully.
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
