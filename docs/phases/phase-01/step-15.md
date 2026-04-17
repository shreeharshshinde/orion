# Step 15 — Makefile
### File: `Makefile` (project root)

---

## What does this file do?

The Makefile is a **collection of shortcuts** for every common developer task. Instead of remembering long commands, you type `make <target>`:

```bash
make help          # show all available targets
make build         # compile all 3 binaries
make test          # run all tests with race detector
make lint          # static analysis
make infra-up      # start Docker Compose
make migrate-up    # apply database migrations
make run-api       # start the API server
```

`make` has been the standard build tool in Unix for 50 years. Go projects use it widely because Go's toolchain has many commands that you'd otherwise have to memorize.

---

## How Makefiles work

A Makefile is a list of **targets**:
```makefile
target-name: dependency1 dependency2  ## Description shown in make help
	command-to-run
	another-command
```

- The indentation **must be a tab** (not spaces) — this is a strict Makefile requirement
- Commands run left to right, top to bottom
- If any command fails (non-zero exit code), Make stops

`.PHONY` declares targets that aren't actually files:
```makefile
.PHONY: build test lint
```
Without this, Make would look for files named `build`, `test`, `lint` and skip the commands if those files exist.

---

## Variables

```makefile
BUILD   := ./build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"
DB_DSN  ?= postgres://orion:orion@localhost:5432/orion?sslmode=disable
```

- `:=` immediate assignment (evaluated once)
- `?=` default value (use this unless the variable is already set in the environment)
- `$(shell ...)` runs a shell command and uses its output as the value

`?=` lets you override from the command line:
```bash
# Use default DSN
make migrate-up

# Override DSN for production
DB_DSN="postgres://prod-user:secret@prod-db/orion" make migrate-up
```

---

## Build targets

### `make build`
```makefile
build: build-api build-scheduler build-worker
```
Runs `build-api`, `build-scheduler`, and `build-worker` in order.

### `make build-api`
```makefile
build-api:
	go build $(LDFLAGS) -o $(BUILD)/orion-api ./cmd/api
```

Compiles `cmd/api/main.go` and all its imports into one binary at `build/orion-api`.

`$(LDFLAGS)` injects the Git version string into the binary:
```
-ldflags "-X main.Version=v0.2.1-3-gf4a1b2c"
```

Now when you run `./build/orion-api --version` it can print the exact Git commit it was built from.

---

## Test targets

### `make test`
```makefile
test:
	go test ./... -v -race -timeout 120s
```

- `./...` — run tests in ALL packages recursively
- `-v` — verbose: show test names as they run
- `-race` — **enable the race detector** (critical)
- `-timeout 120s` — kill any test hanging longer than 2 minutes

**What is the race detector?**

Go's race detector instruments your code to detect when two goroutines read/write the same memory without synchronization. Race conditions are the hardest bugs to reproduce — they might only appear once in a thousand runs. The `-race` flag catches them reliably at test time.

```
WARNING: DATA RACE
Write at 0x00c000122508 by goroutine 7:
  main.handler(...)
Read at 0x00c000122508 by goroutine 9:
  main.worker(...)
```

Always run with `-race` during development. It has ~2x performance overhead (acceptable for tests).

### `make test-coverage`
```makefile
test-coverage:
	go test ./... -race -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html
```

Generates a color-coded HTML report showing which lines are covered by tests and which aren't. Open `coverage.html` in your browser.

---

## Code quality targets

### `make lint`
```makefile
lint:
	golangci-lint run ./...
```

Runs multiple static analysis tools simultaneously. Catches:
- Unused variables (compiler catches these, but lint also catches subtle ones)
- Ignored errors (dangerous in Go — you should always handle errors)
- Security issues (`gosec`)
- Style violations
- Shadowed variables
- Unnecessary type conversions

Install golangci-lint:
```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### `make check`
```makefile
check: fmt vet lint test
```

Runs ALL quality checks in order. Use this before committing code:
```bash
make check
# → formats code → go vet → lint → all tests
# → if any step fails, Make stops
```

---

## Infrastructure targets

### `make infra-up`
```makefile
infra-up:
	docker compose up -d
	sleep 3
	docker compose ps
```

Starts all services in background. The `sleep 3` gives services a moment to initialize before printing their status.

### `make infra-reset`
```makefile
infra-reset:
	docker compose down -v
```

**WARNING:** `-v` deletes all volumes (PostgreSQL data, Redis data). Use this only when you want a completely fresh database.

---

## Database migration targets

### `make migrate-up`
```makefile
DB_DSN ?= postgres://orion:orion@localhost:5432/orion?sslmode=disable

migrate-up:
	migrate -database "$(DB_DSN)" -path ./internal/store/migrations up
```

Applies all `.up.sql` files that haven't been applied yet. After running:
```bash
# Verify:
docker compose exec postgres psql -U orion -d orion -c "\dt"
# Should show: jobs, job_executions, workers, pipelines, pipeline_jobs
```

### `make migrate-create NAME=add_something`
```bash
make migrate-create NAME=add_tags_column
# Creates:
#   internal/store/migrations/002_add_tags_column.up.sql
#   internal/store/migrations/002_add_tags_column.down.sql
```

Creates a new migration file pair. Edit both files to add your SQL.

---

## Run targets (local development)

### `make run-api`
```makefile
run-api:
	ORION_ENV=development go run ./cmd/api
```

`go run` compiles and executes in one step — no binary file created. Faster iteration during development. For production, use `make build` then run the binary.

`ORION_ENV=development` sets the environment to development mode (text logs, not JSON).

---

## The `help` target

```makefile
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
```

This scans the Makefile for lines matching `target: ## Description` and prints them formatted. Run `make help` to see all targets with descriptions.

---

## Complete Phase 1 workflow

```bash
# ── First time setup ──────────────────────────────────────────────────
git clone https://github.com/shreeharshshinde/orion
cd orion
make deps              # go mod download && go mod tidy

# ── Start infrastructure ──────────────────────────────────────────────
make infra-up          # starts PostgreSQL, Redis, Jaeger, Prometheus, Grafana
# wait ~5 seconds for services to become healthy

# ── Database setup ─────────────────────────────────────────────────────
make migrate-up        # creates all 5 tables + indexes + triggers

# Verify:
docker compose exec postgres psql -U orion -d orion -c "\dt"

# ── Build and check ────────────────────────────────────────────────────
make build             # compiles all 3 binaries → build/orion-{api,scheduler,worker}
make vet               # quick sanity check
make test              # run all tests (limited in Phase 1, no DB implementation yet)

# ── Run locally (in 3 terminals) ──────────────────────────────────────
make run-api           # Terminal 1: HTTP server on :8080
make run-scheduler     # Terminal 2: Scheduler (contends for leader lock)
make run-worker        # Terminal 3: Worker pool (10 concurrent goroutines)

# ── Verify API is up ───────────────────────────────────────────────────
curl http://localhost:8080/healthz
# → {"status":"ok"}

# ── Open observability UIs ─────────────────────────────────────────────
open http://localhost:16686   # Jaeger traces
open http://localhost:9090    # Prometheus metrics
open http://localhost:3000    # Grafana dashboards (admin/admin)
```

---

## File location

```
orion/
└── Makefile   ← you are here (project root)
```