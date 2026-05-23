// Package cancel provides a Redis pub/sub mechanism for signalling in-flight
// job cancellations across process boundaries (API → worker).
package cancel

import (
	"context"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const channel = "orion:cancel"

// Signaler can publish and subscribe to job cancel signals.
type Signaler interface {
	// Publish sends a cancel signal for the given job ID.
	Publish(ctx context.Context, jobID uuid.UUID) error
	// Subscribe returns a channel that receives job IDs to cancel.
	// The channel is closed when ctx is cancelled.
	Subscribe(ctx context.Context) <-chan uuid.UUID
}

type redisSignaler struct {
	client *redis.Client
}

// NewRedisSignaler creates a Signaler backed by Redis pub/sub.
func NewRedisSignaler(client *redis.Client) Signaler {
	return &redisSignaler{client: client}
}

func (s *redisSignaler) Publish(ctx context.Context, jobID uuid.UUID) error {
	return s.client.Publish(ctx, channel, jobID.String()).Err()
}

func (s *redisSignaler) Subscribe(ctx context.Context) <-chan uuid.UUID {
	out := make(chan uuid.UUID, 16)
	sub := s.client.Subscribe(ctx, channel)
	go func() {
		defer close(out)
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				id, err := uuid.Parse(msg.Payload)
				if err != nil {
					continue
				}
				select {
				case out <- id:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
