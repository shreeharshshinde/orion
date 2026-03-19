package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
)

const (
	consumerGroup  = "orion-workers"
	pendingSweepInterval = 30 * time.Second
)

// RedisQueue implements queue.Queue using Redis Streams.
//
// Architecture decision: Redis Streams with consumer groups give us:
//  - At-least-once delivery via XACK
//  - Pending entry list (PEL) for reclaiming un-acked messages
//  - Consumer group semantics (each message delivered to one consumer)
//
// We do NOT use Redis Lists (LPUSH/BRPOP) because they offer no delivery
// guarantee — a crashed worker drops the message permanently.
type RedisQueue struct {
	client *redis.Client
	logger *slog.Logger
}

// New creates a RedisQueue and ensures consumer groups exist for all known queues.
func New(client *redis.Client, logger *slog.Logger) (*RedisQueue, error) {
	q := &RedisQueue{client: client, logger: logger}

	knownQueues := []string{
		queue.QueueDefault,
		queue.QueueHigh,
		queue.QueueLow,
	}
	for _, name := range knownQueues {
		if err := q.ensureConsumerGroup(context.Background(), name); err != nil {
			return nil, fmt.Errorf("ensuring consumer group for %s: %w", name, err)
		}
	}
	return q, nil
}

func (r *RedisQueue) ensureConsumerGroup(ctx context.Context, streamName string) error {
	// MKSTREAM creates the stream if it doesn't exist.
	err := r.client.XGroupCreateMkStream(ctx, streamName, consumerGroup, "$").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	return nil
}

// Enqueue serializes the job and adds it to the stream.
// Priority is encoded in the stream name selection (high/default/low).
func (r *RedisQueue) Enqueue(ctx context.Context, job *domain.Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshaling job: %w", err)
	}

	streamName := r.streamForQueue(job.QueueName)

	msg := &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{
			"job_id":  job.ID.String(),
			"payload": string(body),
		},
	}

	// For scheduled jobs, we use a sorted set keyed by unix timestamp.
	// A separate goroutine moves them to the stream when the time arrives.
	if job.ScheduledAt != nil && job.ScheduledAt.After(time.Now()) {
		return r.enqueueScheduled(ctx, job, body)
	}

	if err := r.client.XAdd(ctx, msg).Err(); err != nil {
		return fmt.Errorf("XADD to stream %s: %w", streamName, err)
	}
	return nil
}

func (r *RedisQueue) enqueueScheduled(ctx context.Context, job *domain.Job, body []byte) error {
	score := float64(job.ScheduledAt.Unix())
	return r.client.ZAdd(ctx, queue.QueueScheduled, redis.Z{
		Score:  score,
		Member: string(body),
	}).Err()
}

// Dequeue blocks until a message is available using XREADGROUP.
// Returns the job and an AckFunc. The caller MUST invoke AckFunc.
// If the caller crashes without calling AckFunc, the PEL reclaim sweeper
// will redeliver the message after visibilityTimeout.
func (r *RedisQueue) Dequeue(ctx context.Context, queueName string, visibilityTimeout time.Duration) (*domain.Job, queue.AckFunc, error) {
	streamName := r.streamForQueue(queueName)
	consumerID := fmt.Sprintf("worker-%d", time.Now().UnixNano()) // per-call consumer ID

	args := &redis.XReadGroupArgs{
		Group:    consumerGroup,
		Consumer: consumerID,
		Streams:  []string{streamName, ">"},
		Count:    1,
		Block:    5 * time.Second, // block up to 5s, then return for ctx check
		NoAck:    false,
	}

	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		streams, err := r.client.XReadGroup(ctx, args).Result()
		if err == redis.Nil {
			// No messages within block duration — loop and try again
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("XREADGROUP: %w", err)
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				payload, ok := msg.Values["payload"].(string)
				if !ok {
					r.logger.Error("malformed message in stream", "stream", streamName, "msg_id", msg.ID)
					_ = r.client.XAck(ctx, streamName, consumerGroup, msg.ID)
					continue
				}

				var job domain.Job
				if err := json.Unmarshal([]byte(payload), &job); err != nil {
					r.logger.Error("failed to unmarshal job", "err", err, "msg_id", msg.ID)
					_ = r.client.XAck(ctx, streamName, consumerGroup, msg.ID)
					continue
				}

				ackFn := func(processingErr error) error {
					if processingErr == nil {
						return r.client.XAck(ctx, streamName, consumerGroup, msg.ID).Err()
					}
					// NACK: do not ACK — let the PEL reclaim sweeper pick it up
					r.logger.Warn("job processing failed, not acking", "job_id", job.ID, "err", processingErr)
					return nil
				}

				return &job, ackFn, nil
			}
		}
	}
}

// Len returns the approximate number of pending messages in the consumer group.
func (r *RedisQueue) Len(ctx context.Context, queueName string) (int64, error) {
	streamName := r.streamForQueue(queueName)
	info, err := r.client.XInfoGroups(ctx, streamName).Result()
	if err != nil {
		return 0, err
	}
	for _, g := range info {
		if g.Name == consumerGroup {
			return g.Pending, nil
		}
	}
	return 0, nil
}

// Dead moves a job to the dead-letter stream for manual inspection.
func (r *RedisQueue) Dead(ctx context.Context, job *domain.Job, reason string) error {
	body, _ := json.Marshal(map[string]any{
		"job":    job,
		"reason": reason,
		"dead_at": time.Now(),
	})
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: queue.QueueDead,
		Values: map[string]any{"payload": string(body)},
	}).Err()
}

// Flush is for testing and admin tooling only.
func (r *RedisQueue) Flush(ctx context.Context, queueName string) error {
	return r.client.Del(ctx, r.streamForQueue(queueName)).Err()
}

func (r *RedisQueue) Close() error {
	return r.client.Close()
}

// streamForQueue maps logical queue names to Redis stream keys.
func (r *RedisQueue) streamForQueue(name string) string {
	switch name {
	case "high":
		return queue.QueueHigh
	case "low":
		return queue.QueueLow
	default:
		return queue.QueueDefault
	}
}

// ReclaimStalePending is a background task that periodically sweeps the PEL
// for messages that have been in-flight longer than the visibility timeout
// and reclaims them for redelivery. This handles crashed workers.
//
// Call this in a goroutine: go q.ReclaimStalePending(ctx, visibilityTimeout)
func (r *RedisQueue) ReclaimStalePending(ctx context.Context, visibilityTimeout time.Duration) {
	ticker := time.NewTicker(pendingSweepInterval)
	defer ticker.Stop()

	queues := []string{queue.QueueDefault, queue.QueueHigh, queue.QueueLow}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, q := range queues {
				r.reclaimForStream(ctx, q, visibilityTimeout)
			}
		}
	}
}

func (r *RedisQueue) reclaimForStream(ctx context.Context, streamName string, timeout time.Duration) {
	minIdleMs := timeout.Milliseconds()

	// XAUTOCLAIM transfers idle messages from other consumers to us for redelivery
	msgs, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   streamName,
		Group:    consumerGroup,
		Consumer: "reclaimer",
		MinIdle:  time.Duration(minIdleMs) * time.Millisecond,
		Start:    "0",
		Count:    100,
	}).Result()
	if err != nil {
		r.logger.Error("XAUTOCLAIM failed", "stream", streamName, "err", err)
		return
	}

	if len(msgs) > 0 {
		r.logger.Info("reclaimed stale pending messages", "stream", streamName, "count", len(msgs))
	}
}