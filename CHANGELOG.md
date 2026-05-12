# Changelog

All notable changes to Orion are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- PG LISTEN/NOTIFY notifier (`internal/api/grpc/notifier.go`) drives the
  Broadcaster on job status changes, replacing the 500 ms poll ticker in
  `WatchJob`. Reduces DB load from O(clients × 120 qps) to near-zero.
- `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md` for open-source readiness.
- `make test-integration` now auto-spins Docker infrastructure and waits for
  Postgres + Redis health before running the integration test suite.

### Changed
- `WatchJob` poll ticker removed; broadcaster is now the sole event source.
- `WatchPipeline` poll interval unchanged (pipeline events are low-frequency).

---

## [0.1.0] — 2026-05-12

### Added
- Core job lifecycle: `queued → scheduled → running → completed/failed/dead`.
- PostgreSQL store with CAS state transitions and advisory-lock leader election.
- Redis Streams consumer groups for at-least-once job delivery across three
  priority queues (`high`, `default`, `low`) and a dead-letter stream.
- Bounded worker pool with `InlineExecutor` and `KubernetesExecutor` (client-go).
- Full-jitter exponential backoff retry with configurable cap and max attempts.
- Scheduler: dispatch loop, orphan reclaimer, retry promoter.
- gRPC `JobService`: `SubmitJob`, `GetJob`, `WatchJob`, `WatchPipeline`.
- HTTP REST API: job CRUD, idempotency keys, pipeline endpoints.
- DAG pipeline support with topological advancement and cascade-cancel.
- OpenTelemetry tracing (Jaeger), Prometheus metrics, structured `slog` logging.
- Grafana dashboards and Prometheus scrape config.
- Helm chart for Kubernetes deployment.
- `docker-compose.yml` for local development stack.
- Architecture Decision Records: ADR-001 (Redis Streams), ADR-002 (PG advisory locks).

[Unreleased]: https://github.com/shreeharshshinde/orion/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/shreeharshshinde/orion/releases/tag/v0.1.0
