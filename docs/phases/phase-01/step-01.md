# Step 01 — Go Module Setup (`go.mod`)

## What is this file?

`go.mod` is the **blueprint of your project**. It tells Go:
- What your project is called (the module path)
- What version of Go to use
- What external packages your code depends on

Think of it like `package.json` in Node.js or `requirements.txt` in Python.

## Why `github.com/shreeharshshinde/orion`?

The module path is not just a name — it's how Go finds your code when other files import it.

When you write this in any `.go` file:
```go
import "github.com/shreeharshshinde/orion/internal/domain"
```

Go knows: *"this is inside my own project, look in the `internal/domain/` folder"*.

The `github.com/shreeharsh-a/orion` prefix doesn't mean Go downloads it from GitHub during development — it's just the **unique namespace** for your module. When you eventually publish it, that path becomes the real download URL.

## Why these dependencies?

| Package | What it does | Why we chose it |
|---|---|---|
| `pgx/v5` | Talks to PostgreSQL | Faster than standard `database/sql` — uses PostgreSQL's binary protocol, not text. Native Go types, no reflection magic. |
| `go-redis/v9` | Talks to Redis | Full support for Redis Streams (XADD, XREADGROUP, XAUTOCLAIM) which is exactly what our queue uses. |
| `k8s.io/client-go` | Talks to Kubernetes API | Official Go SDK for creating/watching Kubernetes Jobs. Used by our KubernetesExecutor in Phase 5. |
| `google.golang.org/grpc` | gRPC framework | For high-throughput streaming APIs (worker heartbeats, job streaming). Phase 7. |
| `go.opentelemetry.io/otel` | Distributed tracing | Industry-standard SDK. Sends trace spans to Jaeger so we can see exactly what happened to a job. |
| `prometheus/client_golang` | Metrics | Exposes counters/gauges/histograms at `/metrics` so Prometheus can scrape them. |
| `google/uuid` | UUID generation | Every job gets a UUID as its ID. UUIDs are globally unique without needing a database sequence. |

## How to use

After creating this file, run:
```bash
go mod tidy
```

This downloads all dependencies and creates `go.sum` (a lockfile with checksums for security).

## File location
```
orion/
└── go.mod   ← you are here
```

## Next step
With the module defined, we can write actual Go code. Next: the **domain types** — the core data structures that every other package will use.