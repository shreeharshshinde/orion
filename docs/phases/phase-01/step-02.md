# Step 02 — Domain Types (`internal/domain/`)

## What is this folder?

The `domain` package is the **heart of the entire project**. It defines the data structures (types) that everything else works with.

Think of it like this: before you build a car factory, you decide what a "car" is — how many wheels, what an engine looks like, what states it can be in (parked, moving, broken). That's what domain types do for code.

**Rule:** The `domain` package has **zero external dependencies**. It imports nothing from Redis, PostgreSQL, Kubernetes, or any third-party library. This is intentional — if you change your database from Postgres to MySQL tomorrow, the domain types don't change at all.

## Files in this step

### `job.go` — The central entity

A `Job` is one unit of ML work. It could be:
- A training script to run inside Kubernetes (`k8s_job`)
- A Go function to run inline in the worker process (`inline`)

**Key concepts in `job.go`:**

#### JobStatus — the state machine
```
queued → scheduled → running → completed
                   ↘ failed → retrying → queued (retry loop)
                            ↘ dead      (gave up)
         cancelled (any time before running)
```

Every job moves through these states in one direction. You can't go from `completed` back to `running`. This is enforced by `ValidTransitions`:

```go
var ValidTransitions = map[JobStatus][]JobStatus{
    JobStatusQueued:    {JobStatusScheduled, JobStatusCancelled},
    JobStatusScheduled: {JobStatusRunning, JobStatusQueued, JobStatusCancelled},
    JobStatusRunning:   {JobStatusCompleted, JobStatusFailed},
    // ...
}
```

#### Why a state machine?
Imagine two workers both try to run the same job at the same time. Without a state machine, both succeed and you run the job twice (double billing, double training, double mess). With a state machine + CAS (Compare-And-Swap), only ONE worker can move a job from `scheduled` to `running`. The other gets rejected.

#### JobPriority — who goes first
```go
PriorityLow      = 3   // batch jobs, not urgent
PriorityNormal   = 5   // default
PriorityHigh     = 8   // important training runs
PriorityCritical = 10  // drop everything and run this
```

The scheduler always picks higher-priority jobs first.

#### JobPayload — what to actually run
```go
type JobPayload struct {
    HandlerName    string         // for inline: which Go function to call
    Args           map[string]any // arguments to pass to the function
    KubernetesSpec *KubernetesSpec // for k8s_job: what container to launch
}
```

#### KubernetesSpec — how to launch the ML container
```go
type KubernetesSpec struct {
    Image           string   // "pytorch/pytorch:2.0"
    Command         []string // ["python", "train.py"]
    ResourceRequest ResourceRequest // CPU: "2", Memory: "8Gi", GPU: 1
    Namespace       string   // "ml-jobs"
    // ...
}
```

#### Methods on Job
```go
job.CanTransitionTo(JobStatusRunning) // is this transition allowed?
job.IsTerminal()   // completed/dead/cancelled — no more transitions
job.IsRetryable()  // failed AND still has retries left
```

---

### `worker.go` — Worker registration

A `Worker` is one running instance of the worker process. Multiple workers can run in parallel across different machines.

```go
type Worker struct {
    ID           string       // unique ID, usually hostname
    QueueNames   []string     // which queues this worker pulls from
    Concurrency  int          // how many jobs it can run at once
    ActiveJobs   int          // how many it's running right now
    Status       WorkerStatus // idle / busy / draining / offline
    LastHeartbeat time.Time   // last "I'm alive" signal
}
```

**Why do we need this?**

The scheduler needs to know which workers are alive. If a worker crashes, its jobs get stuck in `running` forever. By checking `LastHeartbeat`, the scheduler can detect dead workers and reclaim their jobs.

```go
worker.IsAlive(45 * time.Second)  // has it heartbeated recently?
worker.AvailableSlots()           // how many more jobs can it take?
```

---

### `pipeline.go` — DAG pipelines (Phase 8)

A `Pipeline` is a sequence of jobs where some jobs depend on others completing first.

```
preprocess_data → train_model → evaluate_model → deploy_model
```

This is a **DAG** (Directed Acyclic Graph) — jobs flow in one direction with no cycles.

```go
type DAGSpec struct {
    Nodes []DAGNode // each step in the pipeline
    Edges []DAGEdge // "A must complete before B starts"
}
```

The `ReadyNodes()` method answers: *"given these completed nodes, which ones can start now?"*

```go
completed := map[string]bool{"preprocess_data": true}
ready := dag.ReadyNodes(completed)
// returns: ["train_model"] — its dependency is satisfied
```

---

## File locations
```
orion/
└── internal/
    └── domain/
        ├── job.go       ← Job, JobStatus, JobPayload, KubernetesSpec
        ├── worker.go    ← Worker, WorkerStatus
        └── pipeline.go  ← Pipeline, DAGSpec, DAGNode, DAGEdge
```

## The "internal" directory — why?

Go has a special rule: code inside `internal/` **cannot be imported by code outside your module**. This protects your implementation details. External users of the Orion library can only use what you put in `pkg/` (public packages). Everything in `internal/` is private to Orion itself.

## Next step
Now that we know what a `Job` is, we need to define **how to configure the system** — what database to connect to, what Redis address to use, etc. Next: the Config package.