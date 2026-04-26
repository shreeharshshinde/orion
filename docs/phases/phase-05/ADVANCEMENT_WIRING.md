# Phase 5 — Advancement Engine, Handler, Scheduler, and Wiring
## `advancement.go` · `pipeline.go` (handler) · `scheduler.go` · `cmd/` updates

---

## `internal/pipeline/advancement.go` — The DAG Engine

This is the most important new file in Phase 5. It contains all the intelligence for moving pipelines forward — deciding which nodes are ready, creating their jobs, detecting completion and failure. Everything else in Phase 5 is plumbing that connects this engine to the rest of the system.

### Package placement: `internal/pipeline`

The advancement logic lives in its own package rather than inside the scheduler because:

1. **Testability** — `advancement_test.go` tests the algorithm in complete isolation with a fake store. No scheduler infrastructure needed.
2. **Clarity** — the scheduler stays thin: it just calls `advancer.AdvanceAll(ctx)`. The algorithm itself is easy to find.
3. **Future** — Phase 6 will add metrics and tracing to `advancePipeline`. Having it in its own package means those additions don't pollute the scheduler.

### The Advancer struct

```go
type Advancer struct {
    store  store.Store
    logger *slog.Logger
}
```

Stateless. All pipeline state lives in PostgreSQL. The Advancer reads state, computes what to do, and writes the results back. It holds no in-memory pipeline state between ticks.

### `AdvanceAll(ctx)` — the tick entry point

Called by the scheduler every 2 seconds. Does two separate SQL queries (one for `pending`, one for `running`) rather than a single `WHERE status IN (...)` query, because two queries each hitting the partial index `idx_pipelines_status_created` are faster than one query that can't use a partial index.

For each pipeline found, calls `advanceOne`. If `advanceOne` returns an error for one pipeline, that error is logged and iteration continues — a broken pipeline must never block advancement of healthy ones.

### `advanceOne(ctx, pipeline)` — the algorithm step by step

```
Step 1: GetPipelineJobs(pipelineID)
        → [{node_id, job_id, job_status}] via one JOIN query

Step 2: Build maps
        completed:    {node_id → true}  where job_status == "completed"
        createdNodes: {node_id → *PipelineJobStatus}  (all nodes with any job)

Step 3: Failure detection
        For each pipelineJob: if job_status == "dead":
          → logCascadeCancellation (logs which downstream nodes will not start)
          → UpdatePipelineStatus(pipelineID, "failed")
          → return early

Step 4: pending → running transition
        If pipeline.Status == "pending":
          → UpdatePipelineStatus(pipelineID, "running")
          (only happens on the very first tick that touches this pipeline)

Step 5: ReadyNodes(completed)
        Pure function on DAGSpec. Returns node IDs whose ALL deps are in completed.
        On first tick: returns all root nodes (nodes with no incoming edges).

Step 6: For each ready node NOT in createdNodes:
          job = CreateJob({name: "pipeline-name/node-id", ...node.JobTemplate})
          AddPipelineJob(pipelineID, nodeID, job.ID)
          (ON CONFLICT DO NOTHING makes AddPipelineJob idempotent)

Step 7: Completion detection
        if allNodesCompleted(dag, completed):
          → UpdatePipelineStatus(pipelineID, "completed")
```

### Why the `alreadyCreated` guard is the most important line

```go
if _, exists := createdNodes[nodeID]; exists {
    continue  // ← this line prevents duplicate job creation
}
```

Without this check: if the scheduler runs `advanceOne` twice in rapid succession (e.g., two ticks fire before any DB write propagates), both calls would see the same `completed` set, call `ReadyNodes` with the same input (same output), and both would call `CreateJob` for the same ready nodes — creating duplicate jobs.

With this check: the second call finds all ready nodes already in `createdNodes` (they were added by `AddPipelineJob` in the first call) and skips `CreateJob` for all of them. Zero duplicates regardless of tick frequency.

### `findDownstream(dag, startNodeID)` — BFS cascade

Used when a node reaches `dead` status to find all downstream nodes that will never run:

```
dag: preprocess → train → evaluate → deploy
findDownstream("train") = ["evaluate", "deploy"]
```

BFS implementation: build adjacency list (source→[]targets), then breadth-first traverse from `startNodeID`. Does NOT include the start node itself — only nodes reachable _from_ it.

In Phase 5, downstream nodes are only logged (not explicitly cancelled in the DB). Phase 6 enhancement: create explicit `cancelled` job records for downstream nodes so they appear in `GET /pipelines/{id}/jobs` with `status=cancelled`.

### `allNodesCompleted(dag, completed)` — the completion gate

```go
func allNodesCompleted(dag domain.DAGSpec, completed map[string]bool) bool {
    if len(dag.Nodes) == 0 {
        return false  // guard: empty DAG never "completes"
    }
    for _, node := range dag.Nodes {
        if !completed[node.ID] {
            return false
        }
    }
    return true
}
```

Checks the DAG spec (ground truth) rather than `pipeline_jobs` count because `pipeline_jobs` may have a one-tick lag after `AddPipelineJob` runs.

---

## `internal/api/handler/pipeline.go` — HTTP Handler

Four routes, one handler struct, one validation function.

### Route summary

| Method | Path | Handler | Purpose |
|---|---|---|---|
| `POST` | `/pipelines` | `CreatePipeline` | Submit a new DAG pipeline |
| `GET` | `/pipelines` | `ListPipelines` | List pipelines (optional `?status=` filter) |
| `GET` | `/pipelines/{id}` | `GetPipeline` | Get pipeline status + DAG spec |
| `GET` | `/pipelines/{id}/jobs` | `GetPipelineJobs` | Get all node→job mappings with live status |

### `CreatePipeline` — the most important handler

Validates the DAG spec before persisting. The validation catches the most common mistakes:

```
1. name is required and non-blank
2. dag_spec.nodes must have ≥ 1 node
3. Each node must have a non-empty, unique ID
4. Each node must specify either handler_name OR kubernetes_spec (not neither, not both)
5. Each edge source/target must reference a real node ID
6. No self-loops (source == target)
```

After validation, inserts the pipeline in `pending` status. The scheduler picks it up within 2 seconds.

### `GetPipelineJobs` — why it checks pipeline existence first

```go
// Verify pipeline exists before querying pipeline_jobs.
if _, err := h.store.GetPipeline(r.Context(), id); err != nil {
    if errors.Is(err, store.ErrNotFound) {
        writeError(w, http.StatusNotFound, "pipeline not found")
        return
    }
    ...
}
```

Without this check, `GetPipelineJobs` would return `200 {"jobs": [], "count": 0}` for any UUID that happens to have no pipeline_jobs rows — including completely invalid UUIDs. The pipeline existence check makes the response semantically correct: `404` for unknown pipelines, `200 {"count": 0}` only for real pipelines that haven't started yet.

### Status validation in `ListPipelines`

```go
func isValidPipelineStatus(s domain.PipelineStatus) bool {
    switch s {
    case domain.PipelineStatusPending,
        domain.PipelineStatusRunning,
        domain.PipelineStatusCompleted,
        domain.PipelineStatusFailed,
        domain.PipelineStatusCancelled:
        return true
    }
    return false
}
```

Returns `422 Unprocessable Entity` (not `400 Bad Request`) for invalid status values because the request body is valid JSON — the issue is a semantic constraint violation, not a parse failure.

---

## `internal/scheduler/scheduler.go` — What Changed

Two surgical changes to the existing file. Nothing removed. Nothing restructured.

### Change 1 — New `advancer` field + updated `New()` signature

```go
// Before:
type Scheduler struct {
    cfg    Config
    db     *pgxpool.Pool
    store  store.Store
    queue  queue.Queue
    logger *slog.Logger
}

func New(cfg Config, db *pgxpool.Pool, s store.Store, q queue.Queue, logger *slog.Logger) *Scheduler

// After:
type Scheduler struct {
    cfg      Config
    db       *pgxpool.Pool
    store    store.Store
    queue    queue.Queue
    advancer *pipeline.Advancer  // ← added
    logger   *slog.Logger
}

func New(cfg Config, db *pgxpool.Pool, s store.Store, q queue.Queue,
    adv *pipeline.Advancer, logger *slog.Logger) *Scheduler
```

### Change 2 — `AdvanceAll` call in the schedule ticker case

```go
// Before (runAsLeader schedule ticker):
case <-scheduleTicker.C:
    if err := s.scheduleQueuedJobs(ctx); err != nil { ... }
    if err := s.promoteRetryableJobs(ctx); err != nil { ... }

// After:
case <-scheduleTicker.C:
    if err := s.scheduleQueuedJobs(ctx); err != nil { ... }
    if err := s.promoteRetryableJobs(ctx); err != nil { ... }
    // Phase 5: advance all active pipelines
    if s.advancer != nil {
        if err := s.advancer.AdvanceAll(ctx); err != nil { ... }
    }
```

### Why advancement runs on the schedule ticker (not orphan ticker)

The schedule ticker fires every 2 seconds. The orphan ticker fires every 30 seconds. Pipeline advancement is time-sensitive: when a node completes, the next node should start within 2 seconds, not 30 seconds. Running on the schedule ticker gives the fastest possible response to node completion.

### Why `s.advancer != nil` guard

The nil guard allows the scheduler to run without pipeline support if `NewAdvancer` fails to construct (future error handling). In practice, `NewAdvancer` never fails — it's always non-nil. The guard exists as a safety net for testing scenarios where the scheduler is constructed without an advancer.

---

## `cmd/api/main.go` — What Changed

One new block after job route registration:

```go
// ── Pipeline routes [Phase 5] ─────────────────────────────────────────────
pipelineHandler := handler.NewPipelineHandler(pgStore, logger)
mux.HandleFunc("POST /pipelines",           pipelineHandler.CreatePipeline)
mux.HandleFunc("GET /pipelines",            pipelineHandler.ListPipelines)
mux.HandleFunc("GET /pipelines/{id}",       pipelineHandler.GetPipeline)
mux.HandleFunc("GET /pipelines/{id}/jobs",  pipelineHandler.GetPipelineJobs)
```

`pgStore` already satisfies `store.Store` including `PipelineStore` after `pipeline.go` is added. No other changes.

## `cmd/scheduler/main.go` — What Changed

Two additions:

**New import:** `"github.com/shreeharsh-a/orion/internal/pipeline"`

**New block before `scheduler.New()`:**

```go
// ── 7. Pipeline Advancer [Phase 5] ────────────────────────────────────────
adv := pipeline.NewAdvancer(pgStore, logger)
logger.Info("pipeline advancer ready")
```

**Updated `scheduler.New()` call:** `adv` passed as fifth argument before `logger`:

```go
sched := scheduler.New(
    scheduler.Config{...},
    db,
    pgStore,
    queue,
    adv,    // ← Phase 5
    logger,
)
```

---

## Test counts — Phase 5 additions

| File | Tests | Type |
|---|---|---|
| `internal/pipeline/advancement_test.go` | 15 | Unit (fake store) |
| `internal/api/handler/pipeline_test.go` | 22 | Unit (fake store) |

```bash
# Run advancement tests
go test -race ./internal/pipeline/... -v

# Run handler tests
go test -race ./internal/api/handler/... -v

# Run everything
go test -race ./... -count=1
```

---

## File locations after Phase 5

```
orion/
├── internal/
│   ├── pipeline/
│   │   ├── advancement.go       ← NEW
│   │   └── advancement_test.go  ← NEW  (15 tests)
│   ├── store/
│   │   ├── store.go             ← UPDATED (PipelineStore + 2 types)
│   │   └── postgres/
│   │       ├── db.go            ← unchanged
│   │       └── pipeline.go      ← NEW (7 SQL methods)
│   ├── api/handler/
│   │   ├── job.go               ← unchanged
│   │   ├── pipeline.go          ← NEW (4 HTTP handlers)
│   │   └── pipeline_test.go     ← NEW (22 tests)
│   └── scheduler/
│       └── scheduler.go         ← UPDATED (advancer field + AdvanceAll call)
├── internal/store/migrations/
│   ├── 002_pipeline_indexes.up.sql   ← NEW
│   └── 002_pipeline_indexes.down.sql ← NEW
└── cmd/
    ├── api/main.go              ← UPDATED (4 pipeline routes)
    └── scheduler/main.go        ← UPDATED (advancer wired)
```