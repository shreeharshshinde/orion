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

GO_PKGS := $(shell go list ./... | grep -v '/node_modules/')

test: ## Run all unit tests
	@go test $(GO_PKGS) -v -race -timeout 120s

test-integration: ## Run integration tests (auto-spins Docker infra, waits for health)
	@echo "Starting infrastructure for integration tests..."
	@docker compose up -d postgres redis
	@echo "Waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U orion -q; do sleep 1; done
	@echo "Waiting for redis..."
	@until docker compose exec -T redis redis-cli ping | grep -q PONG; do sleep 1; done
	@echo "Infrastructure ready. Running integration tests..."
	@go test $(GO_PKGS) -v -race -tags=integration -timeout 300s
	@echo "Integration tests complete."

test-coverage: ## Generate test coverage report
	@go test $(GO_PKGS) -race -coverprofile=coverage.out -covermode=atomic
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

REGISTRY ?= ghcr.io/shreeharshshinde/orion

docker-build: ## Build all service Docker images
	@docker build -f deploy/docker/Dockerfile.api --build-arg VERSION=$(VERSION) -t orion-api:$(VERSION) .
	@docker build -f deploy/docker/Dockerfile.scheduler --build-arg VERSION=$(VERSION) -t orion-scheduler:$(VERSION) .
	@docker build -f deploy/docker/Dockerfile.worker --build-arg VERSION=$(VERSION) -t orion-worker:$(VERSION) .

docker-push: docker-build ## Build and push all images to registry
	@docker tag orion-api:$(VERSION) $(REGISTRY)/orion-api:$(VERSION)
	@docker tag orion-scheduler:$(VERSION) $(REGISTRY)/orion-scheduler:$(VERSION)
	@docker tag orion-worker:$(VERSION) $(REGISTRY)/orion-worker:$(VERSION)
	@docker push $(REGISTRY)/orion-api:$(VERSION)
	@docker push $(REGISTRY)/orion-scheduler:$(VERSION)
	@docker push $(REGISTRY)/orion-worker:$(VERSION)
	@echo "Pushed $(REGISTRY)/*:$(VERSION)"

## ─── Helm ────────────────────────────────────────────────────────────────────

HELM_CHART   := ./deploy/helm
HELM_RELEASE ?= orion
HELM_NS      ?= ml-platform

helm-lint: ## Lint the Helm chart
	@helm lint $(HELM_CHART)

helm-template: ## Render Helm templates to stdout (dry-run)
	@helm template $(HELM_RELEASE) $(HELM_CHART) \
		--set database.dsn="postgres://orion:orion@postgres:5432/orion" \
		--namespace $(HELM_NS)

helm-install: ## Install Orion via Helm (requires DB_DSN env var)
	@helm install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NS) \
		--create-namespace \
		--set database.dsn="$(DB_DSN)" \
		--set redis.addr="$(REDIS_ADDR)"

helm-upgrade: ## Upgrade an existing Helm release
	@helm upgrade $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NS) \
		--reuse-values \
		--set api.image.tag=$(VERSION) \
		--set scheduler.image.tag=$(VERSION) \
		--set worker.image.tag=$(VERSION)

helm-uninstall: ## Uninstall the Helm release
	@helm uninstall $(HELM_RELEASE) --namespace $(HELM_NS)

helm-status: ## Show Helm release status
	@helm status $(HELM_RELEASE) --namespace $(HELM_NS)

## ─── Utility ─────────────────────────────────────────────────────────────────

deps: ## Download and tidy Go dependencies
	@go mod download
	@go mod tidy

clean: ## Remove build artifacts
	@rm -rf $(BUILD)/ coverage.out coverage.html

check: fmt vet lint test ## Run all checks (format, vet, lint, test)