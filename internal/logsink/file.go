package logsink

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// FileSink writes execution logs to a local directory.
//
// Directory layout:
//
//	<BaseDir>/<jobID>/<execID>.log
//
// URI format returned by Flush:
//
//	file:///absolute/path/to/<jobID>/<execID>.log
//
// FileSink creates the directory tree with os.MkdirAll on the first flush.
// It is safe to use across multiple goroutines — there is no shared mutable
// state; each Flush creates its own subdirectory and file.
//
// FileSink is intended for local development and testing. For production,
// use S3Sink which writes to durable object storage.
type FileSink struct {
	baseDir string
}

// NewFileSink creates a FileSink that stores logs under baseDir.
// baseDir is created lazily on first use.
func NewFileSink(baseDir string) (*FileSink, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("logsink: FileSink baseDir must not be empty")
	}
	// Resolve to absolute path so the URI is unambiguous.
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("logsink: resolving baseDir %q: %w", baseDir, err)
	}
	return &FileSink{baseDir: abs}, nil
}

// Writer returns a fresh *bytes.Buffer for the executor to write into.
// The buffer is passed back to Flush by the worker pool.
func (f *FileSink) Writer() io.Writer { return &bytes.Buffer{} }

// Flush writes data to <baseDir>/<jobID>/<execID>.log.
//
// Returns "" (and no error) when data is empty — no file is created for
// executions that produced no log output.
func (f *FileSink) Flush(ctx context.Context, jobID, execID uuid.UUID, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	dir := filepath.Join(f.baseDir, jobID.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("logsink: creating log dir %s: %w", dir, err)
	}

	logPath := filepath.Join(dir, execID.String()+".log")

	// Use os.WriteFile — atomic enough for local dev; no need for temp+rename here.
	if err := os.WriteFile(logPath, data, 0o644); err != nil {
		return "", fmt.Errorf("logsink: writing log file %s: %w", logPath, err)
	}

	return "file://" + logPath, nil
}
