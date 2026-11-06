# Phase 13 — Live Stack Smoke Test

> **Status:** Planned  
> **Priority:** Medium — validation gate for the full production hardening set  
> **Estimated effort:** 2–3 hours  
> **Depends on:** Ideally run after Authentication, Body Size Limit, and Log Shipping are implemented — but can run independently as a baseline first

---

## Table of Contents

1. [Problem statement](#1-problem-statement)
2. [What the smoke test covers](#2-what-the-smoke-test-covers)
3. [Difference from the existing integration test](#3-difference-from-the-existing-integration-test)
4. [Files to create or modify](#4-files-to-create-or-modify)
5. [Test structure](#5-test-structure)
6. [Makefile target](#6-makefile-target)
7. [Individual test cases](#7-individual-test-cases)
8. [Prometheus assertion](#8-prometheus-assertion)
9. [Jaeger assertion](#9-jaeger-assertion)
10. [Dashboard assertion](#10-dashboard-assertion)
11. [Running the smoke test](#11-running-the-smoke-test)
12. [CI integration](#12-ci-integration)
13. [Extending the smoke test](#13-extending-the-smoke-test)

---

## 1. Problem statement

Orion's existing test suite has two layers:

- **Unit tests** (`make check`): fast, in-process, mock all infrastructure.
  They test individual functions and components. They do not test that the
  scheduler, worker, Redis, PostgreSQL, and the API server cooperate correctly
  when all running at the same time.

- **Integration tests** (`make test-integration`): spin up real Postgres and
  Redis; wire the full stack in-process inside a single test binary. They cover
  the job lifecycle end-to-end but without the three separate binaries that
  production uses.

Neither layer verifies that `make infra-up && make run-api && make run-scheduler
&& make run-worker` actually produces a working system. Bugs that only appear
when binaries run as separate OS processes — misconfigured ports, missing env
vars in a real shell, CORS issues in the frontend — are invisible to both
layers.

The smoke test fills this gap. It runs against the **real Docker Compose stack
and the real binaries**, not an in-process test double.

---

## 2. What the smoke test covers

```
Smoke test assertions (in order):

1. Infrastructure health
   ├── PostgreSQL: pg_isready responds
   ├── Redis: PING returns PONG
   ├── Jaeger: /api/services returns 200
   └── Prometheus: /api/v1/targets returns all targets in "up" state

2. API server health
   ├── GET /healthz → 200 "ok"
   ├── GET /readyz → 200 "ready"
   └── GET /metrics → 200, body contains "orion_"

3. Job lifecycle (inline job)
   ├── POST /jobs → 201, returns job with id and status="queued"
   ├── Poll GET /jobs/{id} until status="completed" (timeout: 30s)
   └── GET /jobs/{id}/executions → at least 1 execution record

4. Job lifecycle (pipeline)
   ├── POST /pipelines → 201
   ├── Poll GET /pipelines/{id} until status="completed" (timeout: 60s)
   └── GET /pipelines/{id}/jobs → all nodes show status="completed"

5. Prometheus metrics
   └── Query orion_jobs_completed_total > 0

6. Jaeger traces
   └── GET /api/traces?service=orion-worker → at least 1 trace with
       a "worker.execute_job" span

7. Authentication (only if ORION_API_KEY is set)
   ├── Request without token → 401
   └── Request with token → 200
```

---

## 3. Difference from the existing integration test

| Dimension | Integration test (`tests/e2e/`) | Smoke test (new) |
|---|---|---|
| Infrastructure | Real Postgres + Redis, no Jaeger/Prometheus | Full Docker Compose stack including all observability services |
| Binaries | All components run in the same Go process | Three separate binaries (`orion-api`, `orion-scheduler`, `orion-worker`) |
| Scheduler | In-process goroutine | Real `orion-scheduler` binary listening on its ports |
| Metrics | Prometheus not running | Assertions against live `orion_jobs_completed_total` PromQL query |
| Traces | OpenTelemetry not configured | Assertions against live Jaeger `/api/traces` endpoint |
| Build tag | `//go:build integration` | `//go:build e2e` |
| Run command | `make test-integration` | `make smoke-test` |
| Speed | ~30 seconds | ~2–3 minutes (service startup + job execution) |

---

## 4. Files to create or modify

| File | Action | What changes |
|---|---|---|
| `tests/e2e/smoke_test.go` | **Create** | The full smoke test suite |
| `tests/e2e/helpers_test.go` | **Create** | Shared HTTP client, polling helpers, Prometheus query helper |
| `Makefile` | **Modify** | Add `smoke-test` target |
| `docs/LOCAL_EXECUTION_AND_END_PRODUCT.md` | **Modify** | Add "Running the smoke test" section |

---

## 5. Test structure

### Build tag

```go
//go:build e2e

// Package e2e contains end-to-end smoke tests that run against a live
// Docker Compose stack with all three Orion binaries running.
//
// Prerequisites:
//   make infra-up
//   make migrate-up
//   make run-api        # in background terminal
//   make run-scheduler  # in background terminal
//   make run-worker     # in background terminal
//
// Or use the convenience target:
//   make smoke-test
//
// This target handles startup and teardown automatically.
package e2e
```

### Config constants

All endpoints are configurable via environment variables with sensible
defaults that match the standard `make run-*` setup:

```go
// In tests/e2e/helpers_test.go

const (
    defaultAPIBase        = "http://localhost:8080"
    defaultPrometheusBase = "http://localhost:9090"
    defaultJaegerBase     = "http://localhost:16686"
)

func apiBase() string {
    if v := os.Getenv("SMOKE_API_BASE"); v != "" {
        return v
    }
    return defaultAPIBase
}

// apiKey returns the bearer token to use, or "" if auth is disabled.
func apiKey() string {
    return os.Getenv("ORION_API_KEY")
}

// doRequest sends an HTTP request with the optional bearer token.
// It adds the Authorization header only when apiKey() is non-empty.
func doRequest(t *testing.T, method, url string, body io.Reader) *http.Response {
    t.Helper()
    req, err := http.NewRequestWithContext(context.Background(), method, url, body)
    require.NoError(t, err)
    if body != nil {
        req.Header.Set("Content-Type", "application/json")
    }
    if k := apiKey(); k != "" {
        req.Header.Set("Authorization", "Bearer "+k)
    }
    resp, err := http.DefaultClient.Do(req)
    require.NoError(t, err)
    return resp
}

// pollUntil calls check every interval until it returns true or timeout elapses.
func pollUntil(t *testing.T, timeout, interval time.Duration, check func() bool) {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if check() {
            return
        }
        time.Sleep(interval)
    }
    t.Fatalf("condition not met within %s", timeout)
}
```

---

## 6. Makefile target

Add to `Makefile`:

```makefile
## ─── Smoke Test ──────────────────────────────────────────────────────────────

smoke-test: ## Run end-to-end smoke test against live Docker Compose stack
	@echo "=== Starting infrastructure ==="
	@docker compose up -d
	@echo "Waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U orion -q; do sleep 1; done
	@echo "Waiting for redis..."
	@until docker compose exec -T redis redis-cli ping | grep -q PONG; do sleep 1; done
	@$(MAKE) migrate-up
	@echo ""
	@echo "=== Building Orion binaries ==="
	@$(MAKE) build
	@echo ""
	@echo "=== Starting Orion services ==="
	@echo "Starting API server..."
	@ORION_ENV=development $(BUILD)/orion-api &
	@echo "Starting scheduler..."
	@ORION_ENV=development $(BUILD)/orion-scheduler &
	@echo "Starting worker..."
	@ORION_ENV=development $(BUILD)/orion-worker &
	@echo "Waiting 10 seconds for services to initialize..."
	@sleep 10
	@echo ""
	@echo "=== Running smoke tests ==="
	@go test ./tests/e2e/... -tags=e2e -run TestSmoke -v -timeout 180s; \
	  STATUS=$$?; \
	  pkill -f "$(BUILD)/orion-api" 2>/dev/null || true; \
	  pkill -f "$(BUILD)/orion-scheduler" 2>/dev/null || true; \
	  pkill -f "$(BUILD)/orion-worker" 2>/dev/null || true; \
	  exit $$STATUS
```

**Why build first instead of `go run ./cmd/...`**

`go run ./cmd/api &` compiles a temporary binary in `/tmp/go-build...` and
executes it as a child process. The process tree looks like:

```
make → go run (compiler) → /tmp/go-build.../exe/api (actual server)
```

When you `pkill -f "go run ./cmd/api"`, only the `go run` wrapper is killed.
The compiled child binary is orphaned and continues listening on `:8080`.
The next `smoke-test` run fails with "address already in use".

Building first with `make build` and running `./build/orion-api &` directly
means the process is the binary itself. `pkill -f "build/orion-api"` kills it
exactly, leaving no orphaned children. The `STATUS` variable preserves the
test exit code so `make smoke-test` correctly fails CI when tests fail.

---

## 7. Individual test cases

### `TestSmoke_InfrastructureHealth`

```go
func TestSmoke_InfrastructureHealth(t *testing.T) {
    t.Run("API healthz", func(t *testing.T) {
        resp := doRequest(t, "GET", apiBase()+"/healthz", nil)
        assert.Equal(t, 200, resp.StatusCode)
    })

    t.Run("API readyz", func(t *testing.T) {
        resp := doRequest(t, "GET", apiBase()+"/readyz", nil)
        assert.Equal(t, 200, resp.StatusCode)
    })

    t.Run("API metrics endpoint", func(t *testing.T) {
        resp := doRequest(t, "GET", apiBase()+"/metrics", nil)
        body, _ := io.ReadAll(resp.Body)
        assert.Contains(t, string(body), "orion_")
    })

    t.Run("Prometheus targets up", func(t *testing.T) {
        resp, err := http.Get(prometheusBase() + "/api/v1/targets")
        require.NoError(t, err)
        // Parse response, assert all targets have health="up"
        // ...
    })
}
```

### `TestSmoke_InlineJobLifecycle`

```go
func TestSmoke_InlineJobLifecycle(t *testing.T) {
    // 1. Submit job
    payload := `{
        "name": "smoke-inline-test",
        "type": "inline",
        "payload": {"handler_name": "noop"},
        "queue_name": "default"
    }`
    resp := doRequest(t, "POST", apiBase()+"/jobs",
        strings.NewReader(payload))
    require.Equal(t, 201, resp.StatusCode)

    var job struct {
        ID     string `json:"id"`
        Status string `json:"status"`
    }
    json.NewDecoder(resp.Body).Decode(&job)
    require.NotEmpty(t, job.ID, "response must contain job id")
    t.Logf("submitted job %s", job.ID)

    // 2. Poll until completed
    pollUntil(t, 30*time.Second, 500*time.Millisecond, func() bool {
        r := doRequest(t, "GET", apiBase()+"/jobs/"+job.ID, nil)
        var j struct{ Status string `json:"status"` }
        json.NewDecoder(r.Body).Decode(&j)
        t.Logf("job status: %s", j.Status)
        return j.Status == "completed"
    })

    // 3. Execution record exists
    r := doRequest(t, "GET", apiBase()+"/jobs/"+job.ID+"/executions", nil)
    var execs []struct{ ID string `json:"id"` }
    json.NewDecoder(r.Body).Decode(&execs)
    assert.GreaterOrEqual(t, len(execs), 1, "at least 1 execution record")

    t.Logf("job %s completed successfully", job.ID)
}
```

### `TestSmoke_PipelineLifecycle`

```go
func TestSmoke_PipelineLifecycle(t *testing.T) {
    payload := `{
        "name": "smoke-pipeline-test",
        "dag_spec": {
            "nodes": [
                {"id": "step-a", "job_template": {"name": "step-a", "type": "inline",
                 "payload": {"handler_name": "noop"}}},
                {"id": "step-b", "job_template": {"name": "step-b", "type": "inline",
                 "payload": {"handler_name": "noop"}}, "depends_on": ["step-a"]}
            ]
        }
    }`
    resp := doRequest(t, "POST", apiBase()+"/pipelines",
        strings.NewReader(payload))
    require.Equal(t, 201, resp.StatusCode)

    var p struct{ ID string `json:"id"` }
    json.NewDecoder(resp.Body).Decode(&p)
    require.NotEmpty(t, p.ID)

    pollUntil(t, 60*time.Second, time.Second, func() bool {
        r := doRequest(t, "GET", apiBase()+"/pipelines/"+p.ID, nil)
        var pip struct{ Status string `json:"status"` }
        json.NewDecoder(r.Body).Decode(&pip)
        t.Logf("pipeline status: %s", pip.Status)
        return pip.Status == "completed"
    })

    t.Logf("pipeline %s completed successfully", p.ID)
}
```

### `TestSmoke_Authentication` (runs only if `ORION_API_KEY` is set)

```go
func TestSmoke_Authentication(t *testing.T) {
    key := apiKey()
    if key == "" {
        t.Skip("ORION_API_KEY not set — skipping auth smoke test")
    }

    t.Run("request without token is rejected", func(t *testing.T) {
        req, _ := http.NewRequest("GET", apiBase()+"/jobs", nil)
        resp, err := http.DefaultClient.Do(req)
        require.NoError(t, err)
        assert.Equal(t, 401, resp.StatusCode)
    })

    t.Run("request with wrong token is rejected", func(t *testing.T) {
        req, _ := http.NewRequest("GET", apiBase()+"/jobs", nil)
        req.Header.Set("Authorization", "Bearer wrong-token")
        resp, err := http.DefaultClient.Do(req)
        require.NoError(t, err)
        assert.Equal(t, 401, resp.StatusCode)
    })

    t.Run("health endpoints bypass auth", func(t *testing.T) {
        resp, err := http.Get(apiBase() + "/healthz")
        require.NoError(t, err)
        assert.Equal(t, 200, resp.StatusCode)
    })
}
```

---

## 8. Prometheus assertion

```go
func TestSmoke_PrometheusMetrics(t *testing.T) {
    // Allow up to 15 seconds for metrics to propagate after the inline job completes.
    pollUntil(t, 15*time.Second, 2*time.Second, func() bool {
        url := prometheusBase() + "/api/v1/query?query=orion_jobs_completed_total"
        resp, err := http.Get(url)
        if err != nil {
            return false
        }
        var result struct {
            Data struct {
                Result []struct {
                    Value [2]any `json:"value"`
                } `json:"result"`
            } `json:"data"`
        }
        json.NewDecoder(resp.Body).Decode(&result)

        if len(result.Data.Result) == 0 {
            t.Log("no metric samples yet")
            return false
        }

        valueStr, _ := result.Data.Result[0].Value[1].(string)
        count, _ := strconv.ParseFloat(valueStr, 64)
        t.Logf("orion_jobs_completed_total = %v", count)
        return count > 0
    })
}
```

---

## 9. Jaeger assertion

```go
func TestSmoke_JaegerTraces(t *testing.T) {
    // Allow up to 20 seconds for spans to be flushed to Jaeger.
    // OTel SDK batches spans every 5 seconds by default.
    pollUntil(t, 20*time.Second, 2*time.Second, func() bool {
        url := jaegerBase() + "/api/traces?service=orion-worker&limit=5"
        resp, err := http.Get(url)
        if err != nil {
            return false
        }

        var result struct {
            Data []struct {
                Spans []struct {
                    OperationName string `json:"operationName"`
                } `json:"spans"`
            } `json:"data"`
        }
        json.NewDecoder(resp.Body).Decode(&result)

        for _, trace := range result.Data {
            for _, span := range trace.Spans {
                if span.OperationName == "worker.execute_job" {
                    t.Log("found worker.execute_job span in Jaeger")
                    return true
                }
            }
        }
        t.Logf("no worker.execute_job spans yet (%d traces found)", len(result.Data))
        return false
    })
}
```

---

## 10. Dashboard assertion

The Grafana dashboard assertion is marked `t.Skip` by default. It requires
either a headless browser (Playwright, Chromium) or a simpler approach of
asserting the dashboard JSON is provisioned via the Grafana API:

```go
func TestSmoke_GrafanaDashboard(t *testing.T) {
    grafanaBase := os.Getenv("SMOKE_GRAFANA_BASE")
    if grafanaBase == "" {
        grafanaBase = "http://localhost:3000"
    }

    // Check that the provisioned dashboard exists and has panels
    req, _ := http.NewRequest("GET",
        grafanaBase+"/api/dashboards/uid/orion-main", nil)
    req.SetBasicAuth("admin", "admin")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Skipf("Grafana not reachable: %v", err)
    }

    require.Equal(t, 200, resp.StatusCode)

    var dashboard struct {
        Dashboard struct {
            Panels []struct{ Title string } `json:"panels"`
        } `json:"dashboard"`
    }
    json.NewDecoder(resp.Body).Decode(&dashboard)
    assert.GreaterOrEqual(t, len(dashboard.Dashboard.Panels), 9,
        "dashboard should have at least 9 panels")
}
```

---

## 11. Running the smoke test

### Option A: fully automated (recommended)

```bash
# Starts infra, runs all three services, executes smoke tests, cleans up
make smoke-test
```

### Option B: manual (useful for debugging individual test cases)

```bash
# Terminal 1: start infrastructure
make infra-up && make migrate-up

# Terminal 2: build then start API server
make build
ORION_API_KEY=test-token ./build/orion-api

# Terminal 3: scheduler
ORION_API_KEY=test-token ./build/orion-scheduler

# Terminal 4: worker
ORION_API_KEY=test-token ./build/orion-worker

# Terminal 5: run smoke tests
ORION_API_KEY=test-token \
go test ./tests/e2e/... -tags=e2e -run TestSmoke -v -timeout 180s
```

### Option C: run a single test case

```bash
go test ./tests/e2e/... -tags=e2e -run TestSmoke_InlineJobLifecycle -v
```

### Expected output

```
=== RUN   TestSmoke_InfrastructureHealth
=== RUN   TestSmoke_InfrastructureHealth/API_healthz
=== RUN   TestSmoke_InfrastructureHealth/API_readyz
=== RUN   TestSmoke_InfrastructureHealth/API_metrics_endpoint
=== RUN   TestSmoke_InfrastructureHealth/Prometheus_targets_up
--- PASS: TestSmoke_InfrastructureHealth (0.21s)

=== RUN   TestSmoke_InlineJobLifecycle
    smoke_test.go: submitted job 550e8400-e29b-41d4-a716-446655440001
    smoke_test.go: job status: queued
    smoke_test.go: job status: scheduled
    smoke_test.go: job status: running
    smoke_test.go: job status: completed
    smoke_test.go: job 550e8400-e29b-41d4-a716-446655440001 completed successfully
--- PASS: TestSmoke_InlineJobLifecycle (4.5s)

=== RUN   TestSmoke_PipelineLifecycle
    smoke_test.go: pipeline status: pending
    smoke_test.go: pipeline status: running
    smoke_test.go: pipeline status: completed
--- PASS: TestSmoke_PipelineLifecycle (12.3s)

=== RUN   TestSmoke_PrometheusMetrics
    smoke_test.go: orion_jobs_completed_total = 1
--- PASS: TestSmoke_PrometheusMetrics (2.1s)

=== RUN   TestSmoke_JaegerTraces
    smoke_test.go: found worker.execute_job span in Jaeger
--- PASS: TestSmoke_JaegerTraces (6.4s)

=== RUN   TestSmoke_Authentication
--- SKIP: TestSmoke_Authentication (ORION_API_KEY not set — skipping)

PASS
ok  	github.com/shreeharshshinde/orion/tests/e2e	25.54s
```

---

## 12. CI integration

The smoke test is intentionally not run in `make check` (the fast unit test
suite). Add a separate CI job that runs on pull requests targeting `main`:

```yaml
# .github/workflows/smoke.yml (example GitHub Actions)
name: Smoke Test

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      - name: Start infrastructure
        run: docker compose up -d

      - name: Wait for infrastructure
        run: |
          until docker compose exec -T postgres pg_isready -U orion -q; do sleep 1; done
          until docker compose exec -T redis redis-cli ping | grep -q PONG; do sleep 1; done

      - name: Apply migrations
        run: make migrate-up

      - name: Build binaries
        run: make build

      - name: Start Orion services
        run: |
          ./build/orion-api &
          ./build/orion-scheduler &
          ./build/orion-worker &
          sleep 10

      - name: Run smoke tests
        run: go test ./tests/e2e/... -tags=e2e -run TestSmoke -v -timeout 180s
        env:
          ORION_API_KEY: ""  # auth disabled in CI for simplicity
```

---

## 13. Extending the smoke test

The smoke test is a regression harness. After each Phase 13 feature is
implemented, add a corresponding smoke test case:

| Feature (phase) | Smoke test to add |
|---|---|
| Authentication (13-A) | `TestSmoke_Authentication` — already planned above |
| Body size limit (13-B) | `TestSmoke_BodySizeLimit` — send 11 MiB body, assert 400 |
| Log shipping (13-C) | `TestSmoke_LogShipping` — assert `logs_ref` is non-empty in execution record |
| K8s executor | `TestSmoke_KubernetesJob` — skip unless `KUBECONFIG` is set; submit a k8s_job and poll to completion |
| Queue rate limiting | `TestSmoke_RateLimiting` — submit 100 jobs to `low` queue rapidly, assert `orion_queue_rate_limited_total > 0` |
| gRPC WatchJob | `TestSmoke_GRPCWatch` — open a `WatchJob` stream and assert events arrive in order |

Keep individual smoke test cases independent of each other. Each one should
set up its own job and assert against it — never assume another test case's
job is in a particular state.
