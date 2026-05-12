# Contributing to Orion

Thank you for your interest in contributing. This document covers everything
you need to get from zero to a merged pull request.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Local Setup](#local-setup)
3. [Code Style](#code-style)
4. [Testing](#testing)
5. [Submitting a Pull Request](#submitting-a-pull-request)
6. [Commit Convention](#commit-convention)
7. [Architecture Decisions](#architecture-decisions)

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.22+ | https://go.dev/dl |
| Docker + Compose | v2+ | https://docs.docker.com/get-docker |
| `golang-migrate` | latest | `go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest` |
| `golangci-lint` | v1.57+ | https://golangci-lint.run/usage/install |
| `buf` | latest | `go install github.com/bufbuild/buf/cmd/buf@latest` |

---

## Local Setup

```bash
git clone https://github.com/shreeharshshinde/orion.git
cd orion

# Start Postgres, Redis, Jaeger, Prometheus, Grafana
make infra-up

# Apply schema migrations
make migrate-up

# Run unit tests
make test

# Run integration tests (spins infra automatically)
make test-integration
```

---

## Code Style

- **Formatter**: `gofmt` + `goimports`. Run `make fmt` before committing.
- **Linter**: `golangci-lint`. Run `make lint`. CI will reject lint failures.
- **Error handling**: always wrap with `fmt.Errorf("context: %w", err)`. Never
  discard errors silently.
- **Logging**: use `log/slog` with structured key-value pairs. Include `job_id`
  and `worker_id` on every log line that touches a job.
- **No SQL outside `internal/store/postgres/`**. The store interface is the
  only boundary between business logic and the database.
- **No Redis client calls outside `internal/queue/`**.
- **Context propagation**: every function that does I/O must accept and forward
  `context.Context` as its first argument.
- **Exported symbols**: document every exported type, function, and method with
  a Go doc comment.

---

## Testing

### Unit tests

```bash
make test
```

- Live in `*_test.go` files alongside the code they test.
- Must not require any running infrastructure (use interfaces + fakes).
- Use the `-race` flag (already set in `make test`).

### Integration tests

```bash
make test-integration
```

- Tagged with `//go:build integration`.
- `make test-integration` starts Docker infrastructure automatically and waits
  for Postgres and Redis to pass their health checks before running.
- Integration tests live in `internal/store/postgres/` and `internal/queue/`.

### Coverage

```bash
make test-coverage   # generates coverage.html
```

Aim to keep coverage above 70% on new code. Coverage is not enforced by CI
but reviewers will ask about untested paths in critical packages.

---

## Submitting a Pull Request

1. **Fork** the repository and create a branch from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```

2. **Make your changes.** Keep each PR focused on a single concern.

3. **Run all checks** before pushing:
   ```bash
   make check   # fmt + vet + lint + test
   ```

4. **Update `CHANGELOG.md`** under `[Unreleased]` with a one-line summary of
   your change in the appropriate section (`Added`, `Changed`, `Fixed`,
   `Removed`).

5. **Open a PR** against `main`. Fill in the PR template:
   - What problem does this solve?
   - How was it tested?
   - Any follow-up work or known limitations?

6. **Address review feedback** by pushing new commits (do not force-push after
   review has started).

PRs that break `make check` or `make test-integration` will not be merged.

---

## Commit Convention

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

[optional body]
[optional footer]
```

Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`.

Examples:
```
feat(grpc): replace WatchJob poll ticker with PG LISTEN/NOTIFY
fix(scheduler): prevent double-dispatch on leader failover
docs(adr): add ADR-003 for PG LISTEN/NOTIFY decision
```

---

## Architecture Decisions

Significant design choices are recorded as Architecture Decision Records in
`docs/adr/`. If your PR introduces a new architectural pattern or changes an
existing one, add an ADR. Use the existing ADRs as a template.
