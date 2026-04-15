// Package handlers provides built-in HandlerFunc implementations for Orion.
//
// These handlers serve two purposes:
//  1. Smoke-test utilities — verify the entire pipeline is working without
//     writing any business logic. Submit a "noop" job and if it reaches
//     status=completed, your infrastructure is correctly wired end-to-end.
//  2. Code examples — each handler demonstrates a specific pattern that
//     real ML handlers should follow. Read Slow before writing any handler
//     that does long-running work.
//
// Register these in cmd/worker/main.go:
//
//	registry.Register("noop",        handlers.Noop)
//	registry.Register("echo",        handlers.Echo)
//	registry.Register("always_fail", handlers.AlwaysFail)
//	registry.Register("slow",        handlers.Slow)
package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shreeharshshinde/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// Noop — the smoke-test handler
// ─────────────────────────────────────────────────────────────────────────────

// Noop does nothing and always returns nil.
//
// Primary use: submit a noop job to verify the entire pipeline is wired
// correctly. If the job reaches status=completed, every piece of
// infrastructure (PostgreSQL, Redis, scheduler, worker) is working.
//
//	curl -X POST /jobs -d '{"type":"inline","payload":{"handler_name":"noop"}}'
//	→ watch status become "completed" — your first successful job
func Noop(_ context.Context, _ *domain.Job) error {
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Echo — the args-inspection handler
// ─────────────────────────────────────────────────────────────────────────────

// Echo logs the job's full payload and always returns nil.
//
// Use to verify that job.Payload.Args values are correctly passed through
// the entire pipeline from HTTP submission to handler execution.
//
//	curl -X POST /jobs -d '{
//	  "type":"inline",
//	  "payload":{"handler_name":"echo","args":{"model":"resnet50","epochs":10}}
//	}'
//	→ worker log: INFO msg="echo handler" args=map[epochs:10 model:resnet50]
func Echo(_ context.Context, job *domain.Job) error {
	slog.Info("echo handler",
		"job_id", job.ID,
		"job_name", job.Name,
		"attempt", job.Attempt,
		"handler_name", job.Payload.HandlerName,
		"args", job.Payload.Args,
	)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AlwaysFail — the retry-cycle test handler
// ─────────────────────────────────────────────────────────────────────────────

// AlwaysFail always returns a non-nil error.
//
// Use to test the entire retry and dead-letter cycle:
//   - Submit with max_retries=2
//   - Watch: running→failed(1)→retrying→queued→running→failed(2)→retrying→queued→running→failed(3)→dead
//   - Verify 3 rows in job_executions (one per attempt)
//   - Verify error_message is stored correctly on each failure
//
// The error message includes the attempt number so you can verify
// that attempt counting is working correctly.
func AlwaysFail(_ context.Context, job *domain.Job) error {
	return fmt.Errorf("deliberate failure on attempt %d (testing retry cycle)", job.Attempt)
}

// ─────────────────────────────────────────────────────────────────────────────
// Slow — the context-cancellation reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// Slow simulates a long-running job that correctly respects context cancellation.
//
// This handler is the canonical example of how ANY long-running handler
// should be written. Study the select pattern — it is mandatory for
// handlers that do any waiting, sleeping, or looping.
//
// Configuration via job.Payload.Args:
//   - "duration_seconds": float64 — how long to run (default: 5)
//
// Use cases:
//  1. Test graceful shutdown: start a slow job, send SIGTERM, verify the
//     pool waits up to ShutdownTimeout before forcing exit.
//  2. Test job deadlines: submit with a short deadline, verify the handler
//     stops cleanly and the job is retried.
//  3. Test heartbeat under load: verify last_heartbeat updates in the
//     workers table while a slow job is running.
//
// THE MOST IMPORTANT PATTERN IN THIS FILE:
//
//	select {
//	case <-time.After(duration):
//	    return nil         // completed normally
//	case <-ctx.Done():
//	    return ctx.Err()  // cancelled — always propagate this error
//	}
//
// Without ctx.Done(), the handler ignores SIGTERM and blocks shutdown
// for the entire ShutdownTimeout (default 30s).
func Slow(ctx context.Context, job *domain.Job) error {
	// Read duration from args, default 5 seconds.
	// JSON numbers decode as float64, so we type-assert to float64.
	durationSec := 5.0
	if args := job.Payload.Args; args != nil {
		if v, ok := args["duration_seconds"]; ok {
			if n, ok := v.(float64); ok {
				durationSec = n
			}
		}
	}

	slog.Info("slow handler started",
		"job_id", job.ID,
		"duration_seconds", durationSec,
		"attempt", job.Attempt,
	)

	// THE CORRECT PATTERN: always select on ctx.Done() alongside your wait.
	// This makes the handler responsive to:
	//   - Worker SIGTERM (cancel() called on pool context)
	//   - Job deadline exceeded (context.WithDeadline fires)
	//   - Manual job cancellation (future feature)
	select {
	case <-time.After(time.Duration(durationSec * float64(time.Second))):
		slog.Info("slow handler completed normally", "job_id", job.ID)
		return nil

	case <-ctx.Done():
		slog.Info("slow handler cancelled",
			"job_id", job.ID,
			"reason", ctx.Err(),
		)
		// Always return ctx.Err() here, not a custom error.
		// This signals to the pool that the failure was due to cancellation,
		// not a bug in the handler itself — the job should be retried.
		return ctx.Err()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PanicRecover — the safety wrapper
// ─────────────────────────────────────────────────────────────────────────────

// PanicRecover wraps a HandlerFunc and converts any panic into an error.
//
// Without this wrapper, a panicking handler crashes the worker goroutine
// permanently. With it, the panic is caught, converted to an error, and the
// job is marked failed (and retried if retries remain).
//
// Use as a decorator when calling handlers that might panic:
//
//	registry.Register("risky_handler", handlers.PanicRecover(myRiskyHandler))
//
// Note: prefer fixing the root cause over using this wrapper. PanicRecover
// is appropriate for calling third-party code you don't control, not for
// masking bugs in your own handlers.
func PanicRecover(fn func(ctx context.Context, job *domain.Job) error) func(ctx context.Context, job *domain.Job) error {
	return func(ctx context.Context, job *domain.Job) (retErr error) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("handler panic recovered",
					"job_id", job.ID,
					"handler", job.Payload.HandlerName,
					"panic_value", r,
				)
				retErr = fmt.Errorf("handler panicked: %v (job_id=%s)", r, job.ID)
			}
		}()
		return fn(ctx, job)
	}
}
