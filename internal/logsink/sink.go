// Package logsink provides a pluggable interface for shipping execution logs
// produced by Orion workers to durable storage.
//
// # Architecture
//
// The LogSink interface decouples the worker pool from any specific storage
// backend. The pool holds a single LogSink and passes an io.Writer to each
// executor via Execute(ctx, job, out). After execution the pool calls
// Flush(ctx, jobID, execID, data) to persist whatever was written to that
// writer, and receives a URI string it stores in executions.logs_ref.
//
// # Implementations
//
//   - NilSink  — no-op; discards all log data. Zero config. Used by default.
//   - FileSink — writes one file per execution to a local directory.
//     URI format: file:///path/to/dir/{jobID}/{execID}.log
//   - S3Sink   — uploads one object per execution to an S3-compatible bucket.
//     URI format: s3://{bucket}/{prefix}/{jobID}/{execID}.log
//     Compatible with AWS S3, MinIO, and Cloudflare R2.
//
// # Configuration
//
// The sink is selected via the ORION_LOG_SINK environment variable:
//
//	ORION_LOG_SINK=nil    (default)
//	ORION_LOG_SINK=file   (requires ORION_LOG_SINK_DIR)
//	ORION_LOG_SINK=s3     (requires ORION_S3_BUCKET, ORION_S3_REGION, etc.)
package logsink

import (
	"context"
	"io"

	"github.com/google/uuid"
)

// LogSink is the interface every log-shipping backend must implement.
//
// Implementations are called by the worker pool after job execution:
//
//  1. Pool creates a *bytes.Buffer and wraps it as an io.Writer.
//  2. Pool passes the writer to Executor.Execute so the executor can write logs.
//  3. After Execute returns, Pool calls Flush with the accumulated bytes.
//  4. Flush returns a URI that is stored in executions.logs_ref.
//
// Flush must be safe to call with zero-length data (no log output is a valid
// case — Flush should return an empty string URI rather than an error).
type LogSink interface {
	// Flush persists log data for a completed execution and returns a URI
	// that uniquely identifies where the logs are stored.
	//
	// Parameters:
	//   ctx    — may be cancelled; implementations should respect it.
	//   jobID  — the Orion job UUID (used to construct the storage path).
	//   execID — the execution-attempt UUID (used to deduplicate within a job).
	//   data   — the raw log bytes to persist; may be empty.
	//
	// Returns:
	//   uri, nil — a non-empty URI if data was persisted; "" if data was empty.
	//   "", err  — if the upload/write failed.
	Flush(ctx context.Context, jobID, execID uuid.UUID, data []byte) (uri string, err error)

	// Writer returns an io.Writer the executor should write logs to during
	// execution. The returned writer is per-execution (not shared).
	// NilSink returns io.Discard; other sinks return a *bytes.Buffer.
	//
	// Callers must pass the same writer's data to Flush after execution.
	Writer() io.Writer
}
