# Step 03 — Configuration (`internal/config/config.go`)

## What is this file?

This file is the **single place** where all configuration for Orion is defined and loaded. Instead of hardcoding values like `"localhost:5432"` throughout the codebase, every service reads from this one structured config.

## Why environment variables?

Configuration comes from environment variables (like `ORION_DATABASE_DSN`) rather than config files. This is the **12-Factor App** principle — it makes the app work the same way locally, in Docker, and in Kubernetes without changing code.

```bash
# Local dev — use defaults, no env vars needed
go run cmd/api/main.go

# Production Kubernetes — set via Secret/ConfigMap
ORION_DATABASE_DSN=postgres://prod-user:secret@prod-db:5432/orion
ORION_REDIS_ADDR=redis-cluster:6379
ORION_ENV=production
```

## Structure breakdown

The config is grouped into logical sections:

### ServiceConfig
```go
type ServiceConfig struct {
    Name        string  // "orion-api", "orion-worker", etc.
    Environment string  // "development" / "staging" / "production"
    LogLevel    string  // "debug" / "info" / "warn" / "error"
    HTTPPort    int     // 8080
    GRPCPort    int     // 9090
}
```
Controls which service this is and how it logs.

### DatabaseConfig
```go
type DatabaseConfig struct {
    DSN             string        // "postgres://user:pass@host:5432/db"
    MaxConns        int32         // max connections in pool (default: 20)
    MinConns        int32         // keep at least this many open (default: 2)
    MaxConnIdleTime time.Duration // close idle connections after 10min
    MaxConnLifetime time.Duration // recycle connections every 60min
}
```

**Why connection pooling settings?**

Opening a PostgreSQL connection is expensive (~10ms). A connection pool keeps connections open and reuses them. `MaxConns=20` means at most 20 simultaneous DB operations — the 21st waits for a slot. `MinConns=2` means even if nothing is happening, 2 connections stay warm and ready.

### RedisConfig
```go
type RedisConfig struct {
    Addr     string // "localhost:6379"
    Password string // empty for local dev
    DB       int    // Redis database number (0-15), use 0
    PoolSize int    // connection pool size (default: 10)
}
```

### KubernetesConfig
```go
type KubernetesConfig struct {
    InCluster        bool   // true when running inside a K8s pod
    KubeconfigPath   string // "~/.kube/config" for local dev
    DefaultNamespace string // "orion-jobs" — where to create K8s Jobs
}
```

When `InCluster=true`, the K8s client automatically reads the service account token mounted inside every Kubernetes pod. When `false`, it reads your local `~/.kube/config` file.

### WorkerPoolConfig
```go
type WorkerPoolConfig struct {
    WorkerID          string        // unique ID for this worker (defaults to hostname)
    Concurrency       int           // how many jobs to run at once (default: 10)
    Queues            []string      // which queues to consume from
    VisibilityTimeout time.Duration // 5min — how long before Redis re-delivers an un-acked job
    HeartbeatInterval time.Duration // 15s — how often to tell the scheduler "I'm alive"
    ShutdownTimeout   time.Duration // 30s — how long to wait for in-flight jobs on SIGTERM
}
```

**VisibilityTimeout explained:**

When a worker dequeues a job from Redis, that job becomes "invisible" to other workers for `VisibilityTimeout` (5 minutes). If the worker crashes before it can ACK the job, Redis automatically makes it visible again after 5 minutes, so another worker can pick it up. This is how at-least-once delivery works.

## How `Load()` works

```go
cfg, err := config.Load()
```

1. Reads each environment variable
2. Falls back to a sensible default if the variable isn't set
3. Validates the result (e.g., `Concurrency` must be 1-1000)
4. Returns the complete `*Config` struct

The helper functions are straightforward:
```go
getEnv("ORION_ENV", "development")           // string with fallback
getEnvInt("ORION_HTTP_PORT", 8080)           // int with fallback
getEnvBool("ORION_K8S_IN_CLUSTER", false)    // bool with fallback
getEnvDuration("ORION_WORKER_HEARTBEAT", "15s") // time.Duration with fallback
```

## All environment variables

| Variable | Default | Description |
|---|---|---|
| `ORION_DATABASE_DSN` | `postgres://orion:orion@localhost:5432/orion` | PostgreSQL connection |
| `ORION_REDIS_ADDR` | `localhost:6379` | Redis address |
| `ORION_WORKER_CONCURRENCY` | `10` | Parallel jobs per worker |
| `ORION_WORKER_VISIBILITY_TIMEOUT` | `5m` | Redis message visibility |
| `ORION_WORKER_HEARTBEAT_INTERVAL` | `15s` | Worker heartbeat frequency |
| `ORION_SCHEDULER_BATCH_SIZE` | `50` | Jobs dispatched per cycle |
| `ORION_SCHEDULER_INTERVAL` | `2s` | How often scheduler runs |
| `ORION_OTLP_ENDPOINT` | `http://localhost:4317` | Jaeger trace collector |
| `ORION_ENV` | `development` | Environment name |
| `ORION_LOG_LEVEL` | `info` | Log verbosity |

## File location
```
orion/
└── internal/
    └── config/
        └── config.go   ← you are here
```

## Next step
Config tells us WHERE things are. Now we define WHAT we can do with our data. Next: the **Store interface** — the contract for all database operations.