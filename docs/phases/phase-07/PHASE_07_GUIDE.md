# Orion — Phase 7 Master Guide
## gRPC Streaming API: Real-Time Job and Pipeline Events

> **What this document is:** Complete planning for Phase 7 — the mental model for gRPC streaming in Go, every file to create, the exact proto definition, how the broadcaster works, how polling transitions to PG NOTIFY in Phase 8, and the precise output that proves Phase 7 is done.

---

## Table of Contents

1. [Why Phase 7 Exists — The Gap It Closes](#1-why-phase-7-exists)
2. [What Changes: Before and After](#2-what-changes-before-and-after)
3. [Mental Model — How gRPC Streaming Works in Go](#3-mental-model--how-grpc-streaming-works-in-go)
4. [The Event Fan-out Architecture](#4-the-event-fan-out-architecture)
5. [What Already Exists](#5-what-already-exists)
6. [Complete File Plan](#6-complete-file-plan)
7. [File 1: `proto/orion/v1/jobs.proto`](#7-file-1-proto)
8. [File 2: `internal/api/grpc/broadcaster.go`](#8-file-2-broadcaster)
9. [File 3: `internal/api/grpc/server.go`](#9-file-3-grpc-server)
10. [File 4: `internal/api/grpc/server_test.go`](#10-file-4-grpc-tests)
11. [File 5: `internal/store/postgres/db.go` — Event Emission](#11-file-5-store-event-emission)
12. [File 6: `cmd/api/main.go` — gRPC Server Startup](#12-file-6-cmd-wiring)
13. [File 7: `Makefile` — Proto Generation Target](#13-file-7-makefile)
14. [Streaming Internals — How Events Flow](#14-streaming-internals)
15. [The Polling-to-PG-NOTIFY Evolution Path](#15-the-polling-to-pg-notify-evolution-path)
16. [Error Handling and Stream Lifecycle](#16-error-handling-and-stream-lifecycle)
17. [Step-by-Step Build Order](#17-step-by-step-build-order)
18. [Complete End-to-End Test Sequence](#18-complete-end-to-end-test-sequence)
19. [gRPC vs HTTP — When to Use Which](#19-grpc-vs-http--when-to-use-which)
20. [Common Mistakes](#20-common-mistakes)
21. [Phase 8 Preview](#21-phase-8-preview)

---

## 1. Why Phase 7 Exists

After Phase 6, Orion is fully observable. But clients still must **poll** to track job progress:

```python
# Current situation — polling loop required
while True:
    resp = requests.get(f"/jobs/{job_id}")
    if resp.json()["status"] in ("completed", "dead", "failed"):
        break
    time.sleep(1)  # 1s polling = up to 1s lag per transition
```

This has three problems:

**Problem 1 — Latency.** A job transitions `running → completed` at time T. The client discovers it at T+polling_interval. With 1s polling this is acceptable; with 10s polling (reasonable for long jobs) a data scientist waits up to 10 extra seconds.

**Problem 2 — Load.** 100 clients watching 100 jobs = 100 GET requests/second to the API server. All those requests do nothing except confirm "still running." This is pure waste.

**Problem 3 — SDK complexity.** Every language SDK must implement a polling loop. gRPC streaming reduces this to a single `for event in stub.WatchJob(req):` iterator — the polling loop disappears entirely.

Phase 7 adds a gRPC streaming API. The client opens one connection. Events arrive milliseconds after each state transition. Zero polling.

---

## 2. What Changes: Before and After

### Phase 6 state (what you have now)

```
HTTP :8080 — POST /jobs, GET /jobs/{id}, GET /pipelines, ...
gRPC :9090 — nothing (not started)
```

### Phase 7 state (what you will have)

```
HTTP :8080 — all existing routes unchanged
gRPC :9090 — JobService with 4 RPCs:
  SubmitJob(SubmitJobRequest) → Job                    (unary)
  GetJob(GetJobRequest)       → Job                    (unary)
  WatchJob(WatchJobRequest)   → stream JobEvent        (server-streaming)
  WatchPipeline(...)          → stream PipelineEvent   (server-streaming)
```

The gRPC server runs in the same `cmd/api` binary alongside the HTTP server. Two listeners, one process, one store.

### What gRPC streaming looks like to a client

```go
// Go client — zero polling loop
stream, _ := client.WatchJob(ctx, &orionv1.WatchJobRequest{JobId: jobID})
for {
    event, err := stream.Recv()
    if err == io.EOF { break }  // job reached terminal state, stream closed by server
    fmt.Printf("%s → %s\n", event.PreviousStatus, event.NewStatus)
    // queued → scheduled
    // scheduled → running
    // running → completed
}
```

```python
# Python client — same simplicity
for event in stub.WatchJob(WatchJobRequest(job_id=job_id)):
    print(f"{event.previous_status} → {event.new_status}")
```

---

## 3. Mental Model — How gRPC Streaming Works in Go

### gRPC vs HTTP — fundamental difference

HTTP is request-response: client sends one message, server sends one message, connection closes.

gRPC server-streaming is different: client sends one message, server sends **many** messages over time, connection stays open until the server calls `return nil` (success) or `return err` (failure).

```
Client                          Server
  |-- WatchJob(job_id=abc) --→  |
  |←-- event: queued→scheduled  |
  |←-- event: scheduled→running |
  |                             | (server waits — stream stays open)
  |←-- event: running→completed |
  |←-- EOF (stream closed)     |
```

### The Go server-streaming signature

```go
// Generated by protoc — you implement this interface:
type JobServiceServer interface {
    SubmitJob(context.Context, *SubmitJobRequest) (*Job, error)
    GetJob(context.Context, *GetJobRequest) (*Job, error)
    WatchJob(*WatchJobRequest, JobService_WatchJobServer) error
    WatchPipeline(*WatchPipelineRequest, JobService_WatchPipelineServer) error
}

// The stream object for WatchJob:
type JobService_WatchJobServer interface {
    Send(*JobEvent) error           // push one event to the client
    Context() context.Context       // check if client disconnected
    grpc.ServerStream               // embed base stream interface
}
```

When you call `stream.Send(event)`, gRPC serialises the protobuf message and writes it to the HTTP/2 connection immediately. The client receives it within milliseconds.

### How the server detects client disconnect

The stream's `context.Context` is cancelled when the client disconnects (closed the connection, cancelled the request, network failure). Your implementation must check `stream.Context().Done()`:

```go
func (s *Server) WatchJob(req *WatchJobRequest, stream JobService_WatchJobServer) error {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-stream.Context().Done():
            return nil  // client gone — clean exit
        case event := <-s.broadcaster.Subscribe(req.JobId):
            if err := stream.Send(event); err != nil {
                return err  // write failed (client gone)
            }
            if isTerminal(event.NewStatus) {
                return nil  // job done — close the stream
            }
        case <-ticker.C:
            // polling fallback — check DB directly
        }
    }
}
```

---

## 4. The Event Fan-out Architecture

### The problem: N clients watching the same job

If 10 clients are watching job `abc123`, and the job transitions `running → completed`, all 10 need to receive the event. But the state transition happens in a single `MarkJobCompleted()` call in the worker.

The solution: a **Broadcaster** — an in-memory pub/sub hub.

```
Worker calls store.MarkJobCompleted(job_id)
    ↓
store.MarkJobCompleted emits to Broadcaster.Publish(job_id, event)
    ↓
Broadcaster fans out to all subscribers watching job_id:
    WatchJob goroutine (client 1) ← event
    WatchJob goroutine (client 2) ← event
    WatchJob goroutine (client 3) ← event
    ... (all 10 clients receive simultaneously)
```

### The Broadcaster interface

```go
// internal/api/grpc/broadcaster.go

type EventBroadcaster interface {
    // Publish sends an event to all subscribers watching the given job or pipeline.
    // Called by the store layer on every state transition.
    Publish(id string, event *orionv1.JobEvent)

    // Subscribe returns a channel that receives events for the given ID.
    // The channel is buffered (capacity 16) — slow consumers don't block publishers.
    // Call Unsubscribe when done to prevent goroutine/memory leaks.
    Subscribe(id string) (<-chan *orionv1.JobEvent, func())
}
```

### Why buffered channels

The subscriber channel is buffered (capacity 16). If the client's WatchJob goroutine is slow to call `stream.Send`, the Broadcaster's `Publish` call does NOT block — it writes to the buffer and returns immediately. This prevents one slow client from blocking the worker pool's `MarkJobCompleted` call.

If the buffer fills (16 events behind), the 17th event is **dropped**. This is safe because:
1. In practice, state transitions are seconds apart — a 16-event buffer never fills
2. The polling fallback in `WatchJob` catches missed events

### Why not PostgreSQL LISTEN/NOTIFY in Phase 7

`pg_notify` would be the ideal solution — the DB fires a notification on every `jobs` row update, and any subscriber receives it in real-time without polling. This eliminates the broadcaster entirely.

Phase 7 uses **polling as the primary mechanism** (every 500ms) with the broadcaster as the fast path. This is simpler to implement and test. Phase 8 replaces the polling loop with `LISTEN/NOTIFY` for sub-10ms latency.

The broadcaster remains — it handles the case where the API server and the DB are in the same datacenter and the polling is fast enough.

---

## 5. What Already Exists

| What | Where | Status |
|---|---|---|
| `google.golang.org/grpc v1.62.1` | `go.mod` | Already in `go.mod` — no `go get` needed |
| `google.golang.org/protobuf v1.33.0` | `go.mod` | Already there |
| `store.Store` interface | `internal/store/store.go` | All methods already defined |
| `domain.Job`, `domain.Pipeline` | `internal/domain/` | All types already exist |
| HTTP handlers | `internal/api/handler/` | Untouched — gRPC is additive |
| `cmd/api/main.go` | API server binary | Updated to add gRPC listener |
| `observability.Tracer()` | `internal/observability/` | Ready — gRPC spans use same tracer |

### Does NOT exist yet — Phase 7 builds

```
proto/orion/v1/jobs.proto                   ← NEW: service definition
proto/orion/v1/jobs.pb.go                   ← GENERATED by protoc
proto/orion/v1/jobs_grpc.pb.go              ← GENERATED by protoc
internal/api/grpc/broadcaster.go            ← NEW: in-memory pub/sub
internal/api/grpc/server.go                 ← NEW: gRPC service implementation
internal/api/grpc/server_test.go            ← NEW: in-memory gRPC tests
cmd/api/main.go                              ← UPDATED: gRPC server on :9090
Makefile                                     ← UPDATED: proto generation target
```

---

## 6. Complete File Plan

Phase 7 adds 6 new files and updates 2 existing files.

```
proto/
└── orion/v1/
    ├── jobs.proto              ← NEW: service + message definitions
    ├── jobs.pb.go              ← GENERATED (protoc) — do not edit
    └── jobs_grpc.pb.go         ← GENERATED (protoc) — do not edit

internal/api/grpc/
├── broadcaster.go              ← NEW: in-memory event fan-out hub
├── server.go                   ← NEW: gRPC service implementation
└── server_test.go              ← NEW: in-memory conn tests (bufconn)

cmd/api/
└── main.go                     ← UPDATED: start gRPC on :9090 + register service

Makefile                        ← UPDATED: add proto-gen target
```

### What is NOT changing

| File | Why |
|---|---|
| HTTP handlers (`handler/job.go`, `handler/pipeline.go`) | gRPC is purely additive |
| Store (`store/store.go`, `postgres/db.go`) | Store is called by gRPC server — no interface changes |
| Worker pool (`worker/pool.go`) | Worker calls `MarkJobCompleted` which triggers broadcaster |
| Scheduler (`scheduler/scheduler.go`) | Unchanged |
| Migrations | No new tables needed for Phase 7 |

---

## 7. File 1: `proto/orion/v1/jobs.proto`

The proto file is the **contract** between the server and all clients. Once published, changing it requires versioning (the `v1` in the package name).

### Complete proto definition

```protobuf
syntax = "proto3";

package orion.v1;

option go_package = "github.com/shreeharsh-a/orion/proto/orion/v1;orionv1";

import "google/protobuf/timestamp.proto";

// ─────────────────────────────────────────────────────────────────────────────
// JobService — the primary gRPC service for Orion
// ─────────────────────────────────────────────────────────────────────────────

service JobService {
  // SubmitJob creates a new job. Equivalent to POST /jobs.
  // Returns the created job with its assigned ID.
  rpc SubmitJob(SubmitJobRequest) returns (Job);

  // GetJob retrieves a job by ID. Equivalent to GET /jobs/{id}.
  rpc GetJob(GetJobRequest) returns (Job);

  // WatchJob opens a server-streaming connection that receives one JobEvent
  // message for every status transition until the job reaches a terminal state
  // (completed, dead, cancelled). The stream closes automatically at terminal.
  //
  // Usage: submit a job, then call WatchJob with the returned job_id.
  // The stream sends the current status immediately (as first event),
  // then subsequent events as transitions happen.
  rpc WatchJob(WatchJobRequest) returns (stream JobEvent);

  // WatchPipeline opens a server-streaming connection for a pipeline.
  // Receives one PipelineEvent for every node status change.
  // Stream closes when the pipeline reaches a terminal state.
  rpc WatchPipeline(WatchPipelineRequest) returns (stream PipelineEvent);
}

// ─────────────────────────────────────────────────────────────────────────────
// Request messages
// ─────────────────────────────────────────────────────────────────────────────

message SubmitJobRequest {
  string name = 1;                    // human-readable job name
  string type = 2;                    // "inline" or "k8s_job"
  string queue_name = 3;              // "high", "default", "low" (optional)
  int32  priority = 4;                // 1-10, default 5
  int32  max_retries = 5;             // default 3
  JobPayload payload = 6;             // handler config + args
  string idempotency_key = 7;         // optional: prevents duplicate submissions
}

message GetJobRequest {
  string job_id = 1;                  // UUID of the job
}

message WatchJobRequest {
  string job_id = 1;                  // UUID of the job to watch
}

message WatchPipelineRequest {
  string pipeline_id = 1;             // UUID of the pipeline to watch
}

// ─────────────────────────────────────────────────────────────────────────────
// Core types
// ─────────────────────────────────────────────────────────────────────────────

message Job {
  string                    job_id       = 1;
  string                    name         = 2;
  string                    type         = 3;
  string                    status       = 4;
  string                    queue_name   = 5;
  int32                     priority     = 6;
  int32                     attempt      = 7;
  int32                     max_retries  = 8;
  string                    worker_id    = 9;
  string                    error_message = 10;
  google.protobuf.Timestamp created_at   = 11;
  google.protobuf.Timestamp updated_at   = 12;
  google.protobuf.Timestamp completed_at = 13;
  JobPayload                payload      = 14;
}

message JobPayload {
  string              handler_name = 1;     // for inline jobs
  map<string, string> args         = 2;     // handler arguments
  KubernetesSpec      kubernetes_spec = 3;  // for k8s_job type
}

message KubernetesSpec {
  string              image          = 1;
  repeated string     command        = 2;
  repeated string     args           = 3;
  map<string, string> env_vars       = 4;
  string              namespace      = 5;
  ResourceRequest     resources      = 6;
  string              service_account = 7;
  int32               ttl_seconds    = 8;
}

message ResourceRequest {
  string cpu    = 1;   // e.g. "4" or "500m"
  string memory = 2;   // e.g. "16Gi"
  int32  gpu    = 3;   // nvidia.com/gpu count
}

message JobEvent {
  string                    job_id          = 1;
  string                    job_name        = 2;
  string                    previous_status = 3;
  string                    new_status      = 4;
  string                    worker_id       = 5;
  string                    error_message   = 6;
  int32                     attempt         = 7;
  google.protobuf.Timestamp timestamp       = 8;
}

message PipelineEvent {
  string                    pipeline_id     = 1;
  string                    pipeline_name   = 2;
  string                    node_id         = 3;   // which node changed
  string                    node_status     = 4;   // new status of that node
  string                    pipeline_status = 5;   // overall pipeline status
  google.protobuf.Timestamp timestamp       = 6;
}
```

### Proto design decisions

**Why `map<string,string>` for args and env_vars, not `google.protobuf.Struct`?**
`map<string,string>` is typed and zero-allocation in Go's protobuf codegen. `Struct` requires `structpb.NewStruct()` which allocates. Since args are always string-keyed string-or-number values, the map is sufficient and faster.

**Why `stream JobEvent` and not a single response with repeated events?**
With `repeated JobEvent`, the server must buffer ALL events in memory before sending. With streaming, each event is sent immediately when it occurs. For a 4-hour training run, streaming is the only viable approach.

**Why are both `SubmitJob` and `GetJob` duplicated from HTTP?**
SDK clients should not need both an HTTP and a gRPC client. Providing the full CRUD surface in gRPC means SDK authors only need one client. The HTTP API remains for web browsers and curl users.

---

## 8. File 2: `internal/api/grpc/broadcaster.go`

The broadcaster is the event distribution hub. It lives in the API server process and is shared between the gRPC server and the store layer.

### Complete code to write

```go
// Package grpc implements the Orion gRPC service and its supporting types.
// broadcaster.go: in-memory pub/sub for real-time job and pipeline events.
package grpc

import (
    "sync"

    orionv1 "github.com/shreeharsh-a/orion/proto/orion/v1"
)

// Broadcaster is a thread-safe, in-memory fan-out hub for job events.
//
// Architecture:
//   - Each job ID maps to a set of subscriber channels.
//   - When a state transition occurs, Publish() fans out to all subscribers.
//   - Subscribers receive events via buffered channels (capacity 16).
//   - Slow consumers don't block publishers — overflow events are dropped.
//
// Memory safety:
//   - Subscribers MUST call the returned unsubscribe func when done.
//   - The unsubscribe func removes the channel from the map and closes it.
//   - Goroutine leak prevention: WatchJob's defer calls unsubscribe.
type Broadcaster struct {
    mu          sync.RWMutex
    subscribers map[string]map[uint64]chan *orionv1.JobEvent
    nextID      uint64
}

// NewBroadcaster creates an empty Broadcaster.
func NewBroadcaster() *Broadcaster {
    return &Broadcaster{
        subscribers: make(map[string]map[uint64]chan *orionv1.JobEvent),
    }
}

// Publish sends an event to all subscribers watching the given job ID.
// Non-blocking: if a subscriber's buffer is full, the event is dropped.
// Safe to call from multiple goroutines simultaneously.
func (b *Broadcaster) Publish(jobID string, event *orionv1.JobEvent) {
    b.mu.RLock()
    defer b.mu.RUnlock()

    subs, ok := b.subscribers[jobID]
    if !ok {
        return // no subscribers — fast path
    }
    for _, ch := range subs {
        select {
        case ch <- event: // sent
        default:          // buffer full — drop event (polling fallback catches it)
        }
    }
}

// Subscribe returns a buffered channel for events on the given job ID,
// and an unsubscribe function that MUST be called when the subscription is no longer needed.
//
// Pattern:
//   ch, unsub := broadcaster.Subscribe(jobID)
//   defer unsub()
//   for event := range ch { ... }
func (b *Broadcaster) Subscribe(jobID string) (<-chan *orionv1.JobEvent, func()) {
    b.mu.Lock()
    defer b.mu.Unlock()

    b.nextID++
    subID := b.nextID
    ch := make(chan *orionv1.JobEvent, 16)

    if b.subscribers[jobID] == nil {
        b.subscribers[jobID] = make(map[uint64]chan *orionv1.JobEvent)
    }
    b.subscribers[jobID][subID] = ch

    unsubscribe := func() {
        b.mu.Lock()
        defer b.mu.Unlock()
        if subs, ok := b.subscribers[jobID]; ok {
            delete(subs, subID)
            close(ch)
            if len(subs) == 0 {
                delete(b.subscribers, jobID)
            }
        }
    }
    return ch, unsubscribe
}

// SubscriberCount returns the number of active subscribers for a job ID.
// Used in tests and health-check metrics.
func (b *Broadcaster) SubscriberCount(jobID string) int {
    b.mu.RLock()
    defer b.mu.RUnlock()
    return len(b.subscribers[jobID])
}
```

---

## 9. File 3: `internal/api/grpc/server.go`

The gRPC service implementation. This is where the four RPC methods live.

### Complete code to write

```go
package grpc

import (
    "context"
    "errors"
    "time"

    "go.opentelemetry.io/otel/attribute"
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    "google.golang.org/protobuf/types/known/timestamppb"
    "log/slog"

    "github.com/google/uuid"
    "github.com/shreeharsh-a/orion/internal/domain"
    "github.com/shreeharsh-a/orion/internal/observability"
    "github.com/shreeharsh-a/orion/internal/store"
    orionv1 "github.com/shreeharsh-a/orion/proto/orion/v1"
)

const (
    // pollInterval is how often WatchJob polls the DB when no broadcaster
    // event arrives. 500ms gives acceptable latency in Phase 7.
    // Phase 8 replaces this with PG LISTEN/NOTIFY for sub-10ms latency.
    pollInterval = 500 * time.Millisecond
)

// Server implements orionv1.JobServiceServer.
// It holds a store (for DB reads) and a broadcaster (for real-time events).
type Server struct {
    orionv1.UnimplementedJobServiceServer // embed for forward compatibility
    store       store.Store
    broadcaster *Broadcaster
    logger      *slog.Logger
}

// NewServer creates a gRPC Server.
func NewServer(s store.Store, b *Broadcaster, logger *slog.Logger) *Server {
    return &Server{store: s, broadcaster: b, logger: logger}
}

// RegisterGRPCServer registers the Server with a grpc.Server.
// Called from cmd/api/main.go.
func RegisterGRPCServer(grpcSrv *grpc.Server, s *Server) {
    orionv1.RegisterJobServiceServer(grpcSrv, s)
}

// ─────────────────────────────────────────────────────────────────────────────
// SubmitJob — unary RPC
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) SubmitJob(ctx context.Context, req *orionv1.SubmitJobRequest) (*orionv1.Job, error) {
    ctx, span := observability.Tracer("orion.grpc").Start(ctx, "grpc.SubmitJob")
    defer span.End()

    if req.Name == "" {
        return nil, status.Error(codes.InvalidArgument, "name is required")
    }
    if req.Type == "" {
        return nil, status.Error(codes.InvalidArgument, "type is required")
    }

    job := &domain.Job{
        Name:       req.Name,
        Type:       domain.JobType(req.Type),
        QueueName:  req.QueueName,
        Priority:   domain.JobPriority(req.Priority),
        MaxRetries: int(req.MaxRetries),
        Payload:    protoPayloadToDomain(req.Payload),
    }
    if job.Priority == 0 {
        job.Priority = domain.PriorityNormal
    }
    if job.MaxRetries == 0 {
        job.MaxRetries = 3
    }

    // Idempotency: if key is set, check for existing job first
    if req.IdempotencyKey != "" {
        existing, err := s.store.GetJobByIdempotencyKey(ctx, req.IdempotencyKey)
        if err == nil {
            return domainJobToProto(existing), nil
        }
        if !errors.Is(err, store.ErrNotFound) {
            return nil, status.Errorf(codes.Internal, "checking idempotency key: %v", err)
        }
    }

    created, err := s.store.CreateJob(ctx, job)
    if err != nil {
        s.logger.Error("gRPC SubmitJob: store error", "err", err)
        return nil, status.Errorf(codes.Internal, "creating job: %v", err)
    }

    span.SetAttributes(attribute.String("job.id", created.ID.String()))
    return domainJobToProto(created), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetJob — unary RPC
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) GetJob(ctx context.Context, req *orionv1.GetJobRequest) (*orionv1.Job, error) {
    ctx, span := observability.Tracer("orion.grpc").Start(ctx, "grpc.GetJob")
    defer span.End()

    id, err := uuid.Parse(req.JobId)
    if err != nil {
        return nil, status.Error(codes.InvalidArgument, "job_id must be a valid UUID")
    }

    job, err := s.store.GetJob(ctx, id)
    if err != nil {
        if errors.Is(err, store.ErrNotFound) {
            return nil, status.Error(codes.NotFound, "job not found")
        }
        return nil, status.Errorf(codes.Internal, "getting job: %v", err)
    }

    span.SetAttributes(
        attribute.String("job.id", job.ID.String()),
        attribute.String("job.status", string(job.Status)),
    )
    return domainJobToProto(job), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// WatchJob — server-streaming RPC
// ─────────────────────────────────────────────────────────────────────────────

// WatchJob streams job status events to the client until the job reaches
// a terminal state or the client disconnects.
//
// Event delivery strategy (Phase 7 — polling + broadcaster):
//  1. Subscribe to broadcaster for real-time events (fast path: <1ms)
//  2. Poll DB every 500ms as fallback (catches missed broadcast events)
//  3. On first call: send current status as synthetic "initial" event
//  4. On terminal state: close the stream (return nil)
//
// Phase 8 enhancement: replace polling with pg_notify for sub-10ms latency.
func (s *Server) WatchJob(req *orionv1.WatchJobRequest, stream orionv1.JobService_WatchJobServer) error {
    ctx := stream.Context()

    id, err := uuid.Parse(req.JobId)
    if err != nil {
        return status.Error(codes.InvalidArgument, "job_id must be a valid UUID")
    }

    // Verify the job exists before opening the stream
    job, err := s.store.GetJob(ctx, id)
    if err != nil {
        if errors.Is(err, store.ErrNotFound) {
            return status.Error(codes.NotFound, "job not found")
        }
        return status.Errorf(codes.Internal, "getting job: %v", err)
    }

    s.logger.Info("gRPC WatchJob started",
        "job_id", id,
        "current_status", job.Status,
    )

    // Send current status as the first event (client needs to know current state)
    if err := stream.Send(&orionv1.JobEvent{
        JobId:          id.String(),
        JobName:        job.Name,
        PreviousStatus: string(job.Status), // same as new — synthetic first event
        NewStatus:      string(job.Status),
        Timestamp:      timestamppb.Now(),
    }); err != nil {
        return err
    }

    // If already terminal, close immediately
    if job.IsTerminal() {
        return nil
    }

    // Subscribe to broadcaster for real-time events
    ch, unsubscribe := s.broadcaster.Subscribe(id.String())
    defer unsubscribe() // CRITICAL: prevents goroutine/channel leak

    // Polling fallback ticker
    ticker := time.NewTicker(pollInterval)
    defer ticker.Stop()

    lastStatus := job.Status

    for {
        select {
        case <-ctx.Done():
            // Client disconnected or request cancelled
            s.logger.Info("gRPC WatchJob: client disconnected", "job_id", id)
            return nil

        case event, ok := <-ch:
            // Fast path: real-time event from broadcaster
            if !ok {
                return nil // broadcaster closed — should not happen
            }
            if err := stream.Send(event); err != nil {
                return err
            }
            if isTerminalStatus(event.NewStatus) {
                return nil // job done — close stream
            }
            lastStatus = domain.JobStatus(event.NewStatus)

        case <-ticker.C:
            // Polling fallback: check DB for status changes missed by broadcaster
            current, err := s.store.GetJob(ctx, id)
            if err != nil {
                s.logger.Warn("gRPC WatchJob: poll error", "job_id", id, "err", err)
                continue
            }
            if current.Status != lastStatus {
                event := &orionv1.JobEvent{
                    JobId:          id.String(),
                    JobName:        current.Name,
                    PreviousStatus: string(lastStatus),
                    NewStatus:      string(current.Status),
                    WorkerId:       current.WorkerID,
                    ErrorMessage:   current.ErrorMessage,
                    Attempt:        int32(current.Attempt),
                    Timestamp:      timestamppb.Now(),
                }
                if err := stream.Send(event); err != nil {
                    return err
                }
                lastStatus = current.Status
                if current.IsTerminal() {
                    return nil
                }
            }
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// WatchPipeline — server-streaming RPC
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) WatchPipeline(req *orionv1.WatchPipelineRequest, stream orionv1.JobService_WatchPipelineServer) error {
    ctx := stream.Context()

    id, err := uuid.Parse(req.PipelineId)
    if err != nil {
        return status.Error(codes.InvalidArgument, "pipeline_id must be a valid UUID")
    }

    pipeline, err := s.store.GetPipeline(ctx, id)
    if err != nil {
        if errors.Is(err, store.ErrNotFound) {
            return status.Error(codes.NotFound, "pipeline not found")
        }
        return status.Errorf(codes.Internal, "getting pipeline: %v", err)
    }

    // Send current pipeline status as first event
    if err := stream.Send(&orionv1.PipelineEvent{
        PipelineId:     id.String(),
        PipelineName:   pipeline.Name,
        PipelineStatus: string(pipeline.Status),
        Timestamp:      timestamppb.Now(),
    }); err != nil {
        return err
    }

    // Already terminal?
    if isPipelineTerminal(string(pipeline.Status)) {
        return nil
    }

    // Poll every 1 second for pipeline/node status changes
    // (pipelines change less frequently than individual jobs)
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()

    lastStatus := pipeline.Status

    for {
        select {
        case <-ctx.Done():
            return nil

        case <-ticker.C:
            current, err := s.store.GetPipeline(ctx, id)
            if err != nil {
                continue
            }

            if current.Status != lastStatus {
                if err := stream.Send(&orionv1.PipelineEvent{
                    PipelineId:     id.String(),
                    PipelineName:   current.Name,
                    PipelineStatus: string(current.Status),
                    Timestamp:      timestamppb.Now(),
                }); err != nil {
                    return err
                }
                lastStatus = current.Status
                if isPipelineTerminal(string(current.Status)) {
                    return nil
                }
            }
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Conversion helpers
// ─────────────────────────────────────────────────────────────────────────────

func domainJobToProto(j *domain.Job) *orionv1.Job {
    pb := &orionv1.Job{
        JobId:      j.ID.String(),
        Name:       j.Name,
        Type:       string(j.Type),
        Status:     string(j.Status),
        QueueName:  j.QueueName,
        Priority:   int32(j.Priority),
        Attempt:    int32(j.Attempt),
        MaxRetries: int32(j.MaxRetries),
        WorkerId:   j.WorkerID,
        CreatedAt:  timestamppb.New(j.CreatedAt),
        UpdatedAt:  timestamppb.New(j.UpdatedAt),
    }
    if j.ErrorMessage != "" {
        pb.ErrorMessage = j.ErrorMessage
    }
    if j.CompletedAt != nil {
        pb.CompletedAt = timestamppb.New(*j.CompletedAt)
    }
    return pb
}

func protoPayloadToDomain(p *orionv1.JobPayload) domain.JobPayload {
    if p == nil {
        return domain.JobPayload{}
    }
    payload := domain.JobPayload{
        HandlerName: p.HandlerName,
    }
    if len(p.Args) > 0 {
        payload.Args = make(map[string]any, len(p.Args))
        for k, v := range p.Args {
            payload.Args[k] = v
        }
    }
    return payload
}

func isTerminalStatus(s string) bool {
    switch domain.JobStatus(s) {
    case domain.JobStatusCompleted, domain.JobStatusDead, domain.JobStatusCancelled:
        return true
    }
    return false
}

func isPipelineTerminal(s string) bool {
    switch domain.PipelineStatus(s) {
    case domain.PipelineStatusCompleted, domain.PipelineStatusFailed, domain.PipelineStatusCancelled:
        return true
    }
    return false
}
```

---

## 10. File 4: `internal/api/grpc/server_test.go`

Testing gRPC handlers uses `bufconn` — an in-memory connection that behaves exactly like a real network connection but with zero network overhead.

### Testing pattern

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/test/bufconn"
    "net"
)

const bufSize = 1024 * 1024

func startTestServer(t *testing.T, fakeStore store.Store) orionv1.JobServiceClient {
    t.Helper()
    lis := bufconn.Listen(bufSize)

    srv := grpc.NewServer()
    broadcaster := NewBroadcaster()
    grpcServer := NewServer(fakeStore, broadcaster, testLogger())
    orionv1.RegisterJobServiceServer(srv, grpcServer)

    go func() {
        if err := srv.Serve(lis); err != nil {
            t.Logf("server stopped: %v", err)
        }
    }()
    t.Cleanup(func() { srv.Stop(); lis.Close() })

    conn, _ := grpc.DialContext(context.Background(), "bufconn",
        grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
            return lis.DialContext(ctx)
        }),
        grpc.WithInsecure(),
    )
    t.Cleanup(func() { conn.Close() })
    return orionv1.NewJobServiceClient(conn)
}
```

### Tests to write (18 total)

| Test | What it verifies |
|---|---|
| `TestSubmitJob_Valid` | 200-equivalent for valid inline job |
| `TestSubmitJob_MissingName` | INVALID_ARGUMENT for empty name |
| `TestSubmitJob_MissingType` | INVALID_ARGUMENT for empty type |
| `TestSubmitJob_StoreError` | INTERNAL for store failure |
| `TestSubmitJob_IdempotencyKey_Returns_Existing` | Returns existing job on duplicate key |
| `TestGetJob_Found` | Returns job proto correctly |
| `TestGetJob_NotFound` | NOT_FOUND code |
| `TestGetJob_InvalidUUID` | INVALID_ARGUMENT code |
| `TestWatchJob_ImmediateTerminal` | Stream sends one event and closes for completed job |
| `TestWatchJob_JobNotFound` | NOT_FOUND before stream opens |
| `TestWatchJob_InvalidUUID` | INVALID_ARGUMENT before stream opens |
| `TestWatchJob_ClientDisconnect` | Stream closes cleanly on ctx cancel |
| `TestWatchJob_BroadcasterEvent` | Event published to broadcaster received by stream |
| `TestWatchJob_PollFallback` | Status change detected via polling when no broadcast |
| `TestWatchJob_TerminalEventClosesStream` | Stream closes after completed event |
| `TestBroadcaster_Publish_NoSubscribers` | Does not panic when no subscribers |
| `TestBroadcaster_MultipleSubscribers` | All subscribers receive the event |
| `TestBroadcaster_UnsubscribeCleanup` | Map is cleaned up after unsubscribe |

---

## 11. File 5: `internal/store/postgres/db.go` — Event Emission

The broadcaster needs to be notified when a job's status changes. The cleanest place to hook this is in the store methods that transition state.

### Pattern: inject broadcaster into DB (option A) vs return events (option B)

**Option A — Inject broadcaster into DB struct:**
```go
type DB struct {
    pool        *pgxpool.Pool
    broadcaster *grpc.Broadcaster // ← inject
}

func (db *DB) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
    // ... SQL ...
    db.broadcaster.Publish(id.String(), &orionv1.JobEvent{...})
    return nil
}
```

**Option B — Store returns events, caller publishes:**
```go
// In cmd/api/main.go or a wrapper layer:
type InstrumentedStore struct {
    store.Store
    broadcaster *grpc.Broadcaster
}

func (s *InstrumentedStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
    err := s.Store.MarkJobCompleted(ctx, id)
    if err == nil {
        s.broadcaster.Publish(id.String(), &orionv1.JobEvent{NewStatus: "completed"})
    }
    return err
}
```

**Phase 7 uses Option B** — the wrapper pattern keeps the store package free of gRPC dependencies. The `InstrumentedStore` wraps `postgres.DB` and adds broadcast calls. This preserves the clean separation between storage and transport.

### InstrumentedStore

```go
// internal/api/grpc/instrumented_store.go

// InstrumentedStore wraps store.Store and publishes job events to the
// Broadcaster on every state transition. This connects the store layer
// to the gRPC streaming layer without creating a dependency from store → grpc.
type InstrumentedStore struct {
    store.Store
    broadcaster *Broadcaster
}

func NewInstrumentedStore(s store.Store, b *Broadcaster) *InstrumentedStore {
    return &InstrumentedStore{Store: s, broadcaster: b}
}

func (s *InstrumentedStore) MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error {
    err := s.Store.MarkJobRunning(ctx, id, workerID)
    if err == nil {
        s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
            JobId:     id.String(),
            NewStatus: "running",
            WorkerId:  workerID,
            Timestamp: timestamppb.Now(),
        })
    }
    return err
}

func (s *InstrumentedStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
    err := s.Store.MarkJobCompleted(ctx, id)
    if err == nil {
        s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
            JobId:     id.String(),
            NewStatus: "completed",
            Timestamp: timestamppb.Now(),
        })
    }
    return err
}

func (s *InstrumentedStore) MarkJobFailed(ctx context.Context, id uuid.UUID, msg string, retryAt *time.Time) error {
    err := s.Store.MarkJobFailed(ctx, id, msg, retryAt)
    if err == nil {
        newStatus := "failed"
        s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
            JobId:        id.String(),
            NewStatus:    newStatus,
            ErrorMessage: msg,
            Timestamp:    timestamppb.Now(),
        })
    }
    return err
}
```

---

## 12. File 6: `cmd/api/main.go` — gRPC Server Startup

```go
// After existing store + handler setup, add:

// ── gRPC server [Phase 7] ──────────────────────────────────────────────────
broadcaster := grpcserver.NewBroadcaster()
instrumentedStore := grpcserver.NewInstrumentedStore(pgStore, broadcaster)

// The gRPC server uses the InstrumentedStore (which publishes events)
// The HTTP handlers continue using pgStore directly (no events needed for HTTP)
grpcSrv := grpc.NewServer(
    grpc.UnaryInterceptor(grpcserver.UnaryLoggingInterceptor(logger)),
    grpc.StreamInterceptor(grpcserver.StreamLoggingInterceptor(logger)),
)
grpcServer := grpcserver.NewServer(instrumentedStore, broadcaster, logger)
grpcserver.RegisterGRPCServer(grpcSrv, grpcServer)

// The worker pool uses instrumentedStore so events are published on job completion
pool := worker.NewPool(
    workerCfg,
    queue,
    instrumentedStore,  // ← use instrumented store, not pgStore
    executors,
    metrics,
    logger,
)

// Start gRPC listener on :9090
grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Service.GRPCPort))
if err != nil {
    logger.Error("failed to start gRPC listener", "err", err)
    os.Exit(1)
}
go func() {
    logger.Info("gRPC server listening", "port", cfg.Service.GRPCPort)
    if err := grpcSrv.Serve(grpcLis); err != nil {
        logger.Error("gRPC server error", "err", err)
    }
}()
defer grpcSrv.GracefulStop()
```

---

## 13. File 7: `Makefile` — Proto Generation Target

```makefile
# Proto generation — requires protoc + protoc-gen-go + protoc-gen-go-grpc
# Install: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#          go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

.PHONY: proto-gen
proto-gen:
	protoc \
	  --go_out=. \
	  --go_opt=paths=source_relative \
	  --go-grpc_out=. \
	  --go-grpc_opt=paths=source_relative \
	  proto/orion/v1/jobs.proto
	@echo "Proto generated: proto/orion/v1/jobs.pb.go + jobs_grpc.pb.go"

# Verify grpcurl can connect
.PHONY: grpc-check
grpc-check:
	grpcurl -plaintext localhost:9090 list
```

---

## 14. Streaming Internals — How Events Flow

```
Worker goroutine:
  executor.Execute(ctx, job) → returns nil
  ↓
  instrumentedStore.MarkJobCompleted(ctx, id)
  ↓
  db.MarkJobCompleted(ctx, id)          ← SQL: UPDATE jobs SET status='completed'
  ↓ (on success)
  broadcaster.Publish(id, JobEvent{    ← in-memory: fans out to all subscribers
    NewStatus: "completed"
  })
  ↓
  ┌────────────────────────────────────────────────────────┐
  │ All WatchJob goroutines watching this job_id:          │
  │   ch <- event  (non-blocking, buffered channel)        │
  │                                                        │
  │ Each WatchJob goroutine:                               │
  │   event := <-ch                                        │
  │   stream.Send(event)   ← gRPC: writes to HTTP/2 frame  │
  │   if isTerminal: return nil  ← closes the stream       │
  └────────────────────────────────────────────────────────┘
  ↓
  Client (Python/Go/curl):
    for event in stub.WatchJob(req):
        print(event.new_status)  # "completed"
    # stream closed — loop exits
```

---

## 15. The Polling-to-PG-NOTIFY Evolution Path

### Phase 7 (current): 500ms polling

```
WatchJob goroutine → every 500ms → SELECT * FROM jobs WHERE id=$1 → check status
```

Maximum latency: 500ms from state transition to client notification.

### Phase 8 enhancement: PostgreSQL LISTEN/NOTIFY

```sql
-- On every state transition (via trigger or application call):
SELECT pg_notify('job_events', '{"job_id":"...","new_status":"completed"}');

-- In Go (one goroutine per API server instance):
conn.Exec("LISTEN job_events")
for notif := range conn.WaitForNotification(ctx):
    broadcaster.Publish(notif.Payload)
```

Maximum latency: <10ms (TCP round-trip to Postgres + one goroutine wake).

The broadcaster remains in Phase 8 — it still fans out to multiple `WatchJob` goroutines. Only the event source changes from polling to PG NOTIFY.

---

## 16. Error Handling and Stream Lifecycle

### gRPC status codes mapped to Orion errors

| Orion error | gRPC code | Meaning |
|---|---|---|
| `store.ErrNotFound` | `codes.NotFound` | Job/pipeline does not exist |
| Invalid UUID format | `codes.InvalidArgument` | Malformed request |
| Missing required field | `codes.InvalidArgument` | Missing name, type, etc. |
| Store failure | `codes.Internal` | DB unreachable, constraint violation |
| Context cancelled | `nil` return | Client disconnected — not an error |
| `stream.Send` fails | forward error | Network error writing to client |

### Stream lifecycle — all four paths

```
Path 1 — Normal completion:
  WatchJob opens → events stream → job reaches terminal → return nil → stream closes

Path 2 — Client disconnect:
  WatchJob opens → events stream → client cancels ctx → ctx.Done() fires → return nil

Path 3 — Write failure:
  WatchJob opens → stream.Send fails (network error) → return err → gRPC logs error

Path 4 — Job already terminal:
  WatchJob opens → GetJob shows terminal status → send one event → return nil immediately
```

---

## 17. Step-by-Step Build Order

```
Step 1: Install proto tools
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

Step 2: Write jobs.proto
  mkdir -p proto/orion/v1
  # write proto/orion/v1/jobs.proto

Step 3: Generate Go code
  make proto-gen
  # produces: proto/orion/v1/jobs.pb.go + jobs_grpc.pb.go
  go build ./proto/...
  # Must compile

Step 4: Write broadcaster.go
  go build ./internal/api/grpc/...

Step 5: Write server.go (without InstrumentedStore first — use raw store)
  go build ./internal/api/grpc/...

Step 6: Write instrumented_store.go
  go build ./internal/api/grpc/...

Step 7: Write server_test.go
  go test -race ./internal/api/grpc/... -v
  # All 18 tests pass

Step 8: Update cmd/api/main.go
  go build ./cmd/api/...
  # Must compile

Step 9: Full build
  make build
  # All 3 binaries compile

Step 10: Run verification sequence
```

---

## 18. Complete End-to-End Test Sequence

### Setup

```bash
# Install grpcurl (gRPC equivalent of curl)
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Start everything
make infra-up && make migrate-up
make run-api       # now starts on :8080 (HTTP) + :9090 (gRPC)
make run-scheduler
make run-worker
```

### Test 1 — gRPC reflection works

```bash
grpcurl -plaintext localhost:9090 list
# orion.v1.JobService

grpcurl -plaintext localhost:9090 describe orion.v1.JobService
# orion.v1.JobService is a service:
# service JobService {
#   rpc SubmitJob ( .orion.v1.SubmitJobRequest ) returns ( .orion.v1.Job );
#   rpc GetJob ( .orion.v1.GetJobRequest ) returns ( .orion.v1.Job );
#   rpc WatchJob ( .orion.v1.WatchJobRequest ) returns ( stream .orion.v1.JobEvent );
#   rpc WatchPipeline ( .orion.v1.WatchPipelineRequest ) returns ( stream .orion.v1.PipelineEvent );
# }

echo "✅ TEST 1 PASSED — gRPC server running with reflection"
```

### Test 2 — SubmitJob via gRPC

```bash
RESPONSE=$(grpcurl -plaintext \
  -d '{"name":"grpc-test","type":"inline","payload":{"handler_name":"noop"}}' \
  localhost:9090 orion.v1.JobService/SubmitJob)

echo "$RESPONSE" | jq .
# {
#   "jobId": "550e8400-...",
#   "name": "grpc-test",
#   "status": "queued",
#   "createdAt": "2024-01-15T10:00:00Z"
# }

JOB_ID=$(echo "$RESPONSE" | jq -r .jobId)
echo "Job ID: $JOB_ID"
echo "✅ TEST 2 PASSED — SubmitJob via gRPC"
```

### Test 3 — WatchJob streams live transitions

```bash
# Watch the job (in background terminal)
grpcurl -plaintext \
  -d "{\"job_id\":\"$JOB_ID\"}" \
  localhost:9090 orion.v1.JobService/WatchJob

# Expected output (appears in real-time as transitions happen):
# {
#   "jobId": "550e8400-...",
#   "previousStatus": "queued",
#   "newStatus": "queued",      ← initial event (current state)
#   "timestamp": "..."
# }
# {
#   "jobId": "550e8400-...",
#   "previousStatus": "queued",
#   "newStatus": "scheduled",
#   "timestamp": "..."
# }
# {
#   "jobId": "550e8400-...",
#   "previousStatus": "scheduled",
#   "newStatus": "running",
#   "timestamp": "..."
# }
# {
#   "jobId": "550e8400-...",
#   "previousStatus": "running",
#   "newStatus": "completed",
#   "timestamp": "..."
# }
# (stream closes — grpcurl exits)

echo "✅ TEST 3 PASSED — WatchJob streams all transitions, closes on terminal"
```

### Test 4 — Concurrent watchers all receive events

```bash
# Submit a slow job
JOB_ID=$(grpcurl -plaintext \
  -d '{"name":"slow-grpc","type":"inline","payload":{"handler_name":"slow","args":{"duration_seconds":"5"}}}' \
  localhost:9090 orion.v1.JobService/SubmitJob | jq -r .jobId)

# Open 3 concurrent watchers in background
for i in 1 2 3; do
  grpcurl -plaintext -d "{\"job_id\":\"$JOB_ID\"}" \
    localhost:9090 orion.v1.JobService/WatchJob \
    > /tmp/watcher_$i.log 2>&1 &
done

sleep 10  # wait for job to complete

# All 3 watchers should have received the completed event
for i in 1 2 3; do
  grep -c "completed" /tmp/watcher_$i.log && echo "Watcher $i: ✅" || echo "Watcher $i: ❌"
done

echo "✅ TEST 4 PASSED — concurrent watchers all receive events"
```

### Test 5 — Pipeline watching

```bash
PIPELINE_ID=$(curl -s -X POST localhost:8080/pipelines \
  -H "Content-Type: application/json" \
  -d '{"name":"grpc-pipeline","dag_spec":{"nodes":[
    {"id":"a","job_template":{"handler_name":"noop"}},
    {"id":"b","job_template":{"handler_name":"noop"}}
  ],"edges":[{"source":"a","target":"b"}]}}' | jq -r .id)

# Watch pipeline via gRPC
grpcurl -plaintext \
  -d "{\"pipeline_id\":\"$PIPELINE_ID\"}" \
  localhost:9090 orion.v1.JobService/WatchPipeline

# Expected: pending → running → completed events
echo "✅ TEST 5 PASSED — WatchPipeline streams pipeline transitions"
```

---

## 19. gRPC vs HTTP — When to Use Which

| Use case | Use HTTP | Use gRPC |
|---|---|---|
| Web browser clients | ✅ | ❌ (no native gRPC in browsers without grpc-web) |
| curl / manual testing | ✅ | ⚠️ (grpcurl needed) |
| Python/Go SDK clients | Either | ✅ (streaming is native) |
| Long-running job watching | ❌ (polling needed) | ✅ (streaming built-in) |
| Simple one-shot submit | ✅ | Either |
| Firewall traversal | ✅ | ✅ (HTTP/2 often allowed) |
| Real-time dashboard feed | ❌ | ✅ |

Both APIs coexist and use the same store. Neither is deprecated by the other.

---

## 20. Common Mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| Forgetting `defer unsubscribe()` | Goroutine leak — channels accumulate in broadcaster | Always `defer unsub()` immediately after `Subscribe()` |
| Blocking `Publish()` | Worker pool stalls waiting for slow gRPC client | Broadcaster uses `select { case ch <- event: default: }` — never blocks |
| Not checking `stream.Context().Done()` | Goroutine keeps running after client disconnect | Always select on `ctx.Done()` in the watch loop |
| Running gRPC on same port as metrics | Address already in use | HTTP :8080, gRPC :9090, metrics :9091 |
| Using `grpc.Dial` without `WithInsecure` in tests | TLS handshake failure | Use `grpc.WithInsecure()` or proper TLS in tests |
| Not embedding `UnimplementedJobServiceServer` | Breaks when proto adds new RPCs | Always embed — required for forward compatibility |
| Writing to a cancelled stream | `context canceled` error flood in logs | Check `stream.Context().Done()` before `stream.Send()` |
| Proto field numbers reused | Silent data corruption if old clients connect | Never reuse a field number — add new fields at the end |

---

## 21. Phase 8 Preview

After Phase 7, clients can watch jobs in real-time. What's still limited:

- Polling every 500ms adds latency and unnecessary DB load
- No per-queue concurrency limits — batch jobs can starve interactive jobs
- No fair scheduling — first-in-first-out regardless of job priority within a queue

Phase 8 adds:

**PG NOTIFY for zero-latency events:**
```sql
-- Trigger fires on every jobs row update:
CREATE OR REPLACE FUNCTION notify_job_event() RETURNS TRIGGER AS $$
BEGIN
  PERFORM pg_notify('job_events',
    json_build_object('job_id', NEW.id, 'status', NEW.status)::text);
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
```

**Per-queue rate limiting:**
```yaml
queue_limits:
  high:    8   # max 8 worker slots
  default: 6   # max 6 worker slots
  low:     2   # max 2 worker slots
```

**Fair scheduling within priority:**
Tokens bucket per queue — high-priority jobs can burst but cannot indefinitely starve low-priority ones.

---

## Summary Checklist

```
□ proto/orion/v1/jobs.proto
    □ service JobService with 4 RPCs (SubmitJob, GetJob, WatchJob, WatchPipeline)
    □ JobEvent + PipelineEvent messages with timestamps
    □ Job, JobPayload, KubernetesSpec, ResourceRequest messages
    □ go_package option set correctly
    □ Compiled: make proto-gen succeeds

□ internal/api/grpc/broadcaster.go
    □ Broadcaster struct with sync.RWMutex
    □ Publish() — non-blocking, drop on full buffer
    □ Subscribe() — returns buffered chan + unsubscribe func
    □ SubscriberCount() — for tests and health checks
    □ Memory safety: unsubscribe deletes map entry + closes channel

□ internal/api/grpc/server.go
    □ Server struct embeds UnimplementedJobServiceServer
    □ SubmitJob — idempotency key check + CreateJob
    □ GetJob — 404 on ErrNotFound
    □ WatchJob — initial event + broadcaster sub + 500ms poll fallback
    □ WatchJob — closes on terminal status or client disconnect
    □ WatchPipeline — 1s poll, closes on terminal pipeline status
    □ domainJobToProto, protoPayloadToDomain conversion helpers

□ internal/api/grpc/instrumented_store.go
    □ InstrumentedStore embeds store.Store
    □ MarkJobRunning — publish "running" event
    □ MarkJobCompleted — publish "completed" event
    □ MarkJobFailed — publish "failed" event

□ internal/api/grpc/server_test.go
    □ 18 tests using bufconn (in-memory gRPC connection)
    □ All 4 RPCs covered
    □ Error paths: NotFound, InvalidArgument, Internal
    □ Streaming paths: terminal, disconnect, broadcast, poll fallback

□ cmd/api/main.go (update)
    □ Broadcaster + InstrumentedStore created
    □ grpc.NewServer() with logging interceptors
    □ gRPC listener on :9090
    □ GracefulStop() on shutdown
    □ Worker pool uses InstrumentedStore (not pgStore)

□ Makefile (update)
    □ proto-gen target using protoc
    □ grpc-check target using grpcurl

□ Verification
    □ make proto-gen succeeds
    □ make build — all 3 binaries compile
    □ go test -race ./internal/api/grpc/... — 18 tests pass
    □ Test 1: grpcurl list shows orion.v1.JobService
    □ Test 2: SubmitJob returns job with ID
    □ Test 3: WatchJob streams queued→scheduled→running→completed and closes
    □ Test 4: 3 concurrent watchers all receive completed event
    □ Test 5: WatchPipeline streams pending→running→completed
```

---

## File Locations After Phase 7

```
orion/
├── proto/
│   └── orion/v1/
│       ├── jobs.proto              ← NEW: service definition
│       ├── jobs.pb.go              ← GENERATED
│       └── jobs_grpc.pb.go         ← GENERATED
├── internal/api/
│   ├── grpc/
│   │   ├── broadcaster.go          ← NEW: pub/sub hub
│   │   ├── instrumented_store.go   ← NEW: event-emitting store wrapper
│   │   ├── server.go               ← NEW: 4 RPC implementations
│   │   └── server_test.go          ← NEW: 18 tests (bufconn)
│   └── handler/                    ← unchanged (HTTP handlers)
└── cmd/api/
    └── main.go                     ← UPDATED: gRPC server on :9090
```