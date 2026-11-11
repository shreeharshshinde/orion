---
title: "Engineering Reference & Playbook"
description: "Big-O algorithm complexity matrix, Go idioms and concurrency primitives, design patterns in Orion, testing strategy, and contributor mental models."
---

# Engineering Reference & System Playbook

## Algorithms & Data Structures Matrix

| Area | Algorithm / Data Structure | Source Location | Complexity | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| **Job FSM** | Map of allowed transitions | `internal/domain/job.go` | `O(1)` per check | Validate local transition intent |
| **DAG Readiness** | Dependency map + node scan | `internal/domain/pipeline.go` | `O(V + E)` | Find runnable nodes whose prerequisites are complete |
| **Cascade Cancel** | BFS over adjacency list | `internal/pipeline/advancement.go` | `O(V + E)` | Find downstream blocked nodes on failure |
| **Fair Queue** | Insertion sort by weight | `internal/scheduler/fairqueue.go` | `O(Q^2)` | Tiny-Q deterministic batch allocation |
| **Token Bucket** | Lazy timestamp refill | `internal/scheduler/ratelimiter.go` | `O(1)` | Rate-limit dispatch without background timers |
| **Retry Backoff** | Exponential + full/equal jitter | `pkg/retry/retry.go` | `O(1)` | Scatter retries and eliminate thundering herds |
| **Worker Pool** | Bounded channel + goroutines | `internal/worker/pool.go` | `O(1)` enqueue/dequeue | Concurrency control and memory backpressure |
| **Handler Registry**| RWMutex-protected map | `internal/worker/inline.go` | `O(1)` average lookup | Concurrent handler dispatch |
| **Broadcaster** | Nested map + buffered channels | `internal/api/grpc/broadcaster.go` | `O(S)` publish | Fan out job events to streaming clients |
| **Scheduled Jobs** | Redis Sorted Set (score = Unix) | `internal/queue/redis/redis_queue.go`| `O(log N)` add/remove | Delayed dispatch via atomic Lua pop |
| **DB Claiming** | `FOR UPDATE SKIP LOCKED` | `internal/store/postgres/db.go` | Index-bound | Concurrent work claiming |

---

## Software Design Patterns in Orion

| Pattern | Where | Why |
| :--- | :--- | :--- |
| **Hexagonal Boundary** | `store.Store`, `queue.Queue` | Isolates infrastructure (PostgreSQL, Redis) from business logic |
| **Active/Passive Leader** | Scheduler advisory lock | Guarantees single dispatcher with high-availability standby |
| **Optimistic Concurrency / CAS** | `TransitionJobState` in PostgreSQL | Avoids distributed locks across worker nodes |
| **Producer / Consumer** | Scheduler $\rightarrow$ Redis $\rightarrow$ Worker Pool | Decouples job dispatching from long-running execution |
| **Bounded Worker Pool** | Worker `jobCh` | Backpressure and stable memory utilization |
| **Strategy Pattern** | `worker.Executor` interface | Unifies inline Go handlers and Kubernetes batch pods |
| **Registry Pattern** | `inline.Registry` with `sync.RWMutex` | Dynamic named function dispatch |
| **Decorator Pattern** | `PanicRecover`, instrumented store | Injects safety and telemetry around core logic |
| **Observer / Pub-Sub** | Broadcaster, Redis cancel signaler | Streams state changes and cancellation events |
| **Repository Pattern** | `internal/store` package | Central persistence contract |
| **Reconciliation Loop** | Scheduler / advancer over DB state | Converges desired work state continuously |
| **Functional Options** | Transition and retry options | Optional behavior without telescoping method signatures |

---

## Go Language Idioms & Concurrency Primitives

| Concept | Where | Why | Tradeoff / Alternative |
| :--- | :--- | :--- | :--- |
| **Interfaces** | `store.Store`, `queue.Queue`, `worker.Executor`, `cancel.Signaler` | Decouple packages and enable fakes | Broad interfaces can burden test fakes |
| **Structs** | Domain, configs, handlers, executors | Explicit data modeling | Can drift from DB/proto schema without code generation |
| **Methods** | Most packages | Attach behavior to domain/services | Pointer/value receiver choices matter for mutability |
| **Functional Options** | Store transitions, retry options | Optional atomic fields and retry config | Dynamic behavior less explicit than explicit parameters |
| **Contexts** | All IO and execution paths | Cancellation, deadlines, shutdown | Must be honored by handlers/executors |
| **Goroutines** | Servers, worker pool, notifiers, pollers | Concurrent IO/execution | Require strict shutdown discipline |
| **Channels** | Worker `jobCh`, cancel pub/sub, broadcaster subscriber channels | Handoff and streaming | Blocking/dropped messages must be intentional |
| **Mutexes** | Rate limiter, registry, broadcaster, cancel map | Protect maps/shared state | Contention risk if held across slow IO |
| **Atomics** | Worker active count | Cheap concurrent metric state | Limited to simple primitive values |
| **Error Wrapping** | `fmt.Errorf(... %w ...)` | Preserve root causes | Callers must use `errors.Is`/`errors.As` |
| **Typed Errors** | `StoreError` | Stable error semantics | Codes must stay consistent across packages |
| **Panic / Recover** | `PanicRecover`, tests | Convert handler panic to job failure | Can mask programming defects if overused |
| **JSON Marshal / Unmarshal** | DB payloads, Redis payloads, HTTP | Flexible ML schema | Runtime validation only |
| **Generics** | gRPC server-streaming type from generated API | Type-safe stream signatures | Generated code complexity |
| **Reflection** | Not directly in authored code | Used by dependencies/protobuf/JSON | Avoided in domain logic for performance and clarity |

---

## Comprehensive Testing Strategy

Orion maintains broad test coverage across unit, integration, and failure recovery scenarios:

- Domain state helper tests
- Handler tests with fake stores
- PostgreSQL unit and integration tests
- Redis queue tests using `miniredis`
- Scheduler tests for dispatch, retry, fairness, and rate limiting
- Pipeline advancer tests with a mutex-protected fake store
- Worker inline/pool tests
- Kubernetes executor tests with `client-go` fake clients and watch reactors
- gRPC tests using real in-process gRPC servers and fake stores
- Observability tests for metrics, logger, and tracing setup
- End-to-end job lifecycle test

### Patterns
- **Fakes Over Mocks**: Reusable in-memory fakes preserve realistic behavior without brittle mock expectations.
- **Table-Driven Tests**: Clear test matrices for state transitions and edge cases.
- **Build-Tagged Integration Tests**: Integration tests require live infrastructure tags (`//go:build integration`).
- **Race Detector**: Ran via `make test` (`go test -race ./...`).

---

## Deployment & Security Baseline

### Docker Compose
`docker-compose.yml` runs PostgreSQL, Redis, Jaeger, Prometheus, and Grafana for local development. Redis is configured with append-only persistence (AOF) and a 256 MB `maxmemory allkeys-lru` policy.

### Helm Chart
The Helm chart deploys API, scheduler, worker, config, secret, RBAC, ingress, HPA, migration Job, network policies, and ServiceMonitor.

### Security Posture
- Non-root user/group (UID 10001).
- `RuntimeDefault` seccomp profile.
- `allowPrivilegeEscalation: false`.
- `readOnlyRootFilesystem: true`.
- Drop all Linux capabilities (`ALL`).
- Worker has ClusterRole only for jobs and read-only pod/log access.
- API and scheduler service accounts do not need job management permissions.

### RBAC Matrix
Workers are granted:
- `create`, `get`, `list`, `watch`, `delete` on `batch/jobs`
- `get`, `list`, `watch` on pods
- `get` on pods/log

They do not grant direct pod creation, secrets access, or job patch/update.

### Network Policy
Allows ingress from ingress and monitoring namespaces and egress to PostgreSQL, Redis, OTLP, Kubernetes API, and DNS.

---

## Security Considerations

### Good Practices Present
- Least-privilege Kubernetes RBAC for workers
- No direct pod creation permissions
- No secrets read/write permissions
- Non-root hardened pod security contexts
- Structured low-cardinality logs and metrics
- Argument key logging avoids printing values (secrets/PII protection)
- Read/write timeouts configured on HTTP server
- Graceful shutdown on SIGTERM
- NetworkPolicy support in Helm chart

### Areas to Harden
- API authentication and authorization (tenant tokens)
- Ingress TLS termination
- Redis and PostgreSQL production secrets management
- gRPC Kubernetes payload conversion completeness
- Queue name normalization at API edge
- Pipeline DAG cycle detection
- Redis stream trimming and retention policies

---

## Frontend Architecture

The frontend is a Next.js 15 / React 19 dashboard.

Structure:
- `app/page.tsx`: Landing page
- `app/dashboard/page.tsx`: System overview, incident cards, active metrics
- `app/dashboard/jobs/page.tsx`: Real-time job monitor and submission
- `app/dashboard/pipelines/page.tsx`: Interactive DAG visualizer
- `app/dashboard/queues/page.tsx`: Priority queue configuration and rate-limit tuning
- `app/dashboard/workers/page.tsx`: Worker pool telemetry
- `app/docs/page.tsx`: Documentation portal
- `components/app-shell.tsx`: Dashboard layout shell
- `components/status-badge.tsx`: Consistent status encoding

---

## Contributor Mental Model & Invariants

To contribute safely, keep these 10 invariants in mind:

1. **PostgreSQL state is canonical**: The database is the single source of truth.
2. **Redis messages are hints to execute scheduled work, not truth**: Duplicate or stale messages must be safely rejected.
3. **Every job state mutation must use CAS**: Use `TransitionJobState` to prevent concurrency races.
4. **Workers must tolerate stale/duplicate messages**: Delivery is at-least-once.
5. **Kubernetes must not retry independently of Orion**: Always set `backoffLimit: 0`.
6. **Long-running code must honor context cancellation**: Check `ctx.Done()`.
7. **Metrics labels must stay low-cardinality**: Never use Job IDs as labels.
8. **The scheduler can be replicated, but only one leader should dispatch**: Use advisory locks.
9. **Pipeline advancement must be idempotent across ticks and crashes**: Never duplicate jobs for a node.
10. **Queue backpressure should happen in Redis, not in unbounded worker memory**: Honor channel limits.

---

## File-By-File Concept Index

| File | Key Concepts Implemented |
| :--- | :--- |
| `cmd/api/main.go` | Process wiring, HTTP/gRPC servers, middleware, notifier, graceful shutdown |
| `cmd/scheduler/main.go` | Leader scheduler wiring, queue depth polling, advancer, rate limiter |
| `cmd/worker/main.go` | Worker wiring, handler registry, Kubernetes client, executor list |
| `internal/config/config.go` | Environment configuration, defaults, parsing, validation |
| `internal/domain/job.go` | Job FSM, payloads, retry/terminal helpers |
| `internal/domain/pipeline.go` | DAG representation and readiness algorithm |
| `internal/domain/worker.go` | Worker liveness/capacity helpers |
| `internal/store/store.go` | Interfaces, filters, typed errors, transition options |
| `internal/store/postgres/db.go` | SQL, CAS, idempotency, workers, executions, queue config |
| `internal/store/postgres/pipeline.go` | Pipeline SQL, JSONB, JOINs, idempotent linking |
| `internal/queue/queue.go` | Broker contract and queue names |
| `internal/queue/redis/redis_queue.go` | Redis Streams, PEL, XAUTOCLAIM, ZSET scheduling, Lua |
| `internal/scheduler/scheduler.go` | Advisory locks, scheduling loop, retry/orphan handling |
| `internal/scheduler/fairqueue.go` | Weighted fair batch allocation |
| `internal/scheduler/ratelimiter.go` | Token bucket with mutex and lazy refill |
| `internal/pipeline/advancement.go` | Stateless DAG advancement and cascade cancellation |
| `internal/worker/pool.go` | Bounded goroutine pool, cancellation registry, retries |
| `internal/worker/inline.go` | Handler registry, RWMutex, inline execution |
| `internal/worker/handlers/handlers.go` | Built-in example handlers and panic recovery |
| `internal/worker/k8s/spec.go` | Kubernetes Job spec construction |
| `internal/worker/k8s/executor.go` | client-go execution, watch/poll, cleanup |
| `internal/worker/cancel/cancel.go` | Redis pub/sub cancel signaling |
| `internal/api/handler/job.go` | REST job lifecycle API |
| `internal/api/handler/pipeline.go` | REST pipeline API and DAG validation |
| `internal/api/handler/queue.go` | Queue config and stats API |
| `internal/api/handler/worker.go` | Worker list API |
| `internal/api/handler/middleware.go` | HTTP metrics and tracing middleware |
| `internal/api/grpc/server.go` | gRPC service, streaming, proto/domain conversion |
| `internal/api/grpc/broadcaster.go` | In-memory fan-out hub |
| `internal/api/grpc/notifier.go` | PostgreSQL LISTEN/NOTIFY bridge |
| `internal/api/grpc/instrumented_store.go`| Decorator store event publisher |
| `internal/observability/observability.go` | Prometheus, OpenTelemetry, slog |
| `pkg/retry/retry.go` | Context-aware retry, full/equal jitter |
| `proto/orion/v1/jobs.proto` | Public gRPC schema |
| `internal/store/migrations/*.sql` | Durable schema and indexes |
| `deploy/helm/templates/*.yaml` | Kubernetes deployment/RBAC/security/autoscaling |
| `deploy/k8s/*.yaml` | Raw RBAC and migration job manifests |
| `deploy/prometheus/*.yml` | Scrape config and alert rules |
| `frontend/app`, `frontend/components` | Operational dashboard scaffolding |

---

## Known Gaps And Reimplementation Notes

If reimplementing Orion from scratch, implement in this order:

1. Domain job state machine and PostgreSQL schema.
2. Store interface and CAS transitions.
3. HTTP job submission/get/list.
4. Scheduler leader election and queued-job dispatch.
5. Redis Streams queue and worker pool.
6. Inline executor.
7. Retry/backoff and execution audit.
8. Worker heartbeat and orphan reclaim.
9. Kubernetes executor with `backoffLimit=0` and watch/poll.
10. Pipelines and DAG advancement.
11. gRPC watch streams with notification bridge.
12. Metrics, tracing, Helm hardening, frontend.

### Key Enhancements To Prioritize

- **DAG Cycle Detection**: Add full DFS cycle detection on submission.
- **Unique Constraint on `(pipeline_id, node_id)`**: Prevent potential duplicate node links in database schema.
- **Normalize Queue Names**: Normalize logical names (`"high"`) vs stream keys (`"orion:queue:high"`) at the API edge.
- **Complete gRPC Kubernetes Payload Conversion**: Map Kubernetes specs in gRPC submission.
- **Explicit Redis Stream Trimming**: Configure stream retention or MAXLEN policies.
- **API Authentication & Authorization**: Add tenant authentication tokens.
- **Explicit Trace Context Propagation**: Propagate trace context through Redis Stream metadata.
- **Clarify Execution Audit Semantics**: Unify start and terminal attempt records.
