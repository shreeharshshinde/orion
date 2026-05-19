package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
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

	// ── Prometheus registry + metrics [Phase 6] ───────────────────────────────
	// Scheduler reports: cycle latency, jobs_submitted, advancer cycle duration,
	// pipeline started/completed/failed, pipeline duration, pipeline nodes total.
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
		ctx, "orion-scheduler",
		cfg.Observability.ServiceVersion,
		cfg.Observability.OTLPEndpoint,
		cfg.Observability.TracingSampleRate,
	)
	if err != nil {
		logger.Warn("tracing setup failed, continuing without traces", "err", err)
	} else {
		defer func() {
			tCtx, tCancel := context.WithCancel(context.Background())
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
	logger.Info("connected to redis")

	// Phase 6: pass metrics to New() for queue depth polling
	queue, err := redisqueue.New(redisClient, metrics, logger)
	if err != nil {
		logger.Error("failed to initialize redis queue", "err", err)
		os.Exit(1)
	}
	defer queue.Close()

	// Start queue depth poller — updates QueueDepth gauge every 5s
	queue.StartQueueDepthPoller(ctx, []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"})

	// ── Pipeline Advancer [Phase 6 update] ───────────────────────────────────
	// Pass metrics so the advancer emits pipeline counters and histograms.
	adv := pipeline.NewAdvancer(pgStore, metrics, logger)
	logger.Info("pipeline advancer ready")

	// ── Phase 8: rate limiter + weighted fair queue allocations ──────────────
	// RateLimiter: one token bucket per queue, seeded from config.
	// queueAllocations: weight map used by ComputeAllocations each tick.
	rateLimiter := scheduler.NewQueueRateLimiter(map[string]scheduler.BucketConfig{
		"orion:queue:high":    {RatePerSec: cfg.Queue.High.RatePerSec, Burst: cfg.Queue.High.Burst},
		"orion:queue:default": {RatePerSec: cfg.Queue.Default.RatePerSec, Burst: cfg.Queue.Default.Burst},
		"orion:queue:low":     {RatePerSec: cfg.Queue.Low.RatePerSec, Burst: cfg.Queue.Low.Burst},
	})
	queueAllocations := map[string]scheduler.QueueAllocation{
		"orion:queue:high":    {QueueName: "orion:queue:high", Weight: cfg.Queue.High.Weight},
		"orion:queue:default": {QueueName: "orion:queue:default", Weight: cfg.Queue.Default.Weight},
		"orion:queue:low":     {QueueName: "orion:queue:low", Weight: cfg.Queue.Low.Weight},
	}

	// Publish initial concurrency limit and weight gauges so Grafana shows
	// configured values even before any jobs are dispatched.
	if metrics != nil {
		for name, alloc := range queueAllocations {
			metrics.QueueDispatchWeight.WithLabelValues(name).Set(alloc.Weight)
		}
		metrics.QueueConcurrencyLimit.WithLabelValues("orion:queue:high").Set(float64(cfg.Queue.High.MaxConcurrent))
		metrics.QueueConcurrencyLimit.WithLabelValues("orion:queue:default").Set(float64(cfg.Queue.Default.MaxConcurrent))
		metrics.QueueConcurrencyLimit.WithLabelValues("orion:queue:low").Set(float64(cfg.Queue.Low.MaxConcurrent))
	}

	// ── Scheduler [Phase 8 update] ────────────────────────────────────────────
	sched := scheduler.New(
		scheduler.Config{
			BatchSize:        cfg.Scheduler.BatchSize,
			ScheduleInterval: cfg.Scheduler.ScheduleInterval,
			OrphanInterval:   cfg.Scheduler.OrphanInterval,
		},
		db,
		pgStore,
		queue,
		adv,
		metrics,          // Phase 6
		rateLimiter,      // Phase 8
		queueAllocations, // Phase 8
		logger,
	)

	logger.Info("starting scheduler",
		"batch_size", cfg.Scheduler.BatchSize,
		"schedule_interval", cfg.Scheduler.ScheduleInterval,
	)

	if err := sched.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("scheduler exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("scheduler stopped cleanly")
}
