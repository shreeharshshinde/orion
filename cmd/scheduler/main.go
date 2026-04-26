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
	"github.com/shreeharshshinde/orion/internal/pipeline"
	redisqueue "github.com/shreeharshshinde/orion/internal/queue/redis"
	"github.com/shreeharshshinde/orion/internal/scheduler"
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
	logger := observability.NewLogger(cfg.Service.LogLevel, "orion-scheduler", cfg.Service.Environment)

	// ── 3. Context + signal handling ──────────────────────────────────────────
	// Cancelling ctx propagates through scheduler.Run() → all goroutines exit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("shutdown signal received")
		cancel() // triggers ctx.Done() everywhere
	}()

	// ── 4. PostgreSQL connection pool ─────────────────────────────────────────
	// The scheduler needs the raw *pgxpool.Pool (not just the Store interface)
	// because it runs advisory lock queries directly:
	//   SELECT pg_try_advisory_lock($1)
	// These must run on the same connection as the lock was acquired on,
	// which requires direct pool access rather than the store abstraction.
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
	// Phase 5: postgres.DB now satisfies the full store.Store interface including
	// PipelineStore (CreatePipeline, GetPipelineJobs, AddPipelineJob, etc.)
	// via the new postgres/pipeline.go file. No changes needed here — New() returns
	// the same *DB that now implements 4 sub-interfaces instead of 3.
	pgStore := postgres.New(db)

	// ── 6. Redis client + queue ───────────────────────────────────────────────
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})

	// Verify Redis is reachable before starting the dispatch loops.
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Error("failed to connect to redis", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	logger.Info("connected to redis", "addr", cfg.Redis.Addr)

	// redisqueue.New creates the Redis Streams consumer group on all queues.
	// It uses XGROUP CREATE MKSTREAM which is idempotent (safe to call on restart).
	queue, err := redisqueue.New(redisClient, logger)
	if err != nil {
		logger.Error("failed to initialize redis queue", "err", err)
		os.Exit(1)
	}
	defer queue.Close()

	// ── 7. Pipeline Advancer [Phase 5] ────────────────────────────────────────
	// The Advancer is the DAG advancement engine. It is passed to the scheduler
	// and called on every schedule tick (every 2 seconds by default).
	//
	// The Advancer is stateless — it reads pipeline state from pgStore and
	// creates jobs via pgStore. Reconstructing it on every restart is safe.
	//
	// Leader lock guarantee: AdvanceAll() is called inside runAsLeader(),
	// which is only entered after acquiring the PostgreSQL advisory lock.
	// Only one scheduler instance ever calls AdvanceAll() at a time.
	adv := pipeline.NewAdvancer(pgStore, logger)
	logger.Info("pipeline advancer ready")

	// ── 8. Scheduler ──────────────────────────────────────────────────────────
	// Phase 5 change: scheduler.New() now accepts *pipeline.Advancer as the
	// fifth argument. The scheduler calls adv.AdvanceAll(ctx) on every
	// schedule ticker tick alongside scheduleQueuedJobs and promoteRetryableJobs.
	sched := scheduler.New(
		scheduler.Config{
			BatchSize:        cfg.Scheduler.BatchSize,
			ScheduleInterval: cfg.Scheduler.ScheduleInterval,
			OrphanInterval:   cfg.Scheduler.OrphanInterval,
		},
		db,      // *pgxpool.Pool for advisory lock queries
		pgStore, // store.Store for job state transitions + pipeline advancement
		queue,   // queue.Queue for XADD to Redis
		adv,     // *pipeline.Advancer for DAG advancement [Phase 5]
		logger,
	)

	// ── 9. Run ────────────────────────────────────────────────────────────────
	// scheduler.Run() blocks until ctx is cancelled.
	// Internally it contends for the PostgreSQL advisory lock and only
	// runs the dispatch loops when it becomes the leader.
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
