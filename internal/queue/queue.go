package queue

import (
	"context"
	"time"

	"github.com/shreeharshshinde/orion/internal/domain"
)

// AckFunc must be called after successful job processing to remove
// the message from the queue. Calling it with a non-nil error NACKs
// the message, making it visible for redelivery.
type AckFunc func(err error) error

// Queue is the primary abstraction over the message broker.
// All worker and scheduler code depends on this interface, NOT on Redis directly.
// This enables swapping Redis for NATS JetStream or Kafka without touching callers.
type Queue interface {
	// Enqueue pushes a job into the specified queue.
	// If the job has a ScheduledAt in the future, it should be held until then.
	Enqueue(ctx context.Context, job *domain.Job) error

	// Dequeue blocks until a job is available or ctx is cancelled.
	// Returns the job and an AckFunc. The caller MUST call AckFunc when done.
	// The visibility timeout determines how long a dequeued but un-acked message
	// stays invisible to other consumers before being redelivered.
	Dequeue(ctx context.Context, queueName string, visibilityTimeout time.Duration) (*domain.Job, AckFunc, error)

	// Len returns the number of pending messages in the named queue.
	Len(ctx context.Context, queueName string) (int64, error)

	// Dead moves a job to the dead-letter queue for the given queue.
	Dead(ctx context.Context, job *domain.Job, reason string) error

	// Flush removes all messages from the named queue (testing/admin only).
	Flush(ctx context.Context, queueName string) error

	// Close shuts down the queue connection gracefully.
	Close() error
}

// QueueNames contains the well-known queue identifiers.
// Custom queue names are also supported.
const (
	QueueDefault  = "orion:queue:default"
	QueueHigh     = "orion:queue:high"
	QueueLow      = "orion:queue:low"
	QueueDead     = "orion:queue:dead"
	QueueScheduled = "orion:queue:scheduled" // sorted set for future-scheduled jobs
)

// Message is the envelope stored in the queue.
// The Job is serialized into Body as JSON.
type Message struct {
	ID        string         `json:"id"`
	JobID     string         `json:"job_id"`
	QueueName string         `json:"queue_name"`
	Body      []byte         `json:"body"`
	Attempt   int            `json:"attempt"`
	EnqueuedAt time.Time    `json:"enqueued_at"`
}