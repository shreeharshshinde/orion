# Orion — Makefile
# Usage: make <target>

.PHONY: help build test lint clean infra-up infra-down migrate proto

## ─── Project ────────────────────────────────────────────────────────────────

MODULE  := github.com/shreeharshshinde/orion
BUILD   := ./build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

## ─── Help ───────────────────────────────────────────────────────────────────

help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

## ─── Build ───────────────────────────────────────────────────────────────────

build: build-api build-scheduler build-worker ## Build all binaries

build-api: ## Build the API server
	@echo "Building orion-api..."
	@go build $(LDFLAGS) -o $(BUILD)/orion-api ./cmd/api

build-scheduler: ## Build the scheduler
	@echo "Building orion-scheduler..."
	@go build $(LDFLAGS) -o $(BUILD)/orion-scheduler ./cmd/scheduler

build-worker: ## Build the worker
	@echo "Building orion-worker..."
	@go build $(LDFLAGS) -o $(BUILD)/orion-worker ./cmd/worker

## ─── Test ────────────────────────────────────────────────────────────────────

test: ## Run all unit tests
	@go test ./... -v -race -timeout 120s

test-integration: ## Run integration tests (requires running infrastructure)
	@go test ./... -v -race -tags=integration -timeout 300s

test-coverage: ## Generate test coverage report
	@go test ./... -race -coverprofile=coverage.out -covermode=atomic
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## ─── Code Quality ────────────────────────────────────────────────────────────

lint: ## Run golangci-lint
	@golangci-lint run ./...

fmt: ## Format all Go code
	@gofmt -w .
	@goimports -w .

vet: ## Run go vet
	@go vet ./...

## ─── Infrastructure ──────────────────────────────────────────────────────────

infra-up: ## Start local infrastructure (Postgres, Redis, Jaeger, Prometheus)
	@docker compose up -d
	@echo "Waiting for services to be healthy..."
	@sleep 3
	@docker compose ps

infra-down: ## Stop local infrastructure
	@docker compose down

infra-logs: ## Tail all infrastructure logs
	@docker compose logs -f

## ─── Database ────────────────────────────────────────────────────────────────

MIGRATE_BIN := $(shell which migrate 2>/dev/null || echo "")
DB_DSN      ?= postgres://orion:orion@localhost:5432/orion?sslmode=disable

migrate-up: ## Apply all pending migrations
ifdef MIGRATE_BIN
	@migrate -database "$(DB_DSN)" -path ./internal/store/migrations up
else
	@echo "golang-migrate not found. Install: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"
	@exit 1
endif

migrate-down: ## Roll back the last migration
	@migrate -database "$(DB_DSN)" -path ./internal/store/migrations down 1

migrate-create: ## Create a new migration (usage: make migrate-create NAME=add_something)
	@migrate create -ext sql -dir ./internal/store/migrations -seq $(NAME)

## ─── Proto ───────────────────────────────────────────────────────────────────

# proto-gen: generate Go code from proto/orion/v1/jobs.proto using buf.
# Requires: go install github.com/bufbuild/buf/cmd/buf@latest
# Output:   proto/orion/v1/jobs.pb.go + jobs_grpc.pb.go
.PHONY: proto-gen
proto-gen: ## Generate gRPC Go code from .proto files (uses buf)
	@cd proto && buf generate
	@echo "Proto generated: proto/orion/v1/jobs.pb.go + jobs_grpc.pb.go"

# grpc-check: verify the gRPC server is reachable and lists the expected service.
# Requires: go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
.PHONY: grpc-check
grpc-check: ## Verify gRPC server is running (requires running API server)
	@grpcurl -plaintext localhost:9090 list
	@echo "Expected: orion.v1.JobService"

# Legacy alias kept for compatibility
proto: proto-gen ## Alias for proto-gen

## ─── Run (local dev) ─────────────────────────────────────────────────────────

run-api: ## Run the API server locally
	@ORION_ENV=development go run ./cmd/api

run-scheduler: ## Run the scheduler locally
	@ORION_ENV=development go run ./cmd/scheduler

run-worker: ## Run a worker locally
	@ORION_ENV=development go run ./cmd/worker

## ─── Docker ──────────────────────────────────────────────────────────────────

docker-build: ## Build all service Docker images
	@docker build -f deploy/docker/Dockerfile.api -t orion-api:$(VERSION) .
	@docker build -f deploy/docker/Dockerfile.scheduler -t orion-scheduler:$(VERSION) .
	@docker build -f deploy/docker/Dockerfile.worker -t orion-worker:$(VERSION) .

## ─── Utility ─────────────────────────────────────────────────────────────────

deps: ## Download and tidy Go dependencies
	@go mod download
	@go mod tidy

clean: ## Remove build artifacts
	@rm -rf $(BUILD)/ coverage.out coverage.html

check: fmt vet lint test ## Run all checks (format, vet, lint, test)