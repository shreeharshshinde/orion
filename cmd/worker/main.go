package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
	redisqueue "github.com/shreeharshshinde/orion/internal/queue/redis"
	"github.com/shreeharshshinde/orion/internal/store/postgres"
	"github.com/shreeharshshinde/orion/internal/worker"
	"github.com/shreeharshshinde/orion/internal/worker/handlers"
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

	// ── 7. Inline Handler Registry ────────────────────────────────────────────
	// Register ALL inline handlers before pool.Start() is called.
	// The registry is read-only at runtime — no lock contention during execution.
	//
	// Naming convention: use snake_case matching job.Payload.HandlerName exactly.
	// Name mismatch = "handler not registered" error on every job attempt.
	registry := worker.NewRegistry()

	// ── Built-in handlers (smoke tests and examples) ──────────────────────────
	registry.Register("noop", handlers.Noop)              // does nothing, always succeeds
	registry.Register("echo", handlers.Echo)              // logs args and succeeds
	registry.Register("always_fail", handlers.AlwaysFail) // always fails (test retry cycle)
	registry.Register("slow", handlers.Slow)              // long-running, respects ctx

	// ── Production ML handlers ────────────────────────────────────────────────
	// Uncomment and implement as needed. Each handler should be in its own
	// file under internal/worker/handlers/ or a domain-specific package.
	//
	// registry.Register("preprocess_dataset",  mlhandlers.PreprocessDataset)
	// registry.Register("validate_schema",     mlhandlers.ValidateSchema)
	// registry.Register("compute_statistics",  mlhandlers.ComputeStatistics)
	// registry.Register("split_train_test",    mlhandlers.SplitTrainTest)
	// registry.Register("train_model",         mlhandlers.TrainModel)
	// registry.Register("evaluate_model",      mlhandlers.EvaluateModel)
	// registry.Register("export_to_onnx",      mlhandlers.ExportToONNX)
	// registry.Register("deploy_to_staging",   mlhandlers.DeployToStaging)

	// InlineExecutor handles all jobs with type = "inline".
	// KubernetesExecutor (Phase 4) will be added for type = "k8s_job".
	inlineExecutor := worker.NewInlineExecutor(registry, logger)

	executors := []worker.Executor{
		inlineExecutor,
		// k8s.NewKubernetesExecutor(k8sClient, cfg.Kubernetes, logger)  ← Phase 4
	}

	logger.Info("inline executor ready",
		"registered_handlers", registry.List(),
		"handler_count", registry.Len(),
	)

	// ── 8. Worker pool ────────────────────────────────────────────────────────
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
	logger.Info("starting worker pool",
		"worker_id", cfg.Worker.WorkerID,
		"concurrency", cfg.Worker.Concurrency,
		"queues", queueNames,
	)

	if err := pool.Start(ctx); err != nil {
		logger.Error("worker pool exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("worker pool stopped cleanly")
}
