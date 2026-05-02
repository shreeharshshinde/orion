# gRPC Server — Phase 7

## What This Document Covers

The gRPC server added in Phase 7: the proto contract, the four RPC implementations, how server-streaming works in Go, and how to test it manually with `grpcurl`.

---

## Service Definition

The proto file at `proto/orion/v1/jobs.proto` defines the contract:

```protobuf
service JobService {
  rpc SubmitJob(SubmitJobRequest)         returns (Job);
  rpc GetJob(GetJobRequest)               returns (Job);
  rpc WatchJob(WatchJobRequest)           returns (stream JobEvent);
  rpc WatchPipeline(WatchPipelineRequest) returns (stream PipelineEvent);
}
```

Two unary RPCs mirror the HTTP API. Two server-streaming RPCs are new — they have no HTTP equivalent.

**Why duplicate SubmitJob and GetJob from HTTP?**
SDK clients should not need both an HTTP and a gRPC client. Providing the full surface in gRPC means SDK authors only need one client. The HTTP API remains for browsers and curl.

---

## How Server-Streaming Works in Go

HTTP is request-response: one request, one response, connection closes.

gRPC server-streaming: client sends one message, server sends **many** messages over time, connection stays open until the server returns.

```
Client                          Server
  |-- WatchJob(job_id=abc) --→  |
  |←-- event: queued→scheduled  |
  |←-- event: scheduled→running |
  |                             | (server waits — stream stays open)
  |←-- event: running→completed |
  |←-- EOF (stream closed)     |
```

The generated interface (grpc v1.64+) uses generics:

```go
// Server implements this:
WatchJob(*WatchJobRequest, grpc.ServerStreamingServer[JobEvent]) error

// stream.Send() pushes one event to the client immediately
// stream.Context().Done() fires when the client disconnects
```

---

## WatchJob Implementation

`internal/api/grpc/server.go` — the core streaming logic:

```
1. Parse and validate job_id UUID
2. GetJob from store — return NotFound if absent
3. Send current status as synthetic first event (client needs starting state)
4. If already terminal → return nil (stream closes immediately)
5. Subscribe to broadcaster (fast path: <1ms event delivery)
6. Start 500ms polling ticker (fallback for missed broadcast events)
7. Loop:
   - ctx.Done()  → client disconnected → return nil
   - ch event    → send to client → if terminal → return nil
   - ticker      → poll DB → if status changed → send event → if terminal → return nil
```

The polling fallback ensures correctness even if a broadcast event is dropped (buffer full) or the broadcaster is not yet wired to a particular state transition.

---

## Stream Lifecycle — Four Paths

| Path | Trigger | Server action |
|---|---|---|
| Normal completion | Job reaches terminal state | Send final event, `return nil` |
| Client disconnect | Client cancels / network drop | `ctx.Done()` fires, `return nil` |
| Write failure | Network error on `stream.Send` | Forward the error |
| Already terminal | Job was completed before WatchJob called | Send one event, `return nil` immediately |

---

## gRPC Status Codes

| Condition | Code |
|---|---|
| Job/pipeline not found | `codes.NotFound` |
| Invalid UUID | `codes.InvalidArgument` |
| Missing required field | `codes.InvalidArgument` |
| Store failure | `codes.Internal` |
| Client disconnect | `nil` (not an error) |

---

## Running the Server

The gRPC server starts automatically with the API binary:

```bash
make run-api
# gRPC server listening port=9090
# API server listening port=8080
```

Both listeners run in the same process, sharing the same store and broadcaster.

---

## Manual Testing with grpcurl

```bash
# Install grpcurl
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# List services (requires server reflection — enabled by default)
grpcurl -plaintext localhost:9090 list
# orion.v1.JobService

# Describe the service
grpcurl -plaintext localhost:9090 describe orion.v1.JobService

# Submit a job
grpcurl -plaintext \
  -d '{"name":"grpc-test","type":"inline","payload":{"handler_name":"noop"}}' \
  localhost:9090 orion.v1.JobService/SubmitJob

# Watch a job (streams until terminal)
grpcurl -plaintext \
  -d '{"job_id":"<uuid>"}' \
  localhost:9090 orion.v1.JobService/WatchJob
```

Expected WatchJob output (events arrive in real-time):
```json
{ "jobId": "...", "previousStatus": "queued",     "newStatus": "queued"     }
{ "jobId": "...", "previousStatus": "queued",     "newStatus": "scheduled"  }
{ "jobId": "...", "previousStatus": "scheduled",  "newStatus": "running"    }
{ "jobId": "...", "previousStatus": "running",    "newStatus": "completed"  }
```
Stream closes after the `completed` event.

---

## Port Allocation

| Service | Port | Protocol |
|---|---|---|
| HTTP API | 8080 | HTTP/1.1 |
| gRPC API | 9090 | HTTP/2 (gRPC) |
| Prometheus metrics | 9091 | HTTP/1.1 |

---

## Phase 8 Enhancement

Phase 7 polls the DB every 500ms as the fallback mechanism. Phase 8 replaces this with PostgreSQL `LISTEN/NOTIFY`:

```sql
-- Trigger fires on every jobs row update:
SELECT pg_notify('job_events', json_build_object('job_id', NEW.id, 'status', NEW.status)::text);
```

This reduces maximum event latency from 500ms to <10ms. The broadcaster remains — it still fans out to multiple `WatchJob` goroutines. Only the event source changes.
