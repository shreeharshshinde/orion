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

// Metrics holds all Prometheus counters and histograms for Orion.
// All metrics are pre-registered with descriptive names and labels.
type Metrics struct {
	// Job lifecycle counters
	JobsSubmitted   *prometheus.CounterVec
	JobsCompleted   *prometheus.CounterVec
	JobsFailed      *prometheus.CounterVec
	JobsRetried     *prometheus.CounterVec
	JobsDead        *prometheus.CounterVec

	// Execution timing
	JobDuration     *prometheus.HistogramVec

	// Queue depth (gauge per queue)
	QueueDepth      *prometheus.GaugeVec

	// Worker utilization
	WorkerActiveJobs *prometheus.GaugeVec

	// Scheduler performance
	SchedulerCycleLatency prometheus.Histogram
}

// NewMetrics creates and registers all Prometheus metrics.
// Panics if registration fails (startup-time misconfiguration).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		JobsSubmitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_submitted_total",
			Help:      "Total number of jobs submitted to the system.",
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
			Help:      "Total number of job retry attempts.",
		}, []string{"queue"}),

		JobsDead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "orion",
			Name:      "jobs_dead_total",
			Help:      "Total number of jobs moved to the dead-letter queue.",
		}, []string{"queue"}),

		JobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "job_duration_seconds",
			Help:      "Histogram of job execution durations in seconds.",
			// Buckets tuned for ML workloads: from 100ms to 1 hour
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600, 1800, 3600},
		}, []string{"queue", "type", "status"}),

		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "queue_depth",
			Help:      "Current number of pending jobs per queue.",
		}, []string{"queue"}),

		WorkerActiveJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "orion",
			Name:      "worker_active_jobs",
			Help:      "Number of jobs currently being executed by each worker.",
		}, []string{"worker_id"}),

		SchedulerCycleLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "orion",
			Name:      "scheduler_cycle_duration_seconds",
			Help:      "Duration of each scheduler dispatch cycle.",
			Buckets:   prometheus.DefBuckets,
		}),
	}

	reg.MustRegister(
		m.JobsSubmitted,
		m.JobsCompleted,
		m.JobsFailed,
		m.JobsRetried,
		m.JobsDead,
		m.JobDuration,
		m.QueueDepth,
		m.WorkerActiveJobs,
		m.SchedulerCycleLatency,
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

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// NewLogger creates a structured slog.Logger with the given level and service name.
// In production, use JSON format for ingestion into log aggregators (Loki, CloudWatch, etc).
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
func MetricsServer(port int, reg *prometheus.Registry) *http.Server {
	// Register standard Go runtime metrics
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