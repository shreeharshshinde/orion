package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// minimalStore embeds a no-op base and lets tests override specific methods.
type minimalStore struct{ store.Store }

func (minimalStore) MarkJobRunning(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (minimalStore) MarkJobCompleted(_ context.Context, _ uuid.UUID) error          { return nil }
func (minimalStore) MarkJobFailed(_ context.Context, _ uuid.UUID, _ string, _ *time.Time) error {
	return nil
}
func (minimalStore) TransitionJobState(_ context.Context, _ uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
	return nil
}

func TestInstrumentedStore_MarkJobRunning_Broadcasts(t *testing.T) {
	b := NewBroadcaster()
	s := NewInstrumentedStore(minimalStore{}, b)
	id := uuid.New()

	ch, unsub := b.Subscribe(id.String())
	defer unsub()

	_ = s.MarkJobRunning(context.Background(), id, "worker-1")

	e := <-ch
	if e.NewStatus != "running" {
		t.Errorf("expected running, got %q", e.NewStatus)
	}
}

func TestInstrumentedStore_MarkJobCompleted_Broadcasts(t *testing.T) {
	b := NewBroadcaster()
	s := NewInstrumentedStore(minimalStore{}, b)
	id := uuid.New()

	ch, unsub := b.Subscribe(id.String())
	defer unsub()

	_ = s.MarkJobCompleted(context.Background(), id)

	e := <-ch
	if e.NewStatus != "completed" {
		t.Errorf("expected completed, got %q", e.NewStatus)
	}
}

func TestInstrumentedStore_MarkJobFailed_Broadcasts(t *testing.T) {
	b := NewBroadcaster()
	s := NewInstrumentedStore(minimalStore{}, b)
	id := uuid.New()

	ch, unsub := b.Subscribe(id.String())
	defer unsub()

	_ = s.MarkJobFailed(context.Background(), id, "OOM", nil)

	e := <-ch
	if e.NewStatus != "failed" || e.ErrorMessage != "OOM" {
		t.Errorf("unexpected event: %+v", e)
	}
}

func TestInstrumentedStore_TransitionJobState_Broadcasts(t *testing.T) {
	b := NewBroadcaster()
	s := NewInstrumentedStore(minimalStore{}, b)
	id := uuid.New()

	cases := []struct {
		from, to domain.JobStatus
	}{
		{domain.JobStatusQueued, domain.JobStatusScheduled},
		{domain.JobStatusFailed, domain.JobStatusRetrying},
		{domain.JobStatusRetrying, domain.JobStatusQueued},
		{domain.JobStatusQueued, domain.JobStatusCancelled},
	}

	for _, tc := range cases {
		ch, unsub := b.Subscribe(id.String())

		_ = s.TransitionJobState(context.Background(), id, tc.from, tc.to)

		e := <-ch
		unsub()

		if e.NewStatus != string(tc.to) {
			t.Errorf("transition %s→%s: expected %q, got %q", tc.from, tc.to, tc.to, e.NewStatus)
		}
	}
}
