package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the root configuration struct for Orion services.
// Values are loaded from environment variables with defaults.
// In production, use Kubernetes secrets/configmaps injected as env vars.
type Config struct {
	Service       ServiceConfig
	Database      DatabaseConfig
	Redis         RedisConfig
	Kubernetes    KubernetesConfig
	Observability ObservabilityConfig
	Scheduler     SchedulerConfig
	Worker        WorkerPoolConfig
	Queue         QueueConfig // Phase 8: per-queue rate limiting and fair scheduling
}

type ServiceConfig struct {
	Name        string
	Environment string // "development", "staging", "production"
	LogLevel    string
	HTTPPort    int
	GRPCPort    int
}

type DatabaseConfig struct {
	DSN             string // full postgres DSN
	MaxConns        int32
	MinConns        int32
	MaxConnIdleTime time.Duration
	MaxConnLifetime time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

type KubernetesConfig struct {
	InCluster       bool   // use in-cluster config when running inside K8s
	KubeconfigPath  string // path to kubeconfig for local dev
	DefaultNamespace string
}

type ObservabilityConfig struct {
	MetricsPort     int
	OTLPEndpoint    string // e.g., "http://jaeger:4317"
	TracingSampleRate float64
	ServiceVersion  string
}

type SchedulerConfig struct {
	BatchSize        int
	ScheduleInterval time.Duration
	OrphanInterval   time.Duration
}

type WorkerPoolConfig struct {
	WorkerID          string
	Concurrency       int
	Queues            []string
	VisibilityTimeout time.Duration
	HeartbeatInterval time.Duration
	ShutdownTimeout   time.Duration
}

// QueueLimitConfig holds rate limiting and concurrency settings for one queue.
// All values are loaded from environment variables with safe defaults.
// Dynamic overrides come from the queue_config PostgreSQL table (Phase 8).
type QueueLimitConfig struct {
	// MaxConcurrent is the maximum number of worker slots this queue can occupy.
	// When this many jobs from this queue are running, no more are dispatched.
	MaxConcurrent int

	// Weight is the fraction of each scheduler batch allocated to this queue.
	// Used by the weighted fair scheduler. Weights are relative proportions —
	// they do not need to sum to 1.0.
	Weight float64

	// RatePerSec is the token bucket refill rate (jobs per second).
	// Controls how fast jobs can be enqueued, independently of concurrency.
	RatePerSec float64

	// Burst is the maximum token accumulation in the token bucket.
	// A queue with Burst=20 can dispatch 20 jobs instantly before rate limiting kicks in.
	Burst int
}

// QueueConfig holds per-queue limits for all three standard Orion queues.
// Loaded from ORION_QUEUE_* environment variables; overridable at runtime
// via PUT /queues/{name} without a service restart.
type QueueConfig struct {
	High    QueueLimitConfig
	Default QueueLimitConfig
	Low     QueueLimitConfig
}

// Load reads configuration from environment variables.
// All ORION_ prefixed env vars are recognized.
func Load() (*Config, error) {
	cfg := &Config{
		Service: ServiceConfig{
			Name:        getEnv("ORION_SERVICE_NAME", "orion"),
			Environment: getEnv("ORION_ENV", "development"),
			LogLevel:    getEnv("ORION_LOG_LEVEL", "info"),
			HTTPPort:    getEnvInt("ORION_HTTP_PORT", 8080),
			GRPCPort:    getEnvInt("ORION_GRPC_PORT", 9090),
		},
		Database: DatabaseConfig{
			DSN:             getEnv("ORION_DATABASE_DSN", "postgres://orion:orion@localhost:5432/orion?sslmode=disable"),
			MaxConns:        int32(getEnvInt("ORION_DB_MAX_CONNS", 20)),
			MinConns:        int32(getEnvInt("ORION_DB_MIN_CONNS", 2)),
			MaxConnIdleTime: getEnvDuration("ORION_DB_MAX_CONN_IDLE", 10*time.Minute),
			MaxConnLifetime: getEnvDuration("ORION_DB_MAX_CONN_LIFETIME", 60*time.Minute),
		},
		Redis: RedisConfig{
			Addr:     getEnv("ORION_REDIS_ADDR", "localhost:6379"),
			Password: getEnv("ORION_REDIS_PASSWORD", ""),
			DB:       getEnvInt("ORION_REDIS_DB", 0),
			PoolSize: getEnvInt("ORION_REDIS_POOL_SIZE", 10),
		},
		Kubernetes: KubernetesConfig{
			InCluster:        getEnvBool("ORION_K8S_IN_CLUSTER", false),
			KubeconfigPath:   getEnv("KUBECONFIG", "~/.kube/config"),
			DefaultNamespace: getEnv("ORION_K8S_NAMESPACE", "orion-jobs"),
		},
		Observability: ObservabilityConfig{
			MetricsPort:       getEnvInt("ORION_METRICS_PORT", 9091),
			OTLPEndpoint:      getEnv("ORION_OTLP_ENDPOINT", "http://localhost:4317"),
			TracingSampleRate: getEnvFloat("ORION_TRACING_SAMPLE_RATE", 1.0),
			ServiceVersion:    getEnv("ORION_SERVICE_VERSION", "dev"),
		},
		Scheduler: SchedulerConfig{
			BatchSize:        getEnvInt("ORION_SCHEDULER_BATCH_SIZE", 50),
			ScheduleInterval: getEnvDuration("ORION_SCHEDULER_INTERVAL", 2*time.Second),
			OrphanInterval:   getEnvDuration("ORION_SCHEDULER_ORPHAN_INTERVAL", 30*time.Second),
		},
		Worker: WorkerPoolConfig{
			WorkerID:          getEnv("ORION_WORKER_ID", mustHostname()),
			Concurrency:       getEnvInt("ORION_WORKER_CONCURRENCY", 10),
			VisibilityTimeout: getEnvDuration("ORION_WORKER_VISIBILITY_TIMEOUT", 5*time.Minute),
			HeartbeatInterval: getEnvDuration("ORION_WORKER_HEARTBEAT_INTERVAL", 15*time.Second),
			ShutdownTimeout:   getEnvDuration("ORION_WORKER_SHUTDOWN_TIMEOUT", 30*time.Second),
		},
	}

	// Phase 8: queue limits default to fractions of worker concurrency.
	// Computed after cfg is built so we can reference cfg.Worker.Concurrency.
	c := cfg.Worker.Concurrency
	cfg.Queue = QueueConfig{
		High: QueueLimitConfig{
			MaxConcurrent: getEnvInt("ORION_QUEUE_HIGH_MAX_CONCURRENT", c*8/10),
			Weight:        getEnvFloat("ORION_QUEUE_HIGH_WEIGHT", 0.8),
			RatePerSec:    getEnvFloat("ORION_QUEUE_HIGH_RATE_PER_SEC", 100.0),
			Burst:         getEnvInt("ORION_QUEUE_HIGH_BURST", 20),
		},
		Default: QueueLimitConfig{
			MaxConcurrent: getEnvInt("ORION_QUEUE_DEFAULT_MAX_CONCURRENT", c*6/10),
			Weight:        getEnvFloat("ORION_QUEUE_DEFAULT_WEIGHT", 0.6),
			RatePerSec:    getEnvFloat("ORION_QUEUE_DEFAULT_RATE_PER_SEC", 50.0),
			Burst:         getEnvInt("ORION_QUEUE_DEFAULT_BURST", 10),
		},
		Low: QueueLimitConfig{
			MaxConcurrent: getEnvInt("ORION_QUEUE_LOW_MAX_CONCURRENT", c*2/10),
			Weight:        getEnvFloat("ORION_QUEUE_LOW_WEIGHT", 0.2),
			RatePerSec:    getEnvFloat("ORION_QUEUE_LOW_RATE_PER_SEC", 10.0),
			Burst:         getEnvInt("ORION_QUEUE_LOW_BURST", 5),
		},
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Database.DSN == "" {
		return fmt.Errorf("ORION_DATABASE_DSN is required")
	}
	if c.Worker.Concurrency < 1 || c.Worker.Concurrency > 1000 {
		return fmt.Errorf("ORION_WORKER_CONCURRENCY must be between 1 and 1000")
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func mustHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown-worker"
	}
	return h
}