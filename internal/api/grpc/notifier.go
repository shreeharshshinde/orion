// Package grpc — notifier.go
// Notifier listens on the PostgreSQL channel "orion_job_events" and fans out
// every notification to the in-memory Broadcaster.
//
// Architecture:
//   - Uses a single dedicated *pgx.Conn (not from the pool). LISTEN is
//     session-scoped; a pooled connection would lose the subscription when
//     returned to the pool.
//   - The postgres store fires pg_notify('orion_job_events', <json>) inside
//     TransitionJobState, so every status change is delivered here with
//     sub-millisecond latency — no polling required.
//   - On reconnect (network blip, PG restart) the notifier re-issues LISTEN
//     automatically with exponential backoff.
//
// Notification payload JSON:
//
//	{"job_id":"<uuid>","job_name":"<name>","prev":"<status>","next":"<status>"}
package grpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	orionv1 "github.com/shreeharshshinde/orion/proto/orion/v1"
)

const pgChannel = "orion_job_events"

// notifyPayload is the JSON structure sent via pg_notify.
type notifyPayload struct {
	JobID    string `json:"job_id"`
	JobName  string `json:"job_name"`
	Prev     string `json:"prev"`
	Next     string `json:"next"`
	WorkerID string `json:"worker_id,omitempty"`
}

// Notifier connects to PostgreSQL, issues LISTEN, and publishes every
// received notification to the Broadcaster.
type Notifier struct {
	dsn         string
	broadcaster *Broadcaster
	logger      *slog.Logger
}

// NewNotifier creates a Notifier. Call Run to start listening.
func NewNotifier(dsn string, b *Broadcaster, logger *slog.Logger) *Notifier {
	return &Notifier{dsn: dsn, broadcaster: b, logger: logger}
}

// Run blocks until ctx is cancelled, reconnecting on errors with backoff.
// Call it in a dedicated goroutine: go notifier.Run(ctx).
func (n *Notifier) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if err := n.listen(ctx); err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			n.logger.Warn("pg notifier disconnected, reconnecting",
				"err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		return // ctx cancelled inside listen
	}
}

// listen opens a dedicated connection, issues LISTEN, and processes
// notifications until ctx is cancelled or the connection breaks.
func (n *Notifier) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, n.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+pgChannel); err != nil {
		return err
	}
	n.logger.Info("pg notifier listening", "channel", pgChannel)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return err
		}

		var p notifyPayload
		if err := json.Unmarshal([]byte(notification.Payload), &p); err != nil {
			n.logger.Warn("pg notifier: bad payload", "payload", notification.Payload, "err", err)
			continue
		}

		n.broadcaster.Publish(p.JobID, &orionv1.JobEvent{
			JobId:          p.JobID,
			JobName:        p.JobName,
			PreviousStatus: p.Prev,
			NewStatus:      p.Next,
			WorkerId:       p.WorkerID,
			Timestamp:      timestamppb.Now(),
		})
	}
}
