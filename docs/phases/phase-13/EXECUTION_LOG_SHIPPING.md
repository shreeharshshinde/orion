# Phase 13 — Execution Log Shipping

> **Status:** Planned  
> **Priority:** High — critical for debugging ML failures in production  
> **Estimated effort:** 1–2 days  
> **Depends on:** Nothing (can be implemented independently)

---

## Table of Contents

1. [Problem statement](#1-problem-statement)
2. [What already exists](#2-what-already-exists)
3. [Design decisions](#3-design-decisions)
4. [Architecture](#4-architecture)
5. [Files to create or modify](#5-files-to-create-or-modify)
6. [Implementation plan](#6-implementation-plan)
7. [Configuration](#7-configuration)
8. [LogSink interface](#8-logsink-interface)
9. [Inline executor log capture](#9-inline-executor-log-capture)
10. [Kubernetes executor log capture](#10-kubernetes-executor-log-capture)
11. [Database wiring](#11-database-wiring)
12. [Tests](#12-tests)
13. [How to verify](#13-how-to-verify)
14. [ADR reference](#14-adr-reference)

---

## 1. Problem statement

When an ML training job fails, the first question is always: what did the job
print before it crashed? Right now Orion has no answer:

- **Inline jobs**: handler output goes to the worker's stdout, mixed in with
  Orion's own structured log stream. Finding a specific job's output in a log
  aggregator requires filtering by `job_id` and hoping the handler used
  `fmt.Println` or the logger passed to it.

- **Kubernetes jobs**: pod logs exist in the cluster for a configurable
  retention window (typically 1–7 days), then are deleted. If the worker
  retrieves them at all, it discards them. After pod cleanup, they are gone.

`domain.JobExecution` already has a `LogsRef string` field (defined as "S3/GCS
URI" in the comment). The field is populated with an empty string in every
`RecordExecution` call. This phase wires it up.

---

## 2. What already exists

In `internal/domain/job.go`:

```go
// JobExecution is an immutable audit record of a single execution attempt.
type JobExecution struct {
    ID         uuid.UUID
    JobID      uuid.UUID
    Attempt    int
    WorkerID   string
    Status     JobStatus
    StartedAt  *time.Time
    FinishedAt *time.Time
    ExitCode   *int
    LogsRef    string     // S3/GCS URI  ← already defined, never populated
    Error      string
    CreatedAt  time.Time
}
```

In `internal/store/postgres/db.go`, `RecordExecution` already inserts
`logs_ref` into the `job_executions` table — it just receives an empty string
from every caller.

In `internal/worker/pool.go`, `executeJob` calls `RecordExecution` twice: once
at start (`status=running`) and once at end (`status=completed/failed`). The
second call is where `LogsRef` should be set to the uploaded log URI.

---

## 3. Design decisions

### Streaming upload, not buffered

ML training jobs can produce gigabytes of log output (loss curves logged every
step, checkpoint summaries, CUDA memory traces). Buffering the entire log into
worker memory before uploading would replicate the body-size memory exhaustion
problem in reverse.

The `LogSink.Upload` method accepts an `io.Reader` and streams directly to
object storage. The caller pipes the log source to the sink without
materialising the full content in RAM.

### Where the `LogSink` interface lives

`internal/worker/logs/` — a new package inside the worker layer. The store
package is not aware of log shipping. The worker is responsible for capturing
logs and calling the store with the resulting URI. This keeps the dependency
direction correct:

```
cmd/worker → internal/worker/logs (LogSink)
                                   └── uploads to S3/MinIO
           → internal/worker/pool  (executeJob)
                                   └── calls RecordExecution(logs_ref=uri)
```

### Provider abstraction

The `LogSink` interface makes it possible to swap backends without changing
worker code:

```
LogSink interface
  ├── S3Sink      (AWS S3 or any S3-compatible endpoint)
  ├── MinIOSink   (MinIO — same S3 API, different init)
  ├── GCSSink     (Google Cloud Storage — future)
  └── NilSink     (discards all logs — dev/test default)
```

Since MinIO uses the S3-compatible API, `S3Sink` with `Endpoint` set covers
both AWS and MinIO. A separate `MinIOSink` type is not necessary.

### Log URI format

```
s3://orion-logs/{year}/{month}/{day}/{job_id}/{execution_id}.log
```

This makes logs browsable in the S3 console without tooling, partitions by
date for lifecycle policies, and ensures uniqueness by execution ID.

### No log streaming to the API

`GET /jobs/{id}/executions` will return the `logs_ref` URI. The client
(dashboard or SDK) fetches the log directly from object storage using a
presigned URL. Orion does not proxy log content — this keeps the API server
stateless and avoids making it a bottleneck for large log downloads.

A presigned URL endpoint (`GET /jobs/{id}/executions/{execId}/logs`) can be
added as a follow-up without changing the storage architecture.

---

## 4. Architecture

```
Inline executor:
  handler func runs → io.Pipe() captures stdout/stderr
  worker.executeJob → LogSink.Upload(ctx, jobID, execID, pipeReader)
                    → returns s3://orion-logs/.../exec-id.log
  RecordExecution(logs_ref = "s3://...")

Kubernetes executor:
  k8s.Job completes → clientset.CoreV1().Pods(ns).GetLogs(pod, opts)
                    → returns io.ReadCloser (streaming pod log)
  worker.executeJob → LogSink.Upload(ctx, jobID, execID, podLogReader)
                    → returns s3://orion-logs/.../exec-id.log
  RecordExecution(logs_ref = "s3://...")

NilSink (default, no config):
  Upload() → returns ""
  RecordExecution(logs_ref = "")  ← same behaviour as today
```

---

## 5. Files to create or modify

| File | Action | What changes |
|---|---|---|
| `internal/worker/logs/sink.go` | **Create** | `LogSink` interface + `NilSink` |
| `internal/worker/logs/s3_sink.go` | **Create** | `S3Sink` implementation using `aws-sdk-go-v2/service/s3` |
| `internal/worker/logs/sink_test.go` | **Create** | Unit tests for `S3Sink` using the S3 SDK's mock transport |
| `internal/worker/pool.go` | **Modify** | Accept `LogSink` in `Pool`; call `Upload` and pass URI to `RecordExecution` |
| `internal/worker/inline.go` | **Modify** | Capture handler output via `io.Pipe` and return it as an `io.Reader` |
| `internal/worker/k8s/executor.go` | **Modify** | After job completion, stream pod logs and return as `io.ReadCloser` |
| `internal/config/config.go` | **Modify** | Add `LogStorage` config section |
| `cmd/worker/main.go` | **Modify** | Construct `LogSink` from config; pass to `NewPool` |
| `go.mod` | **Modify** | Add `github.com/aws/aws-sdk-go-v2` dependencies |
| `docs/adr/ADR-011-execution-log-storage.md` | **Create** | Document the design decision |

---

## 6. Implementation plan

### 6.1 `internal/worker/logs/sink.go`

```go
package logs

import (
    "context"
    "io"

    "github.com/google/uuid"
)

// LogSink uploads execution logs to object storage and returns a URI.
// Implementations must be safe for concurrent use.
type LogSink interface {
    // Upload reads from r and stores the content at a deterministic path
    // derived from jobID and executionID. Returns the URI of the stored log
    // (e.g., "s3://orion-logs/2026/11/04/job-id/exec-id.log") or an empty
    // string if nothing was uploaded.
    Upload(ctx context.Context, jobID, executionID uuid.UUID, r io.Reader) (uri string, err error)
}

// NilSink discards all log content and always returns an empty URI.
// Used when log shipping is not configured.
type NilSink struct{}

func (NilSink) Upload(_ context.Context, _, _ uuid.UUID, r io.Reader) (string, error) {
    // Drain the reader to prevent the pipe from blocking the handler.
    _, _ = io.Copy(io.Discard, r)
    return "", nil
}
```

### 6.2 `internal/worker/logs/s3_sink.go`

```go
package logs

import (
    "context"
    "fmt"
    "io"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    "github.com/google/uuid"
)

// S3Sink uploads execution logs to an S3-compatible bucket.
// Works with AWS S3, MinIO, Cloudflare R2, and any S3-compatible endpoint.
type S3Sink struct {
    client *s3.Client
    bucket string
}

// NewS3Sink creates an S3Sink. cfg is an aws.Config produced by
// config.LoadDefaultConfig or manually constructed with a custom Endpoint
// resolver for MinIO.
func NewS3Sink(cfg aws.Config, bucket string) *S3Sink {
    return &S3Sink{
        client: s3.NewFromConfig(cfg),
        bucket: bucket,
    }
}

// Upload streams r to S3 at:
//   s3://<bucket>/<year>/<month>/<day>/<jobID>/<executionID>.log
func (s *S3Sink) Upload(ctx context.Context, jobID, executionID uuid.UUID, r io.Reader) (string, error) {
    now := time.Now().UTC()
    key := fmt.Sprintf("%04d/%02d/%02d/%s/%s.log",
        now.Year(), now.Month(), now.Day(),
        jobID.String(), executionID.String(),
    )

    _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket:      aws.String(s.bucket),
        Key:         aws.String(key),
        Body:        r,
        ContentType: aws.String("text/plain; charset=utf-8"),
    })
    if err != nil {
        return "", fmt.Errorf("s3 upload: %w", err)
    }

    return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}
```

### 6.3 `internal/worker/pool.go` — accept `LogSink`, call `Upload`

Add `logSink logs.LogSink` to the `Pool` struct. Default to `logs.NilSink{}`.

In `executeJob`, after `executor.Execute` returns:

```go
// Capture logs regardless of success or failure.
// logReader is non-nil only if the executor produced a readable log stream.
var logsRef string
if logReader != nil {
    execID := uuid.New()
    if uri, err := p.logSink.Upload(ctx, job.ID, execID, logReader); err != nil {
        logger.Warn("log upload failed", "err", err)
    } else {
        logsRef = uri
    }
}

// RecordExecution (success or failure path) now passes logsRef
_ = p.store.RecordExecution(ctx, &domain.JobExecution{
    // ... existing fields ...
    LogsRef: logsRef,
})
```

The `Executor` interface gains an optional `Logs() io.ReadCloser` pattern:

```go
// ExecutorResult is returned by Execute. LogReader is nil if the executor
// does not produce a separate log stream (e.g., inline where logs go to
// worker stdout).
type ExecutorResult struct {
    LogReader io.ReadCloser // nil = no separate log stream
}
```

Alternatively (simpler, no interface change): both executors accept a
`logWriter io.Writer` passed by the pool, write their logs there, and the pool
pipes the other end to `LogSink.Upload` via `io.Pipe`. This is the preferred
approach because it avoids changing the `Executor` interface.

```go
// In executeJob:
pr, pw := io.Pipe()

// Upload runs concurrently with execution so logs stream in real time.
var logsRef string
var uploadWg sync.WaitGroup
uploadWg.Add(1)
go func() {
    defer uploadWg.Done()
    execID := uuid.New()
    uri, err := p.logSink.Upload(ctx, job.ID, execID, pr)
    if err != nil {
        logger.Warn("log upload failed", "err", err)
    }
    logsRef = uri
}()

err = executor.Execute(execCtx, job, pw) // pw is the new logWriter parameter
pw.CloseWithError(err)                   // signal EOF or error to the reader
uploadWg.Wait()                          // wait for upload to finish
```

### 6.4 `internal/worker/inline.go` — write to `logWriter`

The inline `Execute` signature becomes:

```go
func (e *InlineExecutor) Execute(ctx context.Context, job *domain.Job, logWriter io.Writer) error
```

Handler functions registered via `Register(name, fn)` receive a logger derived
from `logWriter` rather than writing to the worker's global logger. Existing
handlers that do not use the provided logger continue to work unchanged — their
output goes to `io.Discard` via the logger sink.

### 6.5 `internal/worker/k8s/executor.go` — stream pod logs

After the K8s Job reaches `Succeeded` or `Failed`, stream the pod logs:

```go
// Get the pod name for the completed job
podList, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
    LabelSelector: fmt.Sprintf("job-name=%s", jobName),
})
if err != nil || len(podList.Items) == 0 {
    // No logs available; write empty to logWriter
    return execErr
}
pod := podList.Items[0]

logStream, err := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
    Container: pod.Spec.Containers[0].Name,
    Follow:    false, // job already completed
}).Stream(ctx)
if err == nil {
    defer logStream.Close()
    _, _ = io.Copy(logWriter, logStream)
}
```

---

## 7. Configuration

### Config struct addition

```go
type LogStorage struct {
    Provider  string // "s3", "none" (default: "none")
    Bucket    string // bucket name
    Region    string // AWS region or MinIO equivalent
    Endpoint  string // custom endpoint for MinIO / R2 (empty = AWS default)
    AccessKey string // access key ID
    SecretKey string // secret access key (read from env, never logged)
}
```

### Environment variables

```bash
# Log storage provider: "s3" or "none" (default)
ORION_LOG_STORAGE_PROVIDER=s3

# S3/MinIO bucket name
ORION_LOG_STORAGE_BUCKET=orion-execution-logs

# AWS region
ORION_LOG_STORAGE_REGION=us-east-1

# Custom endpoint for MinIO or S3-compatible services
# Leave empty for AWS S3
ORION_LOG_STORAGE_ENDPOINT=http://minio:9000

# Credentials (use IAM roles in production instead)
ORION_LOG_STORAGE_ACCESS_KEY=minioadmin
ORION_LOG_STORAGE_SECRET_KEY=minioadmin
```

### `.env.example` addition

```bash
# Execution log storage
# Provider: "s3" (AWS S3 or S3-compatible), "none" (discard logs, default)
ORION_LOG_STORAGE_PROVIDER=none
ORION_LOG_STORAGE_BUCKET=orion-execution-logs
ORION_LOG_STORAGE_REGION=us-east-1
ORION_LOG_STORAGE_ENDPOINT=         # empty = AWS S3; set for MinIO
ORION_LOG_STORAGE_ACCESS_KEY=
ORION_LOG_STORAGE_SECRET_KEY=
```

### Helm values addition

```yaml
logStorage:
  provider: "none"      # "s3" or "none"
  bucket: ""
  region: "us-east-1"
  endpoint: ""          # empty = AWS S3, set to MinIO endpoint for local
  existingSecret: ""    # if set, read access/secret key from this K8s secret
```

---

## 8. LogSink interface

The full interface contract:

```go
// LogSink is the single integration point between the worker and object storage.
// It is designed for concurrent use — multiple goroutines may call Upload
// simultaneously, one per executing job.
//
// Implementations:
//   NilSink   — discards content, returns "". Used when provider=none.
//   S3Sink    — streams to S3-compatible storage.
type LogSink interface {
    Upload(ctx context.Context, jobID, executionID uuid.UUID, r io.Reader) (uri string, err error)
}
```

**Why `io.Reader` not `[]byte`**

An ML training run printing every-step loss can produce hundreds of megabytes.
`[]byte` would require materialising the full log in worker memory. `io.Reader`
lets the S3 SDK stream in chunks. The `PutObject` API with a reader body sends
a chunked `Transfer-Encoding` request and never buffers more than a single
upload chunk.

**Why the sink is in the worker package, not the store**

The store's responsibility is persisting structured metadata. Log content is
unstructured binary data of unbounded size that belongs in object storage, not
a relational column. The worker is the correct layer to coordinate log capture
and upload, then pass the resulting URI to the store as a plain string.

---

## 9. Inline executor log capture

Current inline handler signature:

```go
type HandlerFunc func(ctx context.Context, job *domain.Job) error
```

With log capture:

```go
type HandlerFunc func(ctx context.Context, job *domain.Job, logWriter io.Writer) error
```

Handlers write their output to `logWriter`:

```go
// Example handler after this change
func myTrainingHandler(ctx context.Context, job *domain.Job, w io.Writer) error {
    fmt.Fprintln(w, "Starting epoch 1...")
    // ... training loop ...
    fmt.Fprintln(w, "Training complete. Loss: 0.042")
    return nil
}
```

Existing handlers that ignore `logWriter` continue to compile. Their output is
not captured (goes to worker stdout, same as today) — no regression.

The `InlineExecutor.Execute` method passes `logWriter` to the handler and the
handler's output is streamed to the `LogSink` concurrently.

---

## 10. Kubernetes executor log capture

The K8s executor already watches for job completion via a watcher loop. After
the job reaches `Complete` or `Failed`:

1. List pods with label `job-name=<k8s-job-name>`
2. Call `GetLogs` on the first pod's primary container
3. `io.Copy(logWriter, logStream)`

The log is written to `logWriter` (the pipe connected to `LogSink.Upload`),
so the upload and the pod log stream run concurrently. The `logWriter.Close()`
call signals EOF to the S3 uploader, completing the multipart upload.

**Pod log retention note:** Orion downloads pod logs at job completion, before
any TTL cleanup. The `TTLSecondsAfterFinished` in the K8s Job spec
(`job.Payload.KubernetesSpec.TTLSeconds`) controls how long the pod exists
after the job completes. The log shipping must complete before the pod is
garbage collected. Since we capture logs synchronously as part of
`executeJob`, this is guaranteed.

---

## 11. Database wiring

No schema changes. `job_executions.logs_ref` already exists as a `TEXT` column
(nullable). The `RecordExecution` store method already includes `logs_ref` in
its `INSERT` statement.

The only change: `executeJob` in `pool.go` passes `logsRef` (the URI returned
by `LogSink.Upload`) instead of an empty string.

---

## 12. Tests

### `internal/worker/logs/sink_test.go`

```go
// TestNilSink_Upload verifies NilSink drains the reader and returns "".
// TestS3Sink_Upload verifies:
//   - PutObject is called with the correct bucket and key format
//   - The returned URI matches s3://bucket/year/month/day/jobID/execID.log
//   - Upload errors are propagated
//
// Use httpmock or the SDK's mock transport to intercept S3 calls.
// No real AWS credentials needed.
```

### `internal/worker/pool_test.go` additions

```go
// TestExecuteJob_LogShipping verifies:
//   - After a successful inline job, RecordExecution is called with a
//     non-empty LogsRef when a non-NilSink is configured.
//   - After a failed job, LogsRef is still populated (failure logs matter).
//   - When LogSink.Upload returns an error, executeJob logs a warning but
//     does not fail the job — log upload errors are non-fatal.
```

---

## 13. How to verify

### With MinIO (local S3-compatible)

Add MinIO to `docker-compose.yml` (one service, no persistent volume needed
for testing):

```yaml
minio:
  image: minio/minio:latest
  command: server /data --console-address ":9001"
  environment:
    MINIO_ROOT_USER: minioadmin
    MINIO_ROOT_PASSWORD: minioadmin
  ports:
    - "9000:9000"
    - "9001:9001"
```

Create the bucket:

```bash
mc alias set local http://localhost:9000 minioadmin minioadmin
mc mb local/orion-execution-logs
```

Run a job:

```bash
ORION_LOG_STORAGE_PROVIDER=s3 \
ORION_LOG_STORAGE_BUCKET=orion-execution-logs \
ORION_LOG_STORAGE_ENDPOINT=http://localhost:9000 \
ORION_LOG_STORAGE_ACCESS_KEY=minioadmin \
ORION_LOG_STORAGE_SECRET_KEY=minioadmin \
make run-worker

JOB_ID=$(curl -s -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"log-test","type":"inline","payload":{"handler_name":"echo","args":{"message":"hello logs"}}}' \
  | jq -r .id)

sleep 3

# Check that logs_ref is populated
curl -s "http://localhost:8080/jobs/$JOB_ID/executions" | jq '.[0].logs_ref'
# Expected: "s3://orion-execution-logs/2026/11/04/<job-id>/<exec-id>.log"

# Download the log from MinIO
LOGS_REF=$(curl -s "http://localhost:8080/jobs/$JOB_ID/executions" | jq -r '.[0].logs_ref')
KEY="${LOGS_REF#s3://orion-execution-logs/}"
mc cat "local/orion-execution-logs/$KEY"
# Expected: "hello logs" (or whatever the echo handler produces)
```

---

## 14. ADR reference

Create `docs/adr/ADR-011-execution-log-storage.md` documenting:

- Decision: stream to object storage via `LogSink` interface, not to PostgreSQL BYTEA
- Alternatives considered: PostgreSQL large objects, stdout-only (current), log aggregator sidecar
- Consequences: `logs_ref` URI in `job_executions` is the only pointer; log content is not retrievable if the bucket is deleted; presigned URL endpoint is a follow-up
- The `io.Reader` streaming contract and why buffering was rejected
