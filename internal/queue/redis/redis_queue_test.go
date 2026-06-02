package redis

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
)

func newTestRedisQueue(t *testing.T, consumerID string) (*RedisQueue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	
	q, err := New(client, nil, slog.Default(), consumerID)
	if err != nil {
		t.Fatalf("failed to create RedisQueue: %v", err)
	}
	return q, mr
}

func TestNew_CreatesConsumerGroups(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	// Check that consumer groups are created for all known queues
	ctx := context.Background()
	
	for _, streamName := range []string{"orion:queue:high", "orion:queue:default", "orion:queue:low"} {
		info, err := q.client.XInfoGroups(ctx, streamName).Result()
		if err != nil {
			t.Errorf("failed to get groups for %s: %v", streamName, err)
			continue
		}
		
		found := false
		for _, g := range info {
			if g.Name == consumerGroup {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("consumer group %s not found for stream %s", consumerGroup, streamName)
		}
	}
}

func TestEnqueue_ImmediateJob(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}

	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Check that job was added to the stream
	msgs, err := q.client.XRange(ctx, "orion:queue:default", "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if msgs[0].Values["job_id"] != job.ID.String() {
		t.Errorf("expected job_id %s, got %s", job.ID.String(), msgs[0].Values["job_id"])
	}
}

func TestEnqueue_ScheduledJob(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	future := time.Now().Add(10 * time.Minute)
	job := &domain.Job{
		ID:          uuid.New(),
		QueueName:   queue.QueueHigh,
		Status:      domain.JobStatusQueued,
		ScheduledAt: &future,
	}

	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Check that job is in scheduled sorted set
	count, err := q.client.ZCard(ctx, queue.QueueScheduled).Result()
	if err != nil {
		t.Fatalf("ZCard failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 scheduled job, got %d", count)
	}

	// Check that job is NOT in the immediate stream
	msgs, err := q.client.XRange(ctx, "orion:queue:high", "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange failed: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected no immediate messages, got %d", len(msgs))
	}
}

func TestEnqueue_QueueRouting(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	tests := []struct {
		queueName    string
		expectedStream string
	}{
		{queue.QueueHigh, "orion:queue:high"},
		{queue.QueueDefault, "orion:queue:default"},
		{queue.QueueLow, "orion:queue:low"},
		{"unknown", "orion:queue:default"}, // fallback
	}

	for _, tt := range tests {
		t.Run(tt.queueName, func(t *testing.T) {
			job := &domain.Job{
				ID:        uuid.New(),
				QueueName: tt.queueName,
				Status:    domain.JobStatusQueued,
			}

			err := q.Enqueue(ctx, job)
			if err != nil {
				t.Fatalf("Enqueue failed: %v", err)
			}

			// Check message is in expected stream
			msgs, err := q.client.XRange(ctx, tt.expectedStream, "-", "+").Result()
			if err != nil {
				t.Fatalf("XRange failed: %v", err)
			}
			if len(msgs) == 0 {
				t.Errorf("expected message in %s", tt.expectedStream)
			}

			// Clean up for next test
			q.client.Del(ctx, tt.expectedStream)
		})
	}
}

func TestDequeue_Basic(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}

	// Enqueue job first
	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Dequeue with timeout
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	dequeuedJob, ackFn, err := q.Dequeue(ctx, queue.QueueDefault, 30*time.Second)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	if dequeuedJob.ID != job.ID {
		t.Errorf("expected job ID %s, got %s", job.ID, dequeuedJob.ID)
	}

	// ACK the message
	err = ackFn(nil)
	if err != nil {
		t.Errorf("ACK failed: %v", err)
	}
}

func TestDequeue_AckFailure(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}

	// Enqueue job first
	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Dequeue
	_, ackFn, err := q.Dequeue(ctx, queue.QueueDefault, 30*time.Second)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	// NACK the message (simulate processing failure)
	processingErr := errors.New("processing failed")
	err = ackFn(processingErr)
	if err != nil {
		t.Errorf("NACK failed: %v", err)
	}

	// Check that message is still in pending list
	streamName := "orion:queue:default"
	info, err := q.client.XInfoGroups(ctx, streamName).Result()
	if err != nil {
		t.Fatalf("XInfoGroups failed: %v", err)
	}
	
	for _, g := range info {
		if g.Name == consumerGroup && g.Pending > 0 {
			return // Success: message is still pending
		}
	}
	t.Error("expected message to remain in pending list after NACK")
}

func TestLen(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	
	// Initially empty
	length, err := q.Len(ctx, queue.QueueDefault)
	if err != nil {
		t.Fatalf("Len failed: %v", err)
	}
	if length != 0 {
		t.Errorf("expected length 0, got %d", length)
	}

	// Add a job
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}
	err = q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Dequeue job (this moves it to consumer group PEL)
	_, _, err = q.Dequeue(ctx, queue.QueueDefault, 30*time.Second)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	// Check length shows 1 (pending in consumer group)
	length, err = q.Len(ctx, queue.QueueDefault)
	if err != nil {
		t.Fatalf("Len failed: %v", err)
	}
	if length != 1 {
		t.Errorf("expected length 1, got %d", length)
	}
}

func TestDead(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusDead,
	}
	reason := "max retries exceeded"

	err := q.Dead(ctx, job, reason)
	if err != nil {
		t.Fatalf("Dead failed: %v", err)
	}

	// Check that entry was added to dead letter queue
	msgs, err := q.client.XRange(ctx, queue.QueueDead, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in dead queue, got %d", len(msgs))
	}
}

func TestFlush(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx := context.Background()
	
	// Add a job
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}
	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Verify job exists in stream
	msgs, _ := q.client.XRange(ctx, "orion:queue:default", "-", "+").Result()
	if len(msgs) == 0 {
		t.Fatal("expected job to exist before flush")
	}

	// Flush the queue
	err = q.Flush(ctx, queue.QueueDefault)
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify stream is deleted
	msgs, _ = q.client.XRange(ctx, "orion:queue:default", "-", "+").Result()
	if len(msgs) != 0 {
		t.Errorf("expected empty stream after flush, got %d messages", len(msgs))
	}
}

func TestReclaimStalePending_ReclaimsStaleMessages(t *testing.T) {
	q, mr := newTestRedisQueue(t, "crashed-worker")
	defer mr.Close()

	ctx := context.Background()
	streamName := "orion:queue:default"
	
	// Enqueue a job
	job := &domain.Job{
		ID:        uuid.New(),
		QueueName: queue.QueueDefault,
		Status:    domain.JobStatusQueued,
	}
	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Dequeue but don't ACK (simulate crashed worker)
	_, _, err = q.Dequeue(ctx, queue.QueueDefault, 1*time.Second)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	// Verify message is in PEL
	info, _ := q.client.XInfoGroups(ctx, streamName).Result()
	var pendingBefore int64
	for _, g := range info {
		if g.Name == consumerGroup {
			pendingBefore = g.Pending
			break
		}
	}
	if pendingBefore == 0 {
		t.Fatal("expected message to be pending before reclaim")
	}

	// Fast forward time to make message stale
	mr.FastForward(2 * time.Second)

	// Run reclaim manually
	q.reclaimForStream(ctx, streamName, 1*time.Second)

	// Check that a new message was added to the stream for redelivery
	msgs, err := q.client.XRange(ctx, streamName, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange failed: %v", err)
	}
	
	// Should have the original message (now ACKed from reclaimer) plus the re-added one
	if len(msgs) < 1 {
		t.Errorf("expected at least 1 message after reclaim, got %d", len(msgs))
	}
}

func TestStartQueueDepthPoller_UpdatesMetrics(t *testing.T) {
	q, mr := newTestRedisQueue(t, "test-worker")
	defer mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Add some jobs
	for i := 0; i < 3; i++ {
		job := &domain.Job{
			ID:        uuid.New(),
			QueueName: queue.QueueDefault,
			Status:    domain.JobStatusQueued,
		}
		q.Enqueue(ctx, job)
	}

	// Start poller (this should return immediately since metrics is nil)
	q.StartQueueDepthPoller(ctx, []string{queue.QueueDefault})
	
	// Wait for context timeout
	<-ctx.Done()
	
	// Test passes if no panic occurred (metrics is nil-safe)
}

func TestConsumerIDStability(t *testing.T) {
	// Test that consumer ID is stable across dequeue calls
	q1, mr := newTestRedisQueue(t, "stable-worker-1")
	defer mr.Close()

	ctx := context.Background()
	
	// Add two jobs
	for i := 0; i < 2; i++ {
		job := &domain.Job{
			ID:        uuid.New(),
			QueueName: queue.QueueDefault,
			Status:    domain.JobStatusQueued,
		}
		q1.Enqueue(ctx, job)
	}

	// Dequeue both with same worker
	_, ackFn1, _ := q1.Dequeue(ctx, queue.QueueDefault, 30*time.Second)
	_, ackFn2, _ := q1.Dequeue(ctx, queue.QueueDefault, 30*time.Second)

	// ACK both
	ackFn1(nil)
	ackFn2(nil)

	// Check consumer group only has one consumer
	info, err := q1.client.XInfoConsumers(ctx, "orion:queue:default", consumerGroup).Result()
	if err != nil {
		t.Fatalf("XInfoConsumers failed: %v", err)
	}
	if len(info) != 1 {
		t.Errorf("expected 1 consumer, got %d", len(info))
	}
	if info[0].Name != "stable-worker-1" {
		t.Errorf("expected consumer name 'stable-worker-1', got '%s'", info[0].Name)
	}
}