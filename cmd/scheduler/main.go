package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shreeharshshinde/orion/internal/config"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/scheduler"
)

func main() {
	// Step 1: Load config from environment variables
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Step 2: Set up structured logger
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-scheduler", cfg.Service.Environment)

	// Step 3: Create a cancellable context.
	// When ctx is cancelled, all goroutines inside the scheduler see ctx.Done()
	// and exit cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 4: Listen for OS shutdown signals
	// SIGTERM is what Kubernetes sends when stopping a pod
	// SIGINT  is what you get when pressing Ctrl+C locally
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("received shutdown signal")
		cancel() // this propagates to scheduler.Run() via ctx
	}()

	// ─────────────────────────────────────────────────────────────
	// TODO Phase 2: wire real dependencies
	// ─────────────────────────────────────────────────────────────
	// db, err := pgxpool.New(ctx, cfg.Database.DSN)
	// if err != nil {
	//     logger.Error("failed to connect to postgres", "err", err)
	//     os.Exit(1)
	// }
	// defer db.Close()
	//
	// store := postgres.New(db)
	//
	// redisClient := redis.NewClient(&redis.Options{
	//     Addr:     cfg.Redis.Addr,
	//     Password: cfg.Redis.Password,
	//     DB:       cfg.Redis.DB,
	// })
	// queue, err := redisqueue.New(redisClient, logger)
	// if err != nil {
	//     logger.Error("failed to init redis queue", "err", err)
	//     os.Exit(1)
	// }
	// defer queue.Close()
	// ─────────────────────────────────────────────────────────────

	// Step 5: Create the scheduler
	// nil placeholders will be replaced with real db/store/queue in Phase 2
	sched := scheduler.New(
		scheduler.Config{
			BatchSize:        cfg.Scheduler.BatchSize,
			ScheduleInterval: cfg.Scheduler.ScheduleInterval,
			OrphanInterval:   cfg.Scheduler.OrphanInterval,
		},
		nil, // *pgxpool.Pool — needed for advisory lock queries
		nil, // store.Store
		nil, // queue.Queue
		logger,
	)

	// Step 6: Run the scheduler (blocks until ctx is cancelled)
	// Internally: contends for PostgreSQL advisory lock,
	// only runs dispatch loops when it becomes leader.
	logger.Info("starting scheduler",
		"batch_size", cfg.Scheduler.BatchSize,
		"schedule_interval", cfg.Scheduler.ScheduleInterval,
		"orphan_interval", cfg.Scheduler.OrphanInterval,
	)

	if err := sched.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("scheduler exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("scheduler stopped cleanly")
}
