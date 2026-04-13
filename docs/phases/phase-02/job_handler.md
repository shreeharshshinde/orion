# `internal/api/handler/job.go`
## HTTP Handler — Phase 2 Updates

---

## What changed in Phase 2

Phase 1 had three handlers: `SubmitJob`, `GetJob`, `ListJobs`.

Phase 2 adds one: `GetExecutions` (GET `/jobs/{id}/executions`).

Phase 2 also completes the `SubmitJob` handler — in Phase 1 it called `h.store.CreateJob()` which panicked because `store` was nil. In Phase 2, the store is real.

---

## The four handlers

### `POST /jobs` — SubmitJob

**Full request body:**
```json
{
  "name": "train-resnet50",
  "type": "inline",
  "queue_name": "high",
  "priority": 8,
  "payload": {
    "handler_name": "train",
    "args": {
      "model": "resnet50",
      "epochs": 10,
      "dataset": "imagenet"
    }
  },
  "max_retries": 5,
  "idempotency_key": "train-run-2024-01-15-v3",
  "scheduled_at": null,
  "deadline": null
}
```

**Minimal valid request:**
```json
{
  "name": "test",
  "type": "inline",
  "payload": {"handler_name": "noop"}
}
```

**Responses:**
- `201 Created` — new job created
- `200 OK` — existing job returned (idempotency key matched)
- `400 Bad Request` — invalid JSON
- `422 Unprocessable Entity` — validation failed (missing name, wrong type, etc.)
- `500 Internal Server Error` — database error

**What validation checks:**
1. `name` must be non-empty
2. `type` must be `"inline"` or `"k8s_job"`
3. For `type: "inline"` → `payload.handler_name` must be non-empty
4. For `type: "k8s_job"` → `payload.kubernetes_spec` must be non-null

---

### `GET /jobs/{id}` — GetJob

**Request:**
```
GET /jobs/550e8400-e29b-41d4-a716-446655440000
```

**Response (200):**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "train-resnet50",
  "type": "inline",
  "queue_name": "high",
  "priority": 8,
  "status": "running",
  "attempt": 1,
  "max_retries": 5,
  "worker_id": "worker-prod-3",
  "started_at": "2024-01-15T10:05:22Z",
  "completed_at": null,
  "error_message": "",
  "created_at": "2024-01-15T10:00:00Z",
  "updated_at": "2024-01-15T10:05:22Z"
}
```

**Poll this endpoint to track job progress.** The `status` field moves through:
```
queued → scheduled → running → completed
                   ↘ failed → retrying → queued (retry loop)
                            ↘ dead
```

**Responses:**
- `200 OK` — job found
- `400 Bad Request` — ID is not a valid UUID
- `404 Not Found` — no job with this ID

---

### `GET /jobs` — ListJobs

**Query parameters:**
```
GET /jobs?status=running&queue=high&limit=20&offset=0
```

All parameters optional. Defaults: limit=50, no status filter, no queue filter.

**Response (200):**
```json
{
  "jobs": [
    {"id": "...", "status": "running", ...},
    {"id": "...", "status": "running", ...}
  ],
  "count": 2
}
```

Jobs are ordered by `priority DESC, created_at ASC` — highest priority first, oldest first within same priority.

---

### `GET /jobs/{id}/executions` — GetExecutions (new in Phase 2)

**Request:**
```
GET /jobs/550e8400-e29b-41d4-a716-446655440000/executions
```

**Response (200):**
```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "count": 3,
  "executions": [
    {
      "id": "...",
      "job_id": "...",
      "attempt": 1,
      "worker_id": "worker-prod-1",
      "status": "failed",
      "started_at": "2024-01-15T10:00:05Z",
      "finished_at": "2024-01-15T10:00:07Z",
      "exit_code": null,
      "logs_ref": "s3://orion-logs/jobs/550e8400/attempt-1.log",
      "error": "OOM killed: container exceeded 4Gi memory limit",
      "created_at": "2024-01-15T10:00:05Z"
    },
    {
      "id": "...",
      "attempt": 2,
      "worker_id": "worker-prod-2",
      "status": "failed",
      "error": "Timeout: exceeded 10 minute deadline",
      ...
    },
    {
      "id": "...",
      "attempt": 3,
      "worker_id": "worker-prod-3",
      "status": "completed",
      "error": "",
      ...
    }
  ]
}
```

**Why is this useful?**

When a job fails multiple times and eventually succeeds (or reaches `dead`), this endpoint gives you the complete forensic trail: which worker ran each attempt, what error occurred, how long it took. Critical for debugging ML training failures.

**The table is append-only.** Each row is immutable once written. 3 attempts = 3 rows forever.

**Responses:**
- `200 OK` — returns executions (may be empty `[]` if job hasn't run yet)
- `400 Bad Request` — ID is not a valid UUID
- `404 Not Found` — no job with this ID (distinct from "job exists but has no executions")

---

## Design principle: thin handlers

The handler only does three things:
1. Parse + validate the HTTP request
2. Call exactly one store method
3. Write the HTTP response

**No business logic.** The handler doesn't know about state machines, retries, Redis, or advisory locks. If a handler grows beyond ~50 lines, it's doing too much — extract to a service layer.

**No SQL.** The handler receives a `store.Store` interface. It has no idea PostgreSQL is involved. You can test handlers by passing a fake store:

```go
// In tests:
handler := NewJobHandler(fakeStore, logger)
```

---

## How `parseJobID` works

```go
func parseJobID(r *http.Request) (uuid.UUID, error) {
    return uuid.Parse(r.PathValue("id"))
}
```

`r.PathValue("id")` is Go 1.22's built-in path parameter extraction. Before Go 1.22, you needed a third-party router (gorilla/mux, chi) for this. Now it's in the standard library.

`uuid.Parse` validates that the string is a valid UUID (36 characters, correct format). If not, we return `400 Bad Request` before hitting the database.

---

## Route registration in main.go

```go
mux.HandleFunc("POST /jobs",                    jobHandler.SubmitJob)
mux.HandleFunc("GET /jobs",                     jobHandler.ListJobs)
mux.HandleFunc("GET /jobs/{id}",                jobHandler.GetJob)
mux.HandleFunc("GET /jobs/{id}/executions",     jobHandler.GetExecutions)

// Health probes
mux.HandleFunc("GET /healthz", ...)  // liveness
mux.HandleFunc("GET /readyz", ...)   // readiness (pings DB)
```

The `{id}` in the pattern is a wildcard. Go 1.22's router matches:
- `GET /jobs/550e8400-e29b-41d4-a716-446655440000` → `GetJob`, `id = "550e8400..."`
- `GET /jobs/550e8400-e29b-41d4-a716-446655440000/executions` → `GetExecutions`, `id = "550e8400..."`

More specific patterns take priority. `/jobs/{id}/executions` is more specific than `/jobs/{id}`.

---

## Testing the handler manually

```bash
# 1. Submit a job
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-job",
    "type": "inline",
    "payload": {"handler_name": "noop"},
    "idempotency_key": "test-001"
  }' | jq .

# 2. Get its status
JOB_ID="550e8400-..."   # from step 1
curl -s http://localhost:8080/jobs/$JOB_ID | jq .status

# 3. List all running jobs
curl -s "http://localhost:8080/jobs?status=running" | jq .count

# 4. Get execution history
curl -s http://localhost:8080/jobs/$JOB_ID/executions | jq .

# 5. Re-submit with same key (should return same job_id, HTTP 200)
curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"test-job","type":"inline","payload":{"handler_name":"noop"},"idempotency_key":"test-001"}' \
  | jq .id   # must match the ID from step 1
```