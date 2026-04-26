# `internal/store/postgres/pipeline.go` + `internal/store/store.go`
## PipelineStore: Interface and SQL Implementation

---

## What changed in `store/store.go`

Three additions to the existing file:

**1. `PipelineStore` interface** — 7 methods covering the full lifecycle of a pipeline and its node-to-job mappings.

**2. Two new types:**
- `PipelineFilter` — controls which pipelines are returned by `ListPipelines` (optional status filter, limit, offset)
- `PipelineJobStatus` — the JOIN result type from `GetPipelineJobs`, carrying `NodeID`, `JobID`, `JobName`, and the current `JobStatus` from the `jobs` table

**3. Updated `Store` composite interface** — now includes `PipelineStore` as the fourth sub-interface alongside `JobStore`, `ExecutionStore`, `WorkerStore`

No existing method signatures changed. `postgres.DB` in `db.go` already implements the first three. After adding `pipeline.go` to the same package, it implements all four.

---

## Why `pipeline.go` is a separate file from `db.go`

`db.go` is 798 lines implementing 12 job/worker/execution methods. Adding 7 more pipeline methods would push it toward 1,100 lines and make it hard to navigate. Go compiles by package, not by file — both `db.go` and `pipeline.go` are in `package postgres` and share the `DB` struct seamlessly.

The split is purely for readability: "job logic in `db.go`, pipeline logic in `pipeline.go`."

---

## The 7 methods in `pipeline.go`

### `CreatePipeline`

```go
func (db *DB) CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error)
```

Inserts a new pipeline in `pending` status. The `DAGSpec` is serialised to JSONB via `json.Marshal`. This means the spec shape (new node fields, edge metadata) can evolve without schema migrations — the JSONB column absorbs any new fields.

```sql
INSERT INTO pipelines (id, name, status, dag_spec, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
```

### `GetPipeline`

```go
func (db *DB) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error)
```

Returns `store.ErrNotFound` when the pipeline does not exist. The `dag_spec` JSONB column is deserialised back into `domain.DAGSpec` via `json.Unmarshal` in the shared `scanPipeline` helper.

### `ListPipelines`

```go
func (db *DB) ListPipelines(ctx context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error)
```

Used by `GET /pipelines?status=...`. Builds the WHERE clause dynamically based on `filter.Status`. Orders by `created_at DESC` (newest first for the UI). Safe parameterised query — no SQL injection risk from the dynamic WHERE.

### `ListPipelinesByStatus`

```go
func (db *DB) ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error)
```

The **scheduler's primary read query** — called every 2 seconds for each of `pending` and `running`. The partial index `idx_pipelines_status_created` (migration 002) makes this constant-time even as the pipeline table grows to millions of completed/failed rows: the index only includes `pending` and `running` rows.

Orders by `created_at ASC` (oldest first) for FIFO fairness — a large slow pipeline doesn't starve newly submitted ones.

### `UpdatePipelineStatus`

```go
func (db *DB) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error
```

Sets `completed_at = NOW()` for terminal statuses (`completed`, `failed`, `cancelled`) so the API can compute total pipeline duration. Returns `ErrNotFound` if the row doesn't exist (0 rows affected).

### `AddPipelineJob`

```go
func (db *DB) AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error
```

Links a created job to a pipeline node. The `ON CONFLICT DO NOTHING` clause is the critical idempotency guard:

```sql
INSERT INTO pipeline_jobs (pipeline_id, job_id, node_id)
VALUES ($1, $2, $3)
ON CONFLICT (pipeline_id, job_id) DO NOTHING
```

**Why this matters:** If the scheduler creates a job (step A) and then crashes before calling `AddPipelineJob` (step B), on restart the node has no entry in `pipeline_jobs`. `ReadyNodes` returns it as ready again. A new job is created and `AddPipelineJob` is called for the new job ID — no conflict, succeeds cleanly. The old job becomes an orphan and is reclaimed in ~90 seconds by the orphan detector.

### `GetPipelineJobs`

```go
func (db *DB) GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error)
```

The **advancement algorithm's primary read** — one JOIN query instead of N+1 separate lookups.

```sql
SELECT pj.node_id, pj.job_id, j.name, j.status
FROM pipeline_jobs pj
JOIN jobs j ON j.id = pj.job_id
WHERE pj.pipeline_id = $1
ORDER BY pj.node_id ASC
```

**Why one query matters:** A 10-node pipeline with N+1 queries = 10 round-trips to PostgreSQL per scheduler tick. With 50 active pipelines on a 2-second tick: 500 queries/tick × 30 ticks/minute = 15,000 queries/minute just for pipeline status reads. The JOIN reduces this to 50 queries/tick = 1,500 queries/minute — a 10× reduction.

The `ORDER BY pj.node_id ASC` is satisfied by the index `idx_pipeline_jobs_pipeline_node(pipeline_id, node_id ASC)` — PostgreSQL returns rows in node_id order with no sort step.

---

## `scanPipeline` — shared scanner

Both `GetPipeline` (single row via `QueryRow`) and `ListPipelines`/`ListPipelinesByStatus` (multiple rows via `Query`) use the same `scanPipeline` function:

```go
type pipelineScanner interface {
    Scan(dest ...any) error
}

func scanPipeline(row pipelineScanner) (*domain.Pipeline, error)
```

The `pipelineScanner` interface abstracts over `pgx.Row` and `pgx.Rows`. This eliminates the duplicated scan logic that would otherwise exist in every SELECT function.

---

## Running the tests

```bash
# Unit tests — no database needed
go test -race ./internal/store/... -v

# Integration tests — requires running PostgreSQL + migration 002 applied
make migrate-up
go test -tags=integration -race ./internal/store/postgres/... -v
```

---

## File locations

```
orion/
└── internal/
    └── store/
        ├── store.go               ← UPDATED: PipelineStore + PipelineFilter + PipelineJobStatus
        └── postgres/
            ├── db.go              ← unchanged (12 job/worker/execution methods)
            └── pipeline.go        ← NEW (7 pipeline SQL methods)
```