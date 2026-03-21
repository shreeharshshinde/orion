# Step 09 — API Handler (`internal/api/handler/job.go`)

## What is this file?

This is the **HTTP layer** — the code that handles incoming HTTP requests from clients (CLI, SDK, other services, CI/CD pipelines).

It does three things:
1. Parse and validate the incoming request
2. Call the store to read/write data
3. Return a proper HTTP response

**Important:** The handler is deliberately thin. It doesn't contain business logic. All the real work happens in the store (PostgreSQL) and queue (Redis).

## The three endpoints

### `POST /jobs` — Submit a job

```
Client                    API Handler               PostgreSQL
  |                           |                          |
  |-- POST /jobs ------------>|                          |
  |   {name, type, payload,   |                          |
  |    idempotency_key}       |                          |
  |                           |-- SELECT WHERE           |
  |                           |   idempotency_key=? ---->|
  |                           |<-- null (new job) -------|
  |                           |                          |
  |                           |-- INSERT job ----------->|
  |                           |<-- job created ----------|
  |                           |                          |
  |<-- 201 {job_id, status} --|                          |
```

**Idempotency key** is the most important feature here. If the client sends:
```json
{"name": "train-v2", "idempotency_key": "train-run-2024-01-15-v2"}
```

And then the network drops and the client retries with the same `idempotency_key`, the handler returns the EXISTING job instead of creating a duplicate. This means:
- Safe to retry on network errors
- No double-training, no double-billing
- Client always gets a consistent response

### `GET /jobs/{id}` — Get one job

Returns the current state of a specific job. Clients poll this to track progress:
```
GET /jobs/550e8400-e29b-41d4-a716-446655440000
→ {"id": "550e8400...", "status": "running", "attempt": 1, ...}
```

### `GET /jobs` — List jobs

Returns multiple jobs, filterable by status and queue:
```
GET /jobs?status=running&queue=high&limit=20
→ [{"id": "...", "status": "running"}, ...]
```

## JobHandler struct

```go
type JobHandler struct {
    store  store.Store    // talks to PostgreSQL
    logger *slog.Logger   // structured logging
}
```

Notice: the handler takes `store.Store` (the interface), not `*postgres.DB` (the implementation). This means:
- In production: pass the real PostgreSQL store
- In tests: pass a fake store that returns whatever you need

## Request/Response types

```go
// What the client sends
type SubmitJobRequest struct {
    Name           string            `json:"name"`
    Type           domain.JobType    `json:"type"`           // "inline" or "k8s_job"
    QueueName      string            `json:"queue_name"`     // optional, defaults to "default"
    Priority       domain.JobPriority `json:"priority"`      // 1-10, defaults to 5
    Payload        domain.JobPayload  `json:"payload"`       // what to run
    MaxRetries     int               `json:"max_retries"`    // defaults to 3
    IdempotencyKey string            `json:"idempotency_key"` // optional, for safe retries
    ScheduledAt    *time.Time        `json:"scheduled_at"`   // nil = run immediately
    Deadline       *time.Time        `json:"deadline"`       // nil = no deadline
}
```

## Validation

```go
func (r *SubmitJobRequest) validate() error {
    if r.Name == "" {
        return fmt.Errorf("name is required")
    }
    if r.Type != domain.JobTypeInline && r.Type != domain.JobTypeKubernetes {
        return fmt.Errorf("type must be 'inline' or 'k8s_job'")
    }
    if r.Type == domain.JobTypeKubernetes && r.Payload.KubernetesSpec == nil {
        return fmt.Errorf("kubernetes_spec required for k8s_job type")
    }
    return nil
}
```

Validation happens BEFORE writing to the database. Bad requests are rejected at the edge — no garbage enters the system.

## HTTP status codes used

| Code | Meaning | When |
|---|---|---|
| `201 Created` | Job was created | New submission |
| `200 OK` | Job already exists | Duplicate idempotency_key |
| `400 Bad Request` | Invalid request body | Missing name, wrong type |
| `404 Not Found` | Job doesn't exist | GET /jobs/{unknown-id} |
| `500 Internal Server Error` | Unexpected error | Database down, etc. |

## JSON encoding/decoding

```go
// Decode incoming request
var req SubmitJobRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
    return
}

// Encode outgoing response
w.Header().Set("Content-Type", "application/json")
w.WriteHeader(http.StatusCreated)
json.NewEncoder(w).Encode(job)
```

Go's `encoding/json` package handles JSON automatically. The struct tags (`json:"name"`) control the field names in JSON.

## Go 1.22 routing

The main.go uses Go 1.22's new HTTP router pattern:
```go
mux.HandleFunc("POST /jobs", jobHandler.SubmitJob)
mux.HandleFunc("GET /jobs/{id}", jobHandler.GetJob)
```

The `{id}` is a path parameter. Inside the handler:
```go
id := r.PathValue("id")  // "550e8400-e29b-41d4-..."
```

Before Go 1.22, you needed third-party routers (gorilla/mux, chi) for this. Now it's built into the standard library.

## File location
```
orion/
└── internal/
    └── api/
        └── handler/
            └── job.go   ← you are here
```

## Next step
The API receives jobs and stores them in PostgreSQL. Now we need the **Scheduler** — the process that picks those jobs up from PostgreSQL and pushes them into Redis for workers to execute.