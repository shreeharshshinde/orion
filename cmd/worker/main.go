package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/worker"
)

func main() {
	// Step 1: Load config from environment variables
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Step 2: Set up structured logger
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-worker", cfg.Service.Environment)

	// Step 3: Cancellable context — cancellation triggers graceful drain
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 4: Signal handling
	// On SIGTERM (Kubernetes pod stop) or SIGINT (Ctrl+C):
	// → cancel the context
	// → worker pool stops dequeuing, drains in-flight jobs (up to ShutdownTimeout)
	// → process exits cleanly
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("received shutdown signal, draining in-flight jobs")
		cancel()
	}()

	// ─────────────────────────────────────────────────────────────
	// TODO Phase 2: wire real dependencies
	// ─────────────────────────────────────────────────────────────
	// redisClient := redis.NewClient(&redis.Options{
	//     Addr:     cfg.Redis.Addr,
	//     Password: cfg.Redis.Password,
	// })
	// queue, err := redisqueue.New(redisClient, logger)
	// if err != nil {
	//     logger.Error("failed to init redis queue", "err", err)
	//     os.Exit(1)
	// }
	// defer queue.Close()
	//
	// db, _ := pgxpool.New(ctx, cfg.Database.DSN)
	// store := postgres.New(db)
	// ─────────────────────────────────────────────────────────────

	// ─────────────────────────────────────────────────────────────
	// TODO Phase 4: register inline executor
	// TODO Phase 5: register kubernetes executor
	// ─────────────────────────────────────────────────────────────
	// executors := []worker.Executor{
	//     inline.NewExecutor(map[string]inline.HandlerFunc{
	//         "preprocess": handlers.Preprocess,
	//         "evaluate":   handlers.Evaluate,
	//     }),
	//     k8s.NewExecutor(k8sClient, cfg.Kubernetes.DefaultNamespace),
	// }
	// ─────────────────────────────────────────────────────────────

	// Step 5: Create the worker pool
	pool := worker.NewPool(
		worker.WorkerConfig{
			WorkerID:          cfg.Worker.WorkerID,          // defaults to hostname
			QueueNames:        cfg.Worker.Queues,            // which queues to pull from
			Concurrency:       cfg.Worker.Concurrency,       // max parallel jobs (default 10)
			VisibilityTimeout: cfg.Worker.VisibilityTimeout, // 5m — Redis PEL timeout
			HeartbeatInterval: cfg.Worker.HeartbeatInterval, // 15s — tell scheduler we're alive
			ShutdownTimeout:   cfg.Worker.ShutdownTimeout,   // 30s — max drain time on SIGTERM
		},
		nil, // queue.Queue  — replace in Phase 2
		nil, // store.Store  — replace in Phase 2
		nil, // []Executor   — replace in Phase 4/5
		logger,
	)

	// Step 6: Start the pool (blocks until ctx is cancelled, then drains)
	logger.Info("starting worker pool",
		"worker_id", cfg.Worker.WorkerID,
		"concurrency", cfg.Worker.Concurrency,
		"queues", cfg.Worker.Queues,
	)

	if err := pool.Start(ctx); err != nil {
		logger.Error("worker pool error", "err", err)
		os.Exit(1)
	}

	logger.Info("worker pool stopped cleanly")
}
