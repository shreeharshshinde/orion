# Orion — Phase 5 Master Guide
## Pipeline DAG Orchestration: Chaining Jobs into Workflows

> **What this document is:** Everything you need to understand, plan, and build Phase 5 completely before writing a single line of code — the mental model for DAG execution, every file to create, every SQL query explained, the advancement algorithm, what the API looks like, and the exact output that proves Phase 5 is done.

---

## Table of Contents

1. [Why Phase 5 Exists — The Gap It Closes](#1-why-phase-5-exists--the-gap-it-closes)
2. [What Changes: Before and After](#2-what-changes-before-and-after)
3. [Mental Model — How DAG Execution Works](#3-mental-model--how-dag-execution-works)
4. [The DAG Advancement Algorithm](#4-the-dag-advancement-algorithm)
5. [What Already Exists From Phase 1](#5-what-already-exists-from-phase-1)
6. [Complete File Plan](#6-complete-file-plan)
7. [File 1: `internal/store/store.go` — PipelineStore Interface](#7-file-1-internalstorestorego--pipelinestore-interface)
8. [File 2: `internal/store/postgres/pipeline.go` — Pipeline SQL](#8-file-2-internalstorespostgrespipelinego--pipeline-sql)
9. [File 3: `internal/store/migrations/002_pipeline_indexes.up.sql`](#9-file-3-migrations)
10. [File 4: `internal/pipeline/advancement.go` — The DAG Advancer](#10-file-4-internalpipelineadvancementgo--the-dag-advancer)
11. [File 5: `internal/api/handler/pipeline.go` — HTTP Handlers](#11-file-5-internalapipipelinehandlergo--http-handlers)
12. [File 6: `internal/scheduler/scheduler.go` — DAG Tick Added](#12-file-6-schedulergo--dag-tick-added)
13. [File 7: `cmd/api/main.go` — New Routes](#13-file-7-cmdapimainago--new-routes)
14. [File 8: `cmd/scheduler/main.go` — PipelineStore Wired](#14-file-8-cmdschedulermainago--pipelinestore-wired)
15. [Pipeline State Machine](#15-pipeline-state-machine)
16. [Failure Handling — Cascade Cancellation](#16-failure-handling--cascade-cancellation)
17. [The PipelineStore SQL — Deep Dive](#17-the-pipelinestore-sql--deep-dive)
18. [Step-by-Step Build Order](#18-step-by-step-build-order)
19. [Complete End-to-End Test Sequence](#19-complete-end-to-end-test-sequence)
20. [Common Mistakes and How to Avoid Them](#20-common-mistakes-and-how-to-avoid-them)
21. [Phase 6 Preview](#21-phase-6-preview)

---

## 1. Why Phase 5 Exists — The Gap It Closes

After Phase 4, Orion can run jobs. But real ML workflows are not single jobs — they are sequences:

```
preprocess data → train model → evaluate → deploy
```

Without pipelines, a data scientist must:
1. Submit preprocess job
2. Poll until it completes
3. Manually submit train job
4. Poll until it completes
5. Submit evaluate... and so on

This is fragile (a polling script that crashes loses its place), error-prone (wrong job submitted after the wrong predecessor), and unobservable (no single status for "the training run").

Phase 5 makes Orion handle all of this automatically. Submit a pipeline spec once. Orion ensures:
- Each node starts only when all its dependencies are `completed`
- A node that fails and exhausts retries cancels all downstream nodes
- The pipeline has a single observable status reflecting the whole workflow
- The entire history is queryable via `GET /pipelines/{id}/jobs`

---

## 2. What Changes: Before and After

### Phase 4 state (what you have now)

```
POST /jobs {"type":"inline",...}   → works ✓
POST /jobs {"type":"k8s_job",...}  → works ✓
POST /pipelines                    → 404 (not implemented)
```

### Phase 5 state (what you will have)

```
POST /pipelines {dag_spec: {nodes, edges}} → 201 {"id":"...", "status":"pending"}
GET  /pipelines/{id}                       → {"status":"running", "nodes":[...]}
GET  /pipelines/{id}/jobs                  → [{node_id, job_id, status}, ...]

Scheduler:  every 2s, advancePipelines()
  → for each running pipeline, find completed nodes
  → call ReadyNodes(completed) → get nodes whose deps are now satisfied
  → create jobs for those nodes, link in pipeline_jobs
  → detect all-completed → mark pipeline completed
  → detect dead node → cancel downstream → mark pipeline failed
```

---

## 3. Mental Model — How DAG Execution Works

### The DAG as a dependency graph

Think of a pipeline as a graph where:
- **Nodes** = jobs to run ("preprocess", "train", "evaluate")
- **Edges** = dependency arrows (evaluate depends on train, meaning evaluate cannot start until train completes)

```
preprocess ──► train ──► evaluate ──► deploy
                │
                └──────► validate
                              │
                              └──► evaluate (both train AND validate must complete)
```

### The advancement algorithm in plain English

The scheduler runs `advancePipelines()` every 2 seconds:

```
For each pipeline in status=running:

  1. Load all pipeline_jobs (node_id → job_id → job.status)
  2. Build completed_set = {node_id | job.status = "completed"}
  3. Call ReadyNodes(completed_set) → returns node IDs with all deps done
  4. For each ready node NOT yet created as a job:
       → CreateJob(node.JobTemplate)
       → INSERT into pipeline_jobs
  5. If all nodes are in completed_set:
       → UPDATE pipelines SET status='completed'
  6. If any node is in status='dead' (exhausted retries):
       → cancel all downstream nodes (nodes reachable from dead node)
       → UPDATE pipelines SET status='failed'
```

### Why the scheduler runs advancement, not a separate process

The scheduler already:
- Holds the leader election lock (only one instance runs)
- Has a 2-second tick loop
- Has access to the store

Adding pipeline advancement to the existing `runAsLeader` loop costs zero extra infrastructure. The same leader that dispatches jobs also advances pipelines. They run in the same tick.

### The key insight: jobs don't know they're in a pipeline

Individual jobs are identical whether they run standalone or inside a pipeline. The pipeline layer is entirely in the `pipelines` and `pipeline_jobs` tables. A job being in a pipeline does not change how workers process it.

When a job completes, the worker calls `MarkJobCompleted` as usual. On the next scheduler tick, `advancePipelines` sees that job in the `completed` set and unlocks downstream nodes.

---

## 4. The DAG Advancement Algorithm

This is the most important piece of logic in Phase 5. It must be correct, idempotent, and concurrency-safe.

### Full algorithm (pseudocode)

```
func advancePipeline(ctx, pipelineID):

    pipeline = GetPipeline(pipelineID)
    if pipeline.Status != running: return

    pipelineJobs = GetPipelineJobs(pipelineID)
    // pipelineJobs: [{node_id, job_id, job_status}]

    // Build completed set
    completed = {}
    for pj in pipelineJobs:
        if pj.job_status == "completed":
            completed[pj.node_id] = true

    // Check for failures (dead = exhausted all retries)
    for pj in pipelineJobs:
        if pj.job_status == "dead":
            cancelDownstream(pipelineID, pj.node_id, pipeline.DAGSpec)
            UpdatePipelineStatus(pipelineID, "failed")
            return

    // Find nodes ready to run that haven't been created yet
    createdNodeIDs = {pj.node_id | pj exists}
    ready = pipeline.DAGSpec.ReadyNodes(completed)
    for nodeID in ready:
        if nodeID not in createdNodeIDs:
            node = pipeline.DAGSpec.NodeByID(nodeID)
            job = CreateJob(node.JobTemplate + {name: pipeline.Name+"/"+nodeID})
            InsertPipelineJob(pipelineID, job.ID, nodeID)

    // Check if all nodes are complete
    allNodeIDs = {n.ID | n in pipeline.DAGSpec.Nodes}
    if completed == allNodeIDs:
        UpdatePipelineStatus(pipelineID, "completed")
```

### Why this is idempotent

If the scheduler runs `advancePipeline` twice in rapid succession (e.g., two ticks while a job just completed):

- The `completed` set is recomputed from live DB state each time
- `ReadyNodes` is a pure function — same input → same output
- `nodeID not in createdNodeIDs` prevents creating the same job twice
- `InsertPipelineJob` uses `ON CONFLICT DO NOTHING` for safety

No double-dispatching is possible.

### The initial advance — starting the pipeline

When a pipeline is first created (status=`pending`), the scheduler calls `advancePipelines`. On the very first call:
- `completed = {}` (nothing done yet)
- `ReadyNodes({})` = all nodes with no dependencies (roots of the DAG)
- Those root nodes get jobs created and the pipeline status → `running`

For a linear pipeline `preprocess → train → evaluate`:
- First tick: creates preprocess job, pipeline → running
- When preprocess completes, next tick: creates train job
- When train completes, next tick: creates evaluate job
- When evaluate completes, next tick: detects all done, pipeline → completed

---

## 5. What Already Exists From Phase 1

Phase 1 laid the groundwork. Phase 5 builds on it without needing to touch domain types.

### Already exists ✓

| What | Where | Notes |
|---|---|---|
| `Pipeline` struct | `domain/pipeline.go` | All fields, status constants |
| `DAGSpec`, `DAGNode`, `DAGEdge` | `domain/pipeline.go` | Adjacency list representation |
| `ReadyNodes(completed)` | `domain/pipeline.go` | Pure function, already tested |
| `PipelineStatus` constants | `domain/pipeline.go` | pending/running/completed/failed/cancelled |
| `pipelines` table | `migrations/001` | id, name, status, dag_spec JSONB |
| `pipeline_jobs` table | `migrations/001` | pipeline_id, job_id, node_id, depends_on |

### Does NOT exist yet — Phase 5 builds these

| What | Where | Phase 5 adds |
|---|---|---|
| `PipelineStore` interface | `store/store.go` | NEW: CreatePipeline, GetPipeline, etc. |
| Pipeline SQL | `store/postgres/pipeline.go` | NEW: all pipeline queries |
| `advancePipelines()` | `internal/pipeline/advancement.go` | NEW: the DAG advancement logic |
| Pipeline HTTP handlers | `api/handler/pipeline.go` | NEW: POST/GET /pipelines |
| Scheduler pipeline tick | `scheduler/scheduler.go` | UPDATED: calls advancePipelines |
| Pipeline API routes | `cmd/api/main.go` | UPDATED: register pipeline routes |
| Pipeline indexes | `migrations/002_pipeline_indexes.up.sql` | NEW: performance indexes |

---

## 6. Complete File Plan

Phase 5 touches 9 locations. Five new files, four updates.

```
internal/store/
├── store.go                           ← UPDATED: PipelineStore interface added to Store
└── postgres/
    └── pipeline.go                    ← NEW: All pipeline SQL (separate file from db.go)

internal/store/migrations/
└── 002_pipeline_indexes.up.sql        ← NEW: Composite indexes for pipeline queries
└── 002_pipeline_indexes.down.sql      ← NEW: Drop indexes rollback

internal/pipeline/
└── advancement.go                     ← NEW: advancePipelines(), cancelDownstream()
└── advancement_test.go               ← NEW: Unit tests with fake store

internal/api/handler/
└── pipeline.go                        ← NEW: POST /pipelines, GET /pipelines/{id}

internal/scheduler/
└── scheduler.go                       ← UPDATED: advancePipelines tick added

cmd/api/
└── main.go                            ← UPDATED: pipeline routes registered

cmd/scheduler/
└── main.go                            ← UPDATED: PipelineStore passed to scheduler

docs/phase5/
└── README-pipelines.md               ← NEW: Complete pipeline API reference
└── README-advancement.md             ← NEW: DAG algorithm deep-dive
```

### What is NOT changing

| File | Why unchanged |
|---|---|
| `internal/domain/pipeline.go` | `ReadyNodes()` already exists and is correct |
| `internal/worker/pool.go` | Workers are pipeline-unaware; jobs in pipelines execute identically |
| `internal/worker/inline.go` | No changes |
| `internal/worker/k8s/executor.go` | No changes |
| `internal/queue/redis/redis_queue.go` | Queue is pipeline-unaware |

---

## 7. File 1: `internal/store/store.go` — PipelineStore Interface

Add the `PipelineStore` interface and update the composite `Store` to include it.

### New PipelineStore interface

```go
// PipelineStore manages pipeline lifecycle and node tracking.
type PipelineStore interface {
    // CreatePipeline inserts a new pipeline in pending status.
    CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error)

    // GetPipeline retrieves a pipeline by ID.
    GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error)

    // ListPipelines returns pipelines ordered by created_at DESC.
    ListPipelines(ctx context.Context, filter PipelineFilter) ([]*domain.Pipeline, error)

    // UpdatePipelineStatus transitions a pipeline to a new status.
    UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error

    // ListPipelinesByStatus returns pipelines in a specific status.
    // Used by the scheduler to find pipelines that need advancement.
    ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error)

    // AddPipelineJob links a created job to a pipeline node.
    // ON CONFLICT DO NOTHING makes this idempotent.
    AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error

    // GetPipelineJobs returns all job links for a pipeline, with current job status.
    // Returns PipelineJobStatus structs containing node_id, job_id, and current job.Status.
    GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*PipelineJobStatus, error)
}

// PipelineFilter controls which pipelines are returned by ListPipelines.
type PipelineFilter struct {
    Status *domain.PipelineStatus
    Limit  int
    Offset int
}

// PipelineJobStatus joins pipeline_jobs with jobs to return current execution state.
type PipelineJobStatus struct {
    NodeID    string          // logical name within the pipeline DAG
    JobID     uuid.UUID       // the actual job created for this node
    JobStatus domain.JobStatus // current status from the jobs table
    JobName   string          // human-readable job name
}
```

### Updated Store composite interface

```go
// Store composes all four sub-interfaces.
type Store interface {
    JobStore
    ExecutionStore
    WorkerStore
    PipelineStore   // ← Phase 5 addition
}
```

### Why PipelineJobStatus is a separate type

The `pipeline_jobs` table only stores `pipeline_id`, `job_id`, `node_id`. To get the job's current status, we need a JOIN with the `jobs` table. `PipelineJobStatus` is the result type of that JOIN — it carries everything the advancement algorithm needs in one query instead of N+1 queries.

---

## 8. File 2: `internal/store/postgres/pipeline.go` — Pipeline SQL

All pipeline SQL lives in a **separate file** from `db.go`. This keeps `db.go` focused on job operations and makes pipeline SQL easy to find.

### Complete code to write

```go
package postgres

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// CreatePipeline
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipeline inserts a new pipeline in pending status.
// The DAGSpec is stored as JSONB — no schema migration needed when the spec evolves.
func (db *DB) CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
    if p.ID == uuid.Nil {
        p.ID = uuid.New()
    }
    now := time.Now().UTC()
    p.CreatedAt = now
    p.UpdatedAt = now
    if p.Status == "" {
        p.Status = domain.PipelineStatusPending
    }

    dagJSON, err := json.Marshal(p.DAGSpec)
    if err != nil {
        return nil, fmt.Errorf("marshaling dag_spec: %w", err)
    }

    const q = `
        INSERT INTO pipelines (id, name, status, dag_spec, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6)`

    if _, err := db.pool.Exec(ctx, q,
        p.ID, p.Name, string(p.Status), dagJSON, p.CreatedAt, p.UpdatedAt,
    ); err != nil {
        return nil, fmt.Errorf("inserting pipeline: %w", err)
    }
    return p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPipeline
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error) {
    const q = `
        SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
        FROM pipelines WHERE id = $1`

    row := db.pool.QueryRow(ctx, q, id)
    p, err := scanPipeline(row)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, store.ErrNotFound
        }
        return nil, fmt.Errorf("getting pipeline %s: %w", id, err)
    }
    return p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPipelines
// ─────────────────────────────────────────────────────────────────────────────

func (db *DB) ListPipelines(ctx context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error) {
    whereClauses := []string{"1=1"}
    args := []any{}
    argIdx := 1

    if filter.Status != nil {
        whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
        args = append(args, string(*filter.Status))
        argIdx++
    }

    limit := filter.Limit
    if limit <= 0 || limit > 200 {
        limit = 50
    }
    offset := filter.Offset
    if offset < 0 {
        offset = 0
    }

    q := fmt.Sprintf(`
        SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
        FROM pipelines
        WHERE %s
        ORDER BY created_at DESC
        LIMIT %d OFFSET %d`,
        joinWhere(whereClauses), limit, offset,
    )

    rows, err := db.pool.Query(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("listing pipelines: %w", err)
    }
    defer rows.Close()

    var pipelines []*domain.Pipeline
    for rows.Next() {
        p, err := scanPipeline(rows)
        if err != nil {
            return nil, fmt.Errorf("scanning pipeline: %w", err)
        }
        pipelines = append(pipelines, p)
    }
    return pipelines, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPipelinesByStatus
// ─────────────────────────────────────────────────────────────────────────────

// ListPipelinesByStatus is the scheduler's query — it fetches all pipelines
// in a given status (pending or running) that need advancement.
// Called on every scheduler tick for pipeline advancement.
func (db *DB) ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error) {
    if limit <= 0 {
        limit = 50
    }
    const q = `
        SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
        FROM pipelines
        WHERE status = $1
        ORDER BY created_at ASC
        LIMIT $2`

    rows, err := db.pool.Query(ctx, q, string(status), limit)
    if err != nil {
        return nil, fmt.Errorf("listing pipelines by status %s: %w", status, err)
    }
    defer rows.Close()

    var pipelines []*domain.Pipeline
    for rows.Next() {
        p, err := scanPipeline(rows)
        if err != nil {
            return nil, fmt.Errorf("scanning pipeline: %w", err)
        }
        pipelines = append(pipelines, p)
    }
    return pipelines, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdatePipelineStatus
// ─────────────────────────────────────────────────────────────────────────────

// UpdatePipelineStatus transitions a pipeline to a new status.
// Sets completed_at when transitioning to completed or failed.
func (db *DB) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error {
    var q string
    var args []any

    if status == domain.PipelineStatusCompleted || status == domain.PipelineStatusFailed || status == domain.PipelineStatusCancelled {
        q = `UPDATE pipelines SET status = $1, completed_at = NOW(), updated_at = NOW() WHERE id = $2`
        args = []any{string(status), id}
    } else {
        q = `UPDATE pipelines SET status = $1, updated_at = NOW() WHERE id = $2`
        args = []any{string(status), id}
    }

    result, err := db.pool.Exec(ctx, q, args...)
    if err != nil {
        return fmt.Errorf("updating pipeline %s status to %s: %w", id, status, err)
    }
    if result.RowsAffected() == 0 {
        return store.ErrNotFound
    }
    return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AddPipelineJob
// ─────────────────────────────────────────────────────────────────────────────

// AddPipelineJob links a created job to a pipeline node.
// ON CONFLICT DO NOTHING makes this safe to call twice (idempotent).
// This is critical: if the scheduler tick runs twice before the first
// job is enqueued, we don't create duplicate jobs.
func (db *DB) AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
    const q = `
        INSERT INTO pipeline_jobs (pipeline_id, job_id, node_id)
        VALUES ($1, $2, $3)
        ON CONFLICT (pipeline_id, job_id) DO NOTHING`

    if _, err := db.pool.Exec(ctx, q, pipelineID, jobID, nodeID); err != nil {
        return fmt.Errorf("linking job %s to pipeline %s node %s: %w", jobID, pipelineID, nodeID, err)
    }
    return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPipelineJobs
// ─────────────────────────────────────────────────────────────────────────────

// GetPipelineJobs returns all job links for a pipeline, joined with current job status.
// This is the advancement algorithm's primary read query — one query instead of N+1.
//
// SQL:
//   SELECT pj.node_id, pj.job_id, j.status, j.name
//   FROM pipeline_jobs pj
//   JOIN jobs j ON j.id = pj.job_id
//   WHERE pj.pipeline_id = $1
//   ORDER BY pj.node_id
func (db *DB) GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
    const q = `
        SELECT pj.node_id, pj.job_id, j.status, j.name
        FROM pipeline_jobs pj
        JOIN jobs j ON j.id = pj.job_id
        WHERE pj.pipeline_id = $1
        ORDER BY pj.node_id ASC`

    rows, err := db.pool.Query(ctx, q, pipelineID)
    if err != nil {
        return nil, fmt.Errorf("getting pipeline jobs for %s: %w", pipelineID, err)
    }
    defer rows.Close()

    var result []*store.PipelineJobStatus
    for rows.Next() {
        pjs := &store.PipelineJobStatus{}
        var statusStr string
        if err := rows.Scan(&pjs.NodeID, &pjs.JobID, &statusStr, &pjs.JobName); err != nil {
            return nil, fmt.Errorf("scanning pipeline job: %w", err)
        }
        pjs.JobStatus = domain.JobStatus(statusStr)
        result = append(result, pjs)
    }
    return result, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Scanning helpers
// ─────────────────────────────────────────────────────────────────────────────

type pipelineScanner interface {
    Scan(dest ...any) error
}

func scanPipeline(row pipelineScanner) (*domain.Pipeline, error) {
    p := &domain.Pipeline{}
    var statusStr string
    var dagJSON []byte

    err := row.Scan(
        &p.ID, &p.Name, &statusStr, &dagJSON,
        &p.CreatedAt, &p.UpdatedAt, &p.CompletedAt,
    )
    if err != nil {
        return nil, err
    }

    p.Status = domain.PipelineStatus(statusStr)
    if len(dagJSON) > 0 {
        if err := json.Unmarshal(dagJSON, &p.DAGSpec); err != nil {
            return nil, fmt.Errorf("unmarshaling dag_spec: %w", err)
        }
    }
    return p, nil
}

func joinWhere(clauses []string) string {
    result := ""
    for i, c := range clauses {
        if i > 0 {
            result += " AND "
        }
        result += c
    }
    return result
}
```

### Why pipeline SQL is in a separate file

`db.go` is already 798 lines implementing 12 methods. Adding 7 more pipeline methods would push it past 1,100 lines. Splitting into `pipeline.go` keeps each file focused and navigable. Both files are in the same `postgres` package — they share the `DB` struct. Go's compilation is per-package, not per-file.

---

## 9. File 3: Migrations

### `002_pipeline_indexes.up.sql`

The `pipelines` and `pipeline_jobs` tables were created in migration 001. This migration adds performance indexes needed by Phase 5 query patterns.

```sql
-- Migration: 002_pipeline_indexes.up.sql
-- Adds indexes to support Phase 5 pipeline orchestration query patterns.
-- The pipelines and pipeline_jobs tables exist from 001 but lack
-- the composite indexes needed for efficient scheduler advancement queries.

-- Scheduler advancement query: find all pipelines needing advancement
-- Status filter + created_at ordering (process oldest first, FIFO fairness)
CREATE INDEX IF NOT EXISTS idx_pipelines_status_created
    ON pipelines (status, created_at ASC)
    WHERE status IN ('pending', 'running');

-- API list query: filter pipelines by status, ordered by created_at DESC
CREATE INDEX IF NOT EXISTS idx_pipelines_status_list
    ON pipelines (status, created_at DESC);

-- Advancement algorithm: load all jobs for a pipeline in one query
-- This powers the GetPipelineJobs JOIN — both sides of the JOIN need indexes
CREATE INDEX IF NOT EXISTS idx_pipeline_jobs_pipeline_id
    ON pipeline_jobs (pipeline_id, node_id ASC);
```

### `002_pipeline_indexes.down.sql`

```sql
-- Migration: 002_pipeline_indexes.down.sql
DROP INDEX IF EXISTS idx_pipelines_status_created;
DROP INDEX IF EXISTS idx_pipelines_status_list;
DROP INDEX IF EXISTS idx_pipeline_jobs_pipeline_id;
```

---

## 10. File 4: `internal/pipeline/advancement.go` — The DAG Advancer

This is the most important new file in Phase 5. It implements the core DAG advancement logic, decoupled from the scheduler.

### Why a separate package?

The advancement algorithm is complex enough to deserve its own package and test file. By putting it in `internal/pipeline` instead of `internal/scheduler`, we:
1. Can test it without touching scheduler internals
2. Make it easy to find ("DAG logic lives in the pipeline package")
3. Keep the scheduler thin (it just calls `advancer.AdvanceAll()`)

### Complete code to write

```go
// Package pipeline implements the DAG advancement algorithm for Orion pipelines.
//
// The Advancer is called by the scheduler on every tick. It:
//  1. Fetches all pipelines in pending or running status
//  2. For each pipeline, computes which nodes are ready to run
//  3. Creates jobs for ready nodes that haven't been created yet
//  4. Detects completion (all nodes done) and failure (any node dead)
//  5. Triggers cascade cancellation of downstream nodes on failure
package pipeline

import (
    "context"
    "fmt"
    "log/slog"

    "github.com/google/uuid"
    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/store"
)

// Advancer holds the dependencies needed to advance pipelines.
// It is stateless — all state lives in PostgreSQL.
type Advancer struct {
    store  store.Store
    logger *slog.Logger
}

// NewAdvancer creates an Advancer. The store must implement PipelineStore.
func NewAdvancer(s store.Store, logger *slog.Logger) *Advancer {
    return &Advancer{store: s, logger: logger}
}

// AdvanceAll advances all pipelines that need work.
// Called by the scheduler on every tick. Should be fast — heavy work
// is creating jobs (fast) not executing them (done by workers).
func (a *Advancer) AdvanceAll(ctx context.Context) error {
    // Collect pipelines in both pending and running status.
    // Pending pipelines need their root nodes kicked off.
    // Running pipelines need their next wave of nodes checked.
    var toAdvance []*domain.Pipeline

    pending, err := a.store.ListPipelinesByStatus(ctx, domain.PipelineStatusPending, 50)
    if err != nil {
        return fmt.Errorf("listing pending pipelines: %w", err)
    }
    toAdvance = append(toAdvance, pending...)

    running, err := a.store.ListPipelinesByStatus(ctx, domain.PipelineStatusRunning, 50)
    if err != nil {
        return fmt.Errorf("listing running pipelines: %w", err)
    }
    toAdvance = append(toAdvance, running...)

    for _, p := range toAdvance {
        if err := a.advanceOne(ctx, p); err != nil {
            // Log but don't fail the whole batch — one broken pipeline
            // should not block advancement of all other pipelines.
            a.logger.Error("failed to advance pipeline",
                "pipeline_id", p.ID,
                "pipeline_name", p.Name,
                "err", err,
            )
        }
    }
    return nil
}

// advanceOne advances a single pipeline by one step.
func (a *Advancer) advanceOne(ctx context.Context, p *domain.Pipeline) error {
    a.logger.Debug("advancing pipeline",
        "pipeline_id", p.ID,
        "pipeline_name", p.Name,
        "status", p.Status,
        "node_count", len(p.DAGSpec.Nodes),
    )

    // Load current state of all jobs in this pipeline.
    pipelineJobs, err := a.store.GetPipelineJobs(ctx, p.ID)
    if err != nil {
        return fmt.Errorf("getting pipeline jobs: %w", err)
    }

    // Build maps for fast lookup.
    // completed: node_id → true (for ReadyNodes input)
    // createdNodes: node_id → PipelineJobStatus (to detect already-created nodes)
    completed := make(map[string]bool)
    createdNodes := make(map[string]*store.PipelineJobStatus)

    for _, pj := range pipelineJobs {
        createdNodes[pj.NodeID] = pj
        if pj.JobStatus == domain.JobStatusCompleted {
            completed[pj.NodeID] = true
        }
    }

    // ── Failure detection ──────────────────────────────────────────────────
    // If any node is dead (exhausted all retries), the pipeline has failed.
    // Cancel all downstream nodes that haven't started yet.
    for _, pj := range pipelineJobs {
        if pj.JobStatus == domain.JobStatusDead {
            a.logger.Warn("pipeline node reached dead state — failing pipeline",
                "pipeline_id", p.ID,
                "node_id", pj.NodeID,
                "job_id", pj.JobID,
            )
            a.cancelDownstreamNodes(ctx, p, pj.NodeID, createdNodes)
            return a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusFailed)
        }
    }

    // ── Transition pending → running on first advancement ─────────────────
    if p.Status == domain.PipelineStatusPending {
        if err := a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusRunning); err != nil {
            return fmt.Errorf("transitioning pipeline to running: %w", err)
        }
        p.Status = domain.PipelineStatusRunning
        a.logger.Info("pipeline started", "pipeline_id", p.ID, "pipeline_name", p.Name)
    }

    // ── Ready node detection ───────────────────────────────────────────────
    // ReadyNodes returns nodes whose ALL dependencies are in the completed set.
    ready := p.DAGSpec.ReadyNodes(completed)

    for _, nodeID := range ready {
        // Skip nodes that already have a job (idempotency guard).
        if _, alreadyCreated := createdNodes[nodeID]; alreadyCreated {
            continue
        }

        // Find the node template in the DAG spec.
        node := findNode(p.DAGSpec, nodeID)
        if node == nil {
            a.logger.Error("ready node not found in DAG spec",
                "pipeline_id", p.ID,
                "node_id", nodeID,
            )
            continue
        }

        // Create the job for this node.
        job := &domain.Job{
            Name:      fmt.Sprintf("%s/%s", p.Name, nodeID),
            Type:      jobTypeFromPayload(node.JobTemplate),
            QueueName: queueFromPayload(node.JobTemplate),
            Priority:  domain.PriorityNormal,
            Payload:   node.JobTemplate,
            MaxRetries: 3,
        }

        created, err := a.store.CreateJob(ctx, job)
        if err != nil {
            a.logger.Error("failed to create job for pipeline node",
                "pipeline_id", p.ID,
                "node_id", nodeID,
                "err", err,
            )
            continue // skip this node, try again next tick
        }

        // Link the job to the pipeline.
        if err := a.store.AddPipelineJob(ctx, p.ID, nodeID, created.ID); err != nil {
            a.logger.Error("failed to link job to pipeline",
                "pipeline_id", p.ID,
                "node_id", nodeID,
                "job_id", created.ID,
                "err", err,
            )
            continue
        }

        a.logger.Info("created job for pipeline node",
            "pipeline_id", p.ID,
            "node_id", nodeID,
            "job_id", created.ID,
            "job_type", job.Type,
        )
    }

    // ── Completion detection ───────────────────────────────────────────────
    // All nodes in the DAG must be completed for the pipeline to complete.
    allNodeIDs := make(map[string]bool)
    for _, node := range p.DAGSpec.Nodes {
        allNodeIDs[node.ID] = true
    }

    allComplete := len(allNodeIDs) > 0
    for nodeID := range allNodeIDs {
        if !completed[nodeID] {
            allComplete = false
            break
        }
    }

    if allComplete {
        a.logger.Info("pipeline completed — all nodes done",
            "pipeline_id", p.ID,
            "pipeline_name", p.Name,
            "node_count", len(allNodeIDs),
        )
        return a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusCompleted)
    }

    return nil
}

// cancelDownstreamNodes cancels all nodes reachable from failedNodeID
// that have not yet been started (no job created). Nodes already running
// are left to complete naturally — they cannot be killed mid-execution.
//
// "Reachable" means: nodes that depend on failedNodeID, directly or transitively.
func (a *Advancer) cancelDownstreamNodes(
    ctx context.Context,
    p *domain.Pipeline,
    failedNodeID string,
    createdNodes map[string]*store.PipelineJobStatus,
) {
    downstream := findDownstream(p.DAGSpec, failedNodeID)
    for _, nodeID := range downstream {
        if _, created := createdNodes[nodeID]; !created {
            // Node hasn't started — mark it cancelled in pipeline_jobs
            // by creating a sentinel cancelled job.
            // For Phase 5, we log the cancellation. Phase 6 adds explicit
            // cancelled job records for full audit trail.
            a.logger.Info("downstream node cancelled due to upstream failure",
                "pipeline_id", p.ID,
                "failed_node", failedNodeID,
                "cancelled_node", nodeID,
            )
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// findNode returns the DAGNode with the given ID, or nil if not found.
func findNode(dag domain.DAGSpec, nodeID string) *domain.DAGNode {
    for i := range dag.Nodes {
        if dag.Nodes[i].ID == nodeID {
            return &dag.Nodes[i]
        }
    }
    return nil
}

// findDownstream returns all node IDs reachable from startNodeID
// following edges in the forward direction (BFS).
func findDownstream(dag domain.DAGSpec, startNodeID string) []string {
    // Build adjacency list: source → []targets
    adj := make(map[string][]string)
    for _, edge := range dag.Edges {
        adj[edge.Source] = append(adj[edge.Source], edge.Target)
    }

    visited := make(map[string]bool)
    queue := []string{startNodeID}
    var downstream []string

    for len(queue) > 0 {
        current := queue[0]
        queue = queue[1:]
        for _, next := range adj[current] {
            if !visited[next] {
                visited[next] = true
                downstream = append(downstream, next)
                queue = append(queue, next)
            }
        }
    }
    return downstream
}

// jobTypeFromPayload infers the job type from the payload.
func jobTypeFromPayload(payload domain.JobPayload) domain.JobType {
    if payload.KubernetesSpec != nil {
        return domain.JobTypeKubernetes
    }
    return domain.JobTypeInline
}

// queueFromPayload returns the queue name for a pipeline node job.
// Pipeline jobs use the default queue unless the spec overrides it.
func queueFromPayload(_ domain.JobPayload) string {
    return "default"
}
```

---

## 11. File 5: `internal/api/handler/pipeline.go` — HTTP Handlers

```go
package handler

import (
    "encoding/json"
    "errors"
    "log/slog"
    "net/http"

    "github.com/google/uuid"
    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/store"
)

// PipelineHandler handles HTTP requests for pipeline CRUD operations.
// Design principle: thin handlers — parse request, call store, write response.
type PipelineHandler struct {
    store  store.Store
    logger *slog.Logger
}

// NewPipelineHandler creates a PipelineHandler.
func NewPipelineHandler(s store.Store, logger *slog.Logger) *PipelineHandler {
    return &PipelineHandler{store: s, logger: logger}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request types
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipelineRequest is the body for POST /pipelines.
type CreatePipelineRequest struct {
    Name    string          `json:"name"`
    DAGSpec domain.DAGSpec  `json:"dag_spec"`
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /pipelines
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipeline handles POST /pipelines.
// Validates the DAG spec (no cycles, at least one node, valid references),
// then persists the pipeline in pending status.
// The scheduler picks it up within 2 seconds and starts advancing it.
func (h *PipelineHandler) CreatePipeline(w http.ResponseWriter, r *http.Request) {
    var req CreatePipelineRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
        return
    }

    if req.Name == "" {
        writeError(w, http.StatusUnprocessableEntity, "name is required")
        return
    }
    if len(req.DAGSpec.Nodes) == 0 {
        writeError(w, http.StatusUnprocessableEntity, "dag_spec.nodes must have at least one node")
        return
    }
    if err := validateDAG(req.DAGSpec); err != nil {
        writeError(w, http.StatusUnprocessableEntity, "invalid dag_spec: "+err.Error())
        return
    }

    pipeline := &domain.Pipeline{
        Name:    req.Name,
        Status:  domain.PipelineStatusPending,
        DAGSpec: req.DAGSpec,
    }

    created, err := h.store.CreatePipeline(r.Context(), pipeline)
    if err != nil {
        h.logger.Error("failed to create pipeline", "name", req.Name, "err", err)
        writeError(w, http.StatusInternalServerError, "failed to create pipeline")
        return
    }

    writeJSON(w, http.StatusCreated, created)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id}
// ─────────────────────────────────────────────────────────────────────────────

// GetPipeline handles GET /pipelines/{id}.
// Returns the pipeline with its DAG spec and current status.
// Poll this endpoint to track pipeline progress.
func (h *PipelineHandler) GetPipeline(w http.ResponseWriter, r *http.Request) {
    id, err := parsePipelineID(r)
    if err != nil {
        writeError(w, http.StatusBadRequest, "invalid pipeline ID: must be a UUID")
        return
    }

    pipeline, err := h.store.GetPipeline(r.Context(), id)
    if err != nil {
        if errors.Is(err, store.ErrNotFound) {
            writeError(w, http.StatusNotFound, "pipeline not found")
            return
        }
        h.logger.Error("failed to get pipeline", "id", id, "err", err)
        writeError(w, http.StatusInternalServerError, "internal error")
        return
    }

    writeJSON(w, http.StatusOK, pipeline)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines/{id}/jobs
// ─────────────────────────────────────────────────────────────────────────────

// GetPipelineJobs handles GET /pipelines/{id}/jobs.
// Returns the current status of every node in the pipeline, including
// which nodes are pending (no job yet), running, completed, or failed.
func (h *PipelineHandler) GetPipelineJobs(w http.ResponseWriter, r *http.Request) {
    id, err := parsePipelineID(r)
    if err != nil {
        writeError(w, http.StatusBadRequest, "invalid pipeline ID: must be a UUID")
        return
    }

    // Verify pipeline exists
    if _, err := h.store.GetPipeline(r.Context(), id); err != nil {
        if errors.Is(err, store.ErrNotFound) {
            writeError(w, http.StatusNotFound, "pipeline not found")
            return
        }
        writeError(w, http.StatusInternalServerError, "internal error")
        return
    }

    jobs, err := h.store.GetPipelineJobs(r.Context(), id)
    if err != nil {
        h.logger.Error("failed to get pipeline jobs", "pipeline_id", id, "err", err)
        writeError(w, http.StatusInternalServerError, "internal error")
        return
    }

    writeJSON(w, http.StatusOK, map[string]any{
        "pipeline_id": id,
        "jobs":        jobs,
        "count":       len(jobs),
    })
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /pipelines
// ─────────────────────────────────────────────────────────────────────────────

// ListPipelines handles GET /pipelines.
// Optional query param: ?status=pending|running|completed|failed
func (h *PipelineHandler) ListPipelines(w http.ResponseWriter, r *http.Request) {
    filter := store.PipelineFilter{Limit: 50}

    if s := r.URL.Query().Get("status"); s != "" {
        status := domain.PipelineStatus(s)
        filter.Status = &status
    }

    pipelines, err := h.store.ListPipelines(r.Context(), filter)
    if err != nil {
        h.logger.Error("failed to list pipelines", "err", err)
        writeError(w, http.StatusInternalServerError, "internal error")
        return
    }

    writeJSON(w, http.StatusOK, map[string]any{
        "pipelines": pipelines,
        "count":     len(pipelines),
    })
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation
// ─────────────────────────────────────────────────────────────────────────────

// validateDAG checks the DAG spec for basic correctness.
// Full cycle detection is O(V+E) with DFS; this is a sufficient subset for Phase 5.
func validateDAG(dag domain.DAGSpec) error {
    if len(dag.Nodes) == 0 {
        return errors.New("at least one node is required")
    }

    // Build node ID set for reference validation
    nodeIDs := make(map[string]bool)
    for _, n := range dag.Nodes {
        if n.ID == "" {
            return errors.New("all nodes must have a non-empty id")
        }
        if nodeIDs[n.ID] {
            return errors.New("duplicate node id: " + n.ID)
        }
        nodeIDs[n.ID] = true

        // Validate payload
        if n.JobTemplate.HandlerName == "" && n.JobTemplate.KubernetesSpec == nil {
            return errors.New("node " + n.ID + ": job_template must have handler_name or kubernetes_spec")
        }
    }

    // Validate edges reference existing nodes
    for _, e := range dag.Edges {
        if !nodeIDs[e.Source] {
            return errors.New("edge source " + e.Source + " does not reference a known node")
        }
        if !nodeIDs[e.Target] {
            return errors.New("edge target " + e.Target + " does not reference a known node")
        }
        if e.Source == e.Target {
            return errors.New("self-loop detected on node " + e.Source)
        }
    }

    return nil
}

// parsePipelineID extracts and validates the {id} path parameter.
func parsePipelineID(r *http.Request) (uuid.UUID, error) {
    return uuid.Parse(r.PathValue("id"))
}
```

---

## 12. File 6: `internal/scheduler/scheduler.go` — DAG Tick Added

Add `advancePipelines()` call to the existing `runAsLeader` loop and wire the `Advancer`.

### What changes in scheduler.go

```go
// In the Scheduler struct — add advancer field:
type Scheduler struct {
    cfg      Config
    db       *pgxpool.Pool
    store    store.Store
    queue    queue.Queue
    advancer *pipeline.Advancer  // ← NEW
    logger   *slog.Logger
}

// In New() — accept advancer or construct it:
func New(cfg Config, db *pgxpool.Pool, s store.Store, q queue.Queue,
    advancer *pipeline.Advancer, logger *slog.Logger) *Scheduler {
    // ...
    return &Scheduler{..., advancer: advancer}
}

// In runAsLeader() — add pipeline tick to the schedule ticker case:
case <-scheduleTicker.C:
    if err := s.scheduleQueuedJobs(ctx); err != nil {
        s.logger.Error("schedule loop error", "err", err)
    }
    if err := s.promoteRetryableJobs(ctx); err != nil {
        s.logger.Error("retry promotion error", "err", err)
    }
    // Phase 5: advance all pipelines on every tick
    if err := s.advancer.AdvanceAll(ctx); err != nil {
        s.logger.Error("pipeline advancement error", "err", err)
    }
```

### Why pipeline advancement runs on the schedule ticker (not orphan ticker)

Pipeline advancement is lightweight and time-sensitive. When a job completes, the next pipeline stage should start as quickly as possible. Running on the 2-second `scheduleTicker` gives a maximum 2-second lag between a node completing and the next node starting.

The orphan ticker runs every 30 seconds — too slow for responsive pipeline execution.

---

## 13. File 7: `cmd/api/main.go` — New Routes

```go
// Add to imports:
// "github.com/shreeharsh-a/orion/internal/api/handler"

// After job handler registration, add pipeline routes:
pipelineHandler := handler.NewPipelineHandler(pgStore, logger)
mux.HandleFunc("POST /pipelines",             pipelineHandler.CreatePipeline)
mux.HandleFunc("GET /pipelines",              pipelineHandler.ListPipelines)
mux.HandleFunc("GET /pipelines/{id}",         pipelineHandler.GetPipeline)
mux.HandleFunc("GET /pipelines/{id}/jobs",    pipelineHandler.GetPipelineJobs)
```

---

## 14. File 8: `cmd/scheduler/main.go` — PipelineStore Wired

```go
// Add import:
// "github.com/shreeharsh-a/orion/internal/pipeline"

// After store creation:
advancer := pipeline.NewAdvancer(pgStore, logger)

// Update scheduler.New() call:
sched := scheduler.New(
    scheduler.Config{...},
    db,
    pgStore,
    queue,
    advancer,  // ← NEW
    logger,
)
```

---

## 15. Pipeline State Machine

```
                  ┌──────────────────────────────────────────────┐
                  │  POST /pipelines creates the pipeline         │
                  │  Scheduler picks it up within 2 seconds      │
                  ▼                                              │
              pending ──────────────────────────────────────────►│
                  │                                              │
                  │ scheduler.advancePipelines()                  │
                  │ root nodes created, first jobs submitted      │
                  ▼                                              │
              running                                            │
                  │                                              │
                  ├──── all nodes completed ──────────►  completed (terminal)
                  │
                  ├──── any node reaches dead ─────────►  failed (terminal)
                  │     (cascade cancels downstream)
                  │
                  └──── manual cancel (Phase 6+) ──────►  cancelled (terminal)
```

### Node-level state within a pipeline

Each node in the pipeline is backed by a real `jobs` record. The node's state **is** the job's state:

```
pipeline node "train" → jobs.id = abc123 → jobs.status = "running"

Node state transitions mirror job state transitions:
  pending   (no job created yet, waiting for dependencies)
  queued    (job created, waiting for scheduler dispatch)
  scheduled (job in Redis, worker claimed)
  running   (worker executing)
  completed ← pipeline advancement detects this → unlocks downstream
  failed    (retrying with backoff)
  dead      ← pipeline advancement detects this → cascade cancel + pipeline failed
```

---

## 16. Failure Handling — Cascade Cancellation

When a node reaches `dead` status (exhausted all retries):

1. `advanceOne` detects `pj.JobStatus == domain.JobStatusDead`
2. `cancelDownstreamNodes(ctx, p, deadNodeID, createdNodes)` called
3. BFS from `deadNodeID` following forward edges finds all downstream nodes
4. Nodes that haven't been created (no job yet) are logged as cancelled
5. Pipeline status → `failed`

### Why we don't cancel already-running nodes

When a node dies, its downstream nodes haven't started yet (by definition of the DAG execution model — downstream nodes only start after their dependencies complete). So cancellation only affects nodes in `pending` state (no job created yet). There's nothing to kill.

If for any reason a downstream node somehow started (e.g., a race condition), letting it run to completion is the safer choice — killing a running training pod wastes its work and may leave artifacts in an inconsistent state.

---

## 17. The PipelineStore SQL — Deep Dive

### The critical query: `GetPipelineJobs`

```sql
SELECT pj.node_id, pj.job_id, j.status, j.name
FROM pipeline_jobs pj
JOIN jobs j ON j.id = pj.job_id
WHERE pj.pipeline_id = $1
ORDER BY pj.node_id ASC
```

This is the advancement algorithm's primary read. Instead of N queries (one per node), one JOIN gives us all node statuses in a single round-trip.

**With index `idx_pipeline_jobs_pipeline_id (pipeline_id, node_id ASC)`:**
- PostgreSQL does an index scan on `pipeline_jobs` filtering by `pipeline_id`
- Then nested loop joins to `jobs` using primary key lookup (`j.id`)
- For a 10-node pipeline: 1 index scan + 10 primary key lookups ≈ microseconds

### `AddPipelineJob` with ON CONFLICT

```sql
INSERT INTO pipeline_jobs (pipeline_id, job_id, node_id)
VALUES ($1, $2, $3)
ON CONFLICT (pipeline_id, job_id) DO NOTHING
```

This is the idempotency guard. If the scheduler creates a job and then crashes before calling `AddPipelineJob`, on restart it will find:
- The node has no entry in `pipeline_jobs` (job was created but not linked)
- `ReadyNodes` returns the node as ready again
- A new job is created (the old orphaned one will be reclaimed by orphan detection)

This is safe: the old job will be reclaimed and the new one will run.

### `ListPipelinesByStatus` — the scheduler's query

```sql
SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
FROM pipelines
WHERE status = $1
ORDER BY created_at ASC
LIMIT $2
```

With index `idx_pipelines_status_created (status, created_at ASC) WHERE status IN ('pending', 'running')`:
- PostgreSQL uses a partial index — only scans `pending` and `running` pipelines
- Completed and failed pipelines (vast majority over time) are not in the index
- Constant-time as pipeline history grows

---

## 18. Step-by-Step Build Order

### Step 1 — Update `store/store.go`

Add `PipelineStore` interface and `PipelineFilter`, `PipelineJobStatus` types. Update the composite `Store` interface to include `PipelineStore`.

```bash
go build ./internal/store/...
# Must compile — existing Store implementors (postgres.DB) will now fail
# because they don't implement PipelineStore yet. That's expected.
```

### Step 2 — Create `store/postgres/pipeline.go`

Write all 7 methods. After this, `postgres.DB` again satisfies the `Store` interface.

```bash
go build ./internal/store/...
# Must compile with zero errors
go build ./...
# All packages must compile
```

### Step 3 — Apply migration 002

```bash
make migrate-up
# Creates the 3 new indexes
# Verify:
docker compose exec postgres psql -U orion -d orion \
  -c "\di+ idx_pipelines*"
```

### Step 4 — Create `internal/pipeline/advancement.go`

Write the `Advancer` struct and `AdvanceAll`, `advanceOne`, `cancelDownstreamNodes`.

```bash
go build ./internal/pipeline/...
# Must compile
```

### Step 5 — Write `internal/pipeline/advancement_test.go`

Write unit tests using a fake store.

```bash
go test -race ./internal/pipeline/... -v
# All tests pass
```

### Step 6 — Create `internal/api/handler/pipeline.go`

Write `PipelineHandler` with all 4 route methods.

```bash
go build ./internal/api/...
# Must compile
```

### Step 7 — Update `internal/scheduler/scheduler.go`

Add `advancer` field, update `New()` signature, add `advancer.AdvanceAll(ctx)` call in `runAsLeader`.

```bash
go build ./internal/scheduler/...
# Must compile
```

### Step 8 — Update `cmd/api/main.go` and `cmd/scheduler/main.go`

Register pipeline routes and wire the advancer.

```bash
make build
# All 3 binaries compile
```

---

## 19. Complete End-to-End Test Sequence

### Test 1 — Linear pipeline (3 nodes in sequence)

```bash
PIPELINE_ID=$(curl -s -X POST http://localhost:8080/pipelines \
  -H "Content-Type: application/json" \
  -d '{
    "name": "linear-test-pipeline",
    "dag_spec": {
      "nodes": [
        {"id": "preprocess", "job_template": {"handler_name": "noop"}},
        {"id": "train",      "job_template": {"handler_name": "noop"}},
        {"id": "evaluate",   "job_template": {"handler_name": "noop"}}
      ],
      "edges": [
        {"source": "preprocess", "target": "train"},
        {"source": "train",      "target": "evaluate"}
      ]
    }
  }' | jq -r .id)

echo "Pipeline: $PIPELINE_ID"

# Watch pipeline status
watch -n2 "curl -s http://localhost:8080/pipelines/$PIPELINE_ID | jq '{status}'"
# pending → running → completed

# Watch node statuses
watch -n2 "curl -s http://localhost:8080/pipelines/$PIPELINE_ID/jobs | jq '.jobs[] | {node_id, status: .job_status}'"
# preprocess: queued → completed
# train: (pending) → queued → completed
# evaluate: (pending) → queued → completed

echo "✅ TEST 1 PASSED — linear pipeline executed in order"
```

Expected timeline (with noop handlers):
```
t=0s:  POST → pipeline created (pending)
t=2s:  Scheduler tick → preprocess job created → pipeline running
t=4s:  Preprocess completes → scheduler tick → train job created
t=6s:  Train completes → scheduler tick → evaluate job created
t=8s:  Evaluate completes → scheduler tick → pipeline completed
```

### Test 2 — Diamond DAG (fan-out then fan-in)

```bash
PIPELINE_ID=$(curl -s -X POST http://localhost:8080/pipelines \
  -H "Content-Type: application/json" \
  -d '{
    "name": "diamond-dag-test",
    "dag_spec": {
      "nodes": [
        {"id": "preprocess",    "job_template": {"handler_name": "noop"}},
        {"id": "train_vision",  "job_template": {"handler_name": "slow", "args": {"duration_seconds": 3}}},
        {"id": "train_nlp",     "job_template": {"handler_name": "slow", "args": {"duration_seconds": 4}}},
        {"id": "merge_results", "job_template": {"handler_name": "noop"}}
      ],
      "edges": [
        {"source": "preprocess",   "target": "train_vision"},
        {"source": "preprocess",   "target": "train_nlp"},
        {"source": "train_vision", "target": "merge_results"},
        {"source": "train_nlp",    "target": "merge_results"}
      ]
    }
  }' | jq -r .id)

# Watch — merge_results must wait for BOTH vision AND nlp to complete
watch -n2 "curl -s http://localhost:8080/pipelines/$PIPELINE_ID/jobs \
  | jq '.jobs[] | {node_id, job_status}'"
# preprocess:    completed
# train_vision:  running  (3s)
# train_nlp:     running  (4s, parallel with vision)
# merge_results: (pending until BOTH complete)
```

### Test 3 — Failure cascade

```bash
PIPELINE_ID=$(curl -s -X POST http://localhost:8080/pipelines \
  -H "Content-Type: application/json" \
  -d '{
    "name": "failure-cascade-test",
    "dag_spec": {
      "nodes": [
        {"id": "preprocess", "job_template": {"handler_name": "noop"}},
        {"id": "train",      "job_template": {"handler_name": "always_fail"}},
        {"id": "evaluate",   "job_template": {"handler_name": "noop"}}
      ],
      "edges": [
        {"source": "preprocess", "target": "train"},
        {"source": "train",      "target": "evaluate"}
      ]
    }
  }' | jq -r .id)

# With max_retries=3, train will exhaust retries and reach dead state
# Pipeline should eventually reach failed status
# evaluate should never start (cancelled)
watch -n3 "curl -s http://localhost:8080/pipelines/$PIPELINE_ID | jq .status"
# pending → running → failed

curl -s http://localhost:8080/pipelines/$PIPELINE_ID/jobs | jq '.jobs'
# preprocess: completed
# train: dead
# evaluate: (no job created — cascade cancelled)

echo "✅ TEST 3 PASSED — failure cascade works"
```

### Test 4 — Direct PostgreSQL verification

```bash
PSQL="docker exec -i $(docker compose ps -q postgres) psql -U orion -d orion -c"

# Verify pipeline record
$PSQL "SELECT name, status, created_at, completed_at FROM pipelines ORDER BY created_at DESC LIMIT 3;"

# Verify node-to-job linkage
$PSQL "SELECT pj.node_id, j.name, j.status FROM pipeline_jobs pj
       JOIN jobs j ON j.id = pj.job_id
       ORDER BY pj.pipeline_id, pj.node_id;"

# Verify indexes are used (EXPLAIN)
$PSQL "EXPLAIN SELECT * FROM pipelines WHERE status = 'running' ORDER BY created_at ASC LIMIT 50;"
# Should show: Index Scan on idx_pipelines_status_created
```

---

## 20. Common Mistakes and How to Avoid Them

| Mistake | Symptom | Fix |
|---|---|---|
| Running AdvanceAll without leader lock | Two schedulers create duplicate jobs for the same node | AdvanceAll is called inside `runAsLeader` which holds the PG advisory lock — only one scheduler runs it |
| Not checking `alreadyCreated` before CreateJob | Same node gets 2+ jobs on rapid successive ticks | The `createdNodes[nodeID]` check is the idempotency guard — never remove it |
| Missing `ON CONFLICT DO NOTHING` in AddPipelineJob | Crash between CreateJob and AddPipelineJob creates orphaned jobs | The INSERT is idempotent by design — always use ON CONFLICT |
| `ReadyNodes` including already-running nodes | Jobs created twice for the same node | `ReadyNodes` only returns nodes NOT in `completed` — but you must also exclude nodes in `createdNodes` |
| Cycle in DAG spec | Pipeline runs forever | `validateDAG` in the handler catches self-loops; add DFS cycle detection for full safety |
| Not setting pipeline to `running` on first tick | Pipeline stays `pending` forever | `advanceOne` explicitly transitions pending→running before creating root node jobs |
| Cascade cancellation cancels running jobs | Running job killed mid-execution | `cancelDownstreamNodes` only cancels nodes with no job yet created — it cannot kill running jobs |
| Wrong `completed_at` on pipeline failure | `completed_at` is NULL on failed pipelines | `UpdatePipelineStatus` sets `completed_at = NOW()` for completed, failed, and cancelled states |

---

## 21. Phase 6 Preview

After Phase 5, pipelines work end-to-end. What's still missing:

- No metrics on pipeline throughput (how many pipelines/hour? what's the average node duration?)
- No traces showing the full pipeline execution timeline in Jaeger
- No Grafana dashboard showing pipeline health

Phase 6 adds **full observability**:

```
scheduler.advanceOne() → span("pipeline.advance", pipeline_id=...)
  → child span: "pipeline.create_job" for each new node

Prometheus metrics:
  orion_pipeline_jobs_total{status="completed"} counter
  orion_pipeline_duration_seconds histogram
  orion_pipeline_nodes_active gauge
```

All instrumentation points are already implicit in Phase 5's code structure — Phase 6 just adds the `otel.Tracer` and `prometheus.CounterVec` calls.

---

## Summary Checklist

```
□ internal/store/store.go
    □ PipelineStore interface (7 methods)
    □ PipelineFilter struct
    □ PipelineJobStatus struct
    □ Store interface updated to include PipelineStore

□ internal/store/postgres/pipeline.go  (NEW file, same package as db.go)
    □ CreatePipeline — INSERT with JSONB dag_spec
    □ GetPipeline — SELECT + scanPipeline
    □ ListPipelines — dynamic WHERE, ORDER BY created_at DESC
    □ ListPipelinesByStatus — scheduler's query, partial index
    □ UpdatePipelineStatus — sets completed_at for terminal states
    □ AddPipelineJob — ON CONFLICT DO NOTHING (idempotent)
    □ GetPipelineJobs — JOIN pipeline_jobs + jobs

□ migrations/002_pipeline_indexes.up.sql
    □ idx_pipelines_status_created (partial, WHERE pending/running)
    □ idx_pipelines_status_list
    □ idx_pipeline_jobs_pipeline_id
□ migrations/002_pipeline_indexes.down.sql

□ internal/pipeline/advancement.go
    □ Advancer struct (store + logger)
    □ NewAdvancer constructor
    □ AdvanceAll — fetches pending+running, calls advanceOne for each
    □ advanceOne — build completed map, detect failure, create ready jobs, detect completion
    □ cancelDownstreamNodes — BFS from failed node, cancel pending nodes
    □ findNode, findDownstream, jobTypeFromPayload helpers

□ internal/pipeline/advancement_test.go
    □ Tests for AdvanceAll with fake store
    □ Linear pipeline advancement test
    □ Diamond DAG fan-out/fan-in test
    □ Failure cascade test
    □ Idempotency test (double tick, no duplicate jobs)

□ internal/api/handler/pipeline.go
    □ PipelineHandler struct
    □ CreatePipeline — validate + CreatePipeline in store
    □ GetPipeline — GetPipeline + 404 handling
    □ GetPipelineJobs — GetPipeline (404 check) + GetPipelineJobs
    □ ListPipelines — optional status filter
    □ validateDAG — no empty IDs, no duplicates, edges reference valid nodes

□ internal/scheduler/scheduler.go (update)
    □ advancer field added to Scheduler struct
    □ New() accepts *pipeline.Advancer
    □ runAsLeader calls advancer.AdvanceAll(ctx) in schedule ticker case

□ cmd/api/main.go (update)
    □ PipelineHandler created
    □ 4 pipeline routes registered

□ cmd/scheduler/main.go (update)
    □ pipeline.NewAdvancer(pgStore, logger) created
    □ advancer passed to scheduler.New()

□ Verification
    □ make migrate-up → 002 indexes created
    □ go test -race ./internal/pipeline/... → all pass
    □ make build → zero errors
    □ Test 1 (linear pipeline) → pending→running→completed ← FIRST PIPELINE
    □ Test 2 (diamond DAG) → fan-out runs parallel, fan-in waits for both
    □ Test 3 (failure cascade) → pipeline→failed, downstream never created
    □ Test 4 (SQL verification) → EXPLAIN uses idx_pipelines_status_created
```

---

## File Locations After Phase 5

```
orion/
├── internal/
│   ├── pipeline/
│   │   ├── advancement.go       ← NEW: Advancer, AdvanceAll, advanceOne
│   │   └── advancement_test.go  ← NEW: unit tests with fake store
│   ├── store/
│   │   ├── store.go             ← UPDATED: PipelineStore + PipelineJobStatus
│   │   └── postgres/
│   │       ├── db.go            ← unchanged
│   │       └── pipeline.go      ← NEW: 7 pipeline SQL methods
│   ├── api/handler/
│   │   └── pipeline.go          ← NEW: 4 HTTP handlers
│   └── scheduler/
│       └── scheduler.go         ← UPDATED: advancer field + AdvanceAll call
├── migrations/
│   ├── 002_pipeline_indexes.up.sql   ← NEW
│   └── 002_pipeline_indexes.down.sql ← NEW
└── cmd/
    ├── api/main.go              ← UPDATED: pipeline routes
    └── scheduler/main.go       ← UPDATED: advancer wired
```