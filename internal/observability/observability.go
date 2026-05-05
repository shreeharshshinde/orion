package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// Metrics holds all Prometheus counters, histograms, and gauges for Orion.
//
// Phase 6 additions (marked below):
//   - Pipeline lifecycle metrics  (PipelineStarted, PipelineCompleted, ...)
//   - HTTP request metrics        (HTTPRequestsTotal, HTTPRequestDuration)
//   - Database operation metrics  (DBOperationDuration)
//   - Advancer cycle metric       (AdvancerCycleDuration)
//
// All metrics use the "orion_" namespace. Labels are kept low-cardinality:
// never use job.ID or pipeline.ID as a label — cardinality explosion will OOM Prometheus.
type Metrics struct {
	// ── Job lifecycle counters (Phases 1-5, unchanged) ────────────────────────
	JobsSubmitted *prometheus.CounterVec
	JobsCompleted *prometheus.CounterVec
	JobsFailed    *prometheus.CounterVec
	JobsRetried   *prometheus.CounterVec
	JobsDead      *prometheus.CounterVec

	// ── Execution timing (Phases 1-5, unchanged) ─────────────────────────────
	JobDuration *prometheus.HistogramVec

	// ── Queue depth (Phases 1-5, unchanged) ──────────────────────────────────
	QueueDepth *prometheus.GaugeVec

	// ── Worker utilization (Phases 1-5, unchanged) ───────────────────────────
	WorkerActiveJobs *prometheus.GaugeVec

	// ── Scheduler performance (Phases 1-5, unchanged) ────────────────────────
	SchedulerCycleLatency prometheus.Histogram

	// ── Pipeline metrics [Phase 6] ────────────────────────────────────────────
	// Labels use pipeline_name (low-cardinality) not pipeline_id (high-cardinality).
	// For production with many unique pipeline names, consider removing the name
	// label and grouping by status only.

	// PipelineStarted counts pipelines that transitioned pending → running.
	PipelineStarted *prometheus.CounterVec // labels: pipeline_name

	// PipelineCompleted counts pipelines where all nodes reached completed.
	PipelineCompleted *prometheus.CounterVec // labels: pipeline_name

	// PipelineFailed counts pipelines where a node reached dead status.
	PipelineFailed *prometheus.CounterVec // labels: pipeline_name

	// PipelineDuration measures wall-clock time from pipeline creation to terminal state.
	// Buckets cover the full ML workflow range: 10 seconds to 4 hours.
	PipelineDuration *prometheus.HistogramVec // labels: status (completed|failed)

	// PipelineNodesTotal counts pipeline node lifecycle events.
	// node_status values: "started" (job created), "completed", "dead"
	PipelineNodesTotal *prometheus.CounterVec // labels: pipeline_name, node_status

	// AdvancerCycleDuration measures how long one AdvanceAll() call takes.
	// Useful for detecting when the pipeline advancement is becoming a bottleneck.
	AdvancerCycleDuration prometheus.Histogram

	// ── HTTP metrics [Phase 6] ────────────────────────────────────────────────
	// Applied via MetricsMiddleware wrapping the entire mux — zero per-handler code.
	// Uses r.Pattern (Go 1.22) for route label — gives clean "GET /jobs/{id}"
	// instead of "/jobs/550e8400-..." which would explode cardinality.

	// HTTPRequestsTotal counts all HTTP requests by method, route, and response code.
	HTTPRequestsTotal *prometheus.CounterVec // labels: method, path, status_code

	// HTTPRequestDuration measures HTTP request latency by method and route.
	HTTPRequestDuration *prometheus.HistogramVec // labels: method, path

	// ── Database metrics [Phase 6] ────────────────────────────────────────────
	// DBOperationDuration measures latency of individual PostgreSQL operations.
	// The "operation" label uses the Go method name (e.g., "CreateJob", "MarkJobRunning")
	// making it easy to correlate with code and identify slow queries.
	DBOperationDuration *prometheus.HistogramVec // labels: operation

	// ── Phase 8 metrics ───────────────────────────────────────────────────────

	// QueueRateLimited counts jobs skipped by the scheduler due to token bucket exhaustion.
	// A rising rate here means a queue is being throttled — check rate_per_sec config.
	// PromQL: rate(orion_queue_rate_limited_total[1m]) by (queue)
	QueueRateLimited *prometheus.CounterVec // labels: queue

	// QueueConcurrentJobs tracks how many jobs from each queue are currently running.
	// Compare against QueueConcurrencyLimit to see utilisation.
	QueueConcurrentJobs *prometheus.GaugeVec // labels: queue

	// QueueConcurrencyLimit reflects the configured max_concurrent per queue.
	// Updated when queue_config is reloaded. Used for utilisation ratio panels.
	QueueConcurrencyLimit *prometheus.GaugeVec // labels: queue

	// QueueDispatchWeight reflects the configured dispatch weight per queue.
	// Useful for confirming live config changes took effect.
	QueueDispatchWeight *prometheus.GaugeVec // labels: queue
}

// NewMetrics creates and registers all Prometheus metrics with the given registerer.
// Panics if registration fails — always a startup-time programming error.
//
// Each binary creates its own Metrics instance with its own prometheus.NewRegistry().
// They do not share state — each process reports only the metrics it produces.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		// ── Existing metrics (Phases 1-5) ─────────────────────────────────────
		JobsSubmitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_submitted_total",
			Help:      "Total number of jobs dispatched to the Redis queue.",
		}, []string{"queue", "type"}),

		JobsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_completed_total",
			Help:      "Total number of jobs that completed successfully.",
		}, []string{"queue", "type"}),

		JobsFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_failed_total",
			Help:      "Total number of job execution failures.",
		}, []string{"queue", "type", "reason"}),

		JobsRetried: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_retried_total",
			Help:      "Total number of job retry attempts (re-enqueued after failure).",
		}, []string{"queue"}),

		JobsDead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_dead_total",
			Help:      "Total number of jobs moved to dead state (exhausted all retries).",
		}, []string{"queue"}),

		JobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "job_duration_seconds",
			Help:      "Histogram of job execution wall-clock durations.",
			// Buckets tuned for ML workloads: 100ms fast ops to 1h training runs.
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600, 1800, 3600},
		}, []string{"queue", "type", "status"}),

		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "queue_depth",
			Help:      "Current number of pending messages per Redis stream queue.",
		}, []string{"queue"}),

		WorkerActiveJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "worker_active_jobs",
			Help:      "Number of jobs currently executing on each worker instance.",
		}, []string{"worker_id"}),

		SchedulerCycleLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "scheduler_cycle_duration_seconds",
			Help:      "Duration of each scheduler dispatch cycle (scheduleQueuedJobs).",
			Buckets:   prometheus.DefBuckets,
		}),

		// ── Pipeline metrics [Phase 6] ─────────────────────────────────────────
		PipelineStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "pipelines_started_total",
			Help:      "Total pipelines transitioned from pending to running.",
		}, []string{"pipeline_name"}),

		PipelineCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "pipelines_completed_total",
			Help:      "Total pipelines where all nodes completed successfully.",
		}, []string{"pipeline_name"}),

		PipelineFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "pipelines_failed_total",
			Help:      "Total pipelines that failed due to a node reaching dead status.",
		}, []string{"pipeline_name"}),

		PipelineDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "pipeline_duration_seconds",
			Help:      "Histogram of total pipeline execution durations from creation to terminal state.",
			// Pipelines span 10 seconds (simple chained inline jobs) to 4+ hours (GPU training).
			Buckets: []float64{10, 30, 60, 300, 600, 1800, 3600, 7200, 14400},
		}, []string{"status"}),

		PipelineNodesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "pipeline_nodes_total",
			Help:      "Total pipeline node state transitions. node_status: started|completed|dead.",
		}, []string{"pipeline_name", "node_status"}),

		AdvancerCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "advancer_cycle_duration_seconds",
			Help:      "Duration of one AdvanceAll() call across all active pipelines.",
			Buckets:   prometheus.DefBuckets,
		}),

		// ── HTTP metrics [Phase 6] ─────────────────────────────────────────────
		HTTPRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "http_requests_total",
			Help:      "Total HTTP requests served, by method, route pattern, and status code.",
		}, []string{"method", "path", "status_code"}),

		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "http_request_duration_seconds",
			Help:      "Histogram of HTTP request latencies by method and route pattern.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method", "path"}),

		// ── DB metrics [Phase 6] ──────────────────────────────────────────────
		DBOperationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "db_operation_duration_seconds",
			Help:      "Histogram of PostgreSQL operation latencies by operation name.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"operation"}),

		// ── Phase 8 metrics ───────────────────────────────────────────────────
		QueueRateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "queue_rate_limited_total",
			Help:      "Total jobs skipped by the scheduler due to token bucket exhaustion.",
		}, []string{"queue"}),

		QueueConcurrentJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "queue_concurrent_jobs",
			Help:      "Current number of active (running) jobs per queue.",
		}, []string{"queue"}),

		QueueConcurrencyLimit: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "queue_concurrency_limit",
			Help:      "Configured max_concurrent slots per queue (from queue_config table).",
		}, []string{"queue"}),

		QueueDispatchWeight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "queue_dispatch_weight",
			Help:      "Configured dispatch weight per queue (0.0–1.0). Used by the fair scheduler.",
		}, []string{"queue"}),
	}

	// Register every metric. MustRegister panics on duplicate registration —
	// catch it immediately at startup rather than silently serving wrong data.
	reg.MustRegister(
		// existing
		m.JobsSubmitted,
		m.JobsCompleted,
		m.JobsFailed,
		m.JobsRetried,
		m.JobsDead,
		m.JobDuration,
		m.QueueDepth,
		m.WorkerActiveJobs,
		m.SchedulerCycleLatency,
		// Phase 6
		m.PipelineStarted,
		m.PipelineCompleted,
		m.PipelineFailed,
		m.PipelineDuration,
		m.PipelineNodesTotal,
		m.AdvancerCycleDuration,
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.DBOperationDuration,
		// Phase 8
		m.QueueRateLimited,
		m.QueueConcurrentJobs,
		m.QueueConcurrencyLimit,
		m.QueueDispatchWeight,
	)

	return m
}

// SetupTracing initializes OpenTelemetry tracing with a Jaeger OTLP exporter.
// Returns a shutdown function that must be called before process exit to flush spans.
func SetupTracing(ctx context.Context, serviceName, serviceVersion, otlpEndpoint string, sampleRate float64) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTel resource: %w", err)
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(), // use TLS in production
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(sampleRate)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Tracer returns a named tracer from the global OTel provider.
// The global provider is set by SetupTracing at startup.
// Any code in the same process can call Tracer() without passing a reference.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// NewLogger creates a structured slog.Logger with the given level and service name.
// JSON format in production/staging (for Loki, CloudWatch, etc).
// Text format in development (human-readable).
func NewLogger(level, serviceName, env string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: logLevel}
	var handler slog.Handler

	if env == "production" || env == "staging" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		"service", serviceName,
		"env", env,
	)
}

// MetricsServer starts a Prometheus /metrics HTTP endpoint on the given port.
// Also registers standard Go runtime and process metrics.
// Call this in a goroutine: go metricsSrv.ListenAndServe()
func MetricsServer(port int, reg *prometheus.Registry) *http.Server {
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
}
