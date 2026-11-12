package logsink

import (
	"context"
	"io"

	"github.com/google/uuid"
)

// NilSink is the default no-op LogSink.
// It discards all log output and always returns an empty URI.
// Use this in local development where you don't need log persistence.
//
// NilSink is goroutine-safe — the zero value is usable directly.
type NilSink struct{}

// NewNilSink creates a NilSink. Identical to using &NilSink{} directly.
func NewNilSink() *NilSink { return &NilSink{} }

// Writer returns io.Discard so the executor's log writes cost nothing.
func (n *NilSink) Writer() io.Writer { return io.Discard }

// Flush discards all data and returns an empty URI.
// Never returns an error — NilSink is always "successful" (by definition).
func (n *NilSink) Flush(_ context.Context, _, _ uuid.UUID, _ []byte) (string, error) {
	return "", nil
}
