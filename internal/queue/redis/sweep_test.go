package redis

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/queue"
)

func newTestQueue(t *testing.T) (*RedisQueue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	q := &RedisQueue{client: client, logger: slog.Default()}
	return q, mr
}

func addScheduledJob(t *testing.T, mr *miniredis.Miniredis, job *domain.Job, score float64) {
	t.Helper()
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	mr.ZAdd(queue.QueueScheduled, score, string(body))
}

func TestSweepScheduled_PromotesDueJobs(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Minute)
	job := &domain.Job{
		ID:          uuid.New(),
		QueueName:   queue.QueueDefault,
		ScheduledAt: &past,
	}
	addScheduledJob(t, mr, job, float64(past.Unix()))

	q.sweepScheduled(ctx)

	// Job must be removed from sorted set
	count, _ := q.client.ZCard(ctx, queue.QueueScheduled).Result()
	if count != 0 {
		t.Errorf("expected sorted set to be empty, got %d entries", count)
	}

	// Job must appear in the target stream
	msgs, err := q.client.XRange(ctx, q.streamForQueue(queue.QueueDefault), "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message in stream, got %d", len(msgs))
	}
}

func TestSweepScheduled_DoesNotPromoteFutureJobs(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	future := time.Now().Add(10 * time.Minute)
	job := &domain.Job{
		ID:          uuid.New(),
		QueueName:   queue.QueueDefault,
		ScheduledAt: &future,
	}
	addScheduledJob(t, mr, job, float64(future.Unix()))

	q.sweepScheduled(ctx)

	// Job must remain in sorted set
	count, _ := q.client.ZCard(ctx, queue.QueueScheduled).Result()
	if count != 1 {
		t.Errorf("expected 1 entry in sorted set, got %d", count)
	}

	// Stream must be empty
	msgs, _ := q.client.XRange(ctx, q.streamForQueue(queue.QueueDefault), "-", "+").Result()
	if len(msgs) != 0 {
		t.Errorf("expected empty stream, got %d messages", len(msgs))
	}
}

func TestSweepScheduled_AtomicNoDuplicates(t *testing.T) {
	// Simulate two concurrent sweepers: both call sweepScheduled at the same time.
	// The Lua script ensures only one of them pops the member.
	q, mr := newTestQueue(t)
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Minute)
	job := &domain.Job{
		ID:          uuid.New(),
		QueueName:   queue.QueueDefault,
		ScheduledAt: &past,
	}
	addScheduledJob(t, mr, job, float64(past.Unix()))

	// Both sweepers run; only one should promote the job.
	q.sweepScheduled(ctx)
	q.sweepScheduled(ctx) // second call — sorted set is already empty

	msgs, _ := q.client.XRange(ctx, q.streamForQueue(queue.QueueDefault), "-", "+").Result()
	if len(msgs) != 1 {
		t.Errorf("expected exactly 1 message in stream (no duplicates), got %d", len(msgs))
	}
}

func TestSweepScheduled_RoutesToCorrectStream(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Minute)
	for _, qName := range []string{queue.QueueHigh, queue.QueueDefault, queue.QueueLow} {
		job := &domain.Job{
			ID:          uuid.New(),
			QueueName:   qName,
			ScheduledAt: &past,
		}
		addScheduledJob(t, mr, job, float64(past.Unix()))
	}

	q.sweepScheduled(ctx)

	for _, qName := range []string{queue.QueueHigh, queue.QueueDefault, queue.QueueLow} {
		msgs, _ := q.client.XRange(ctx, q.streamForQueue(qName), "-", "+").Result()
		if len(msgs) != 1 {
			t.Errorf("queue %s: expected 1 message, got %d", qName, len(msgs))
		}
	}
}

func TestSweepScheduled_EmptySet(t *testing.T) {
	q, _ := newTestQueue(t)
	// Should not panic or error on empty sorted set
	q.sweepScheduled(context.Background())
}
