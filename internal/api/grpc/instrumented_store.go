package grpc

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/store"
	orionv1 "github.com/shreeharshshinde/orion/proto/orion/v1"
)

// InstrumentedStore wraps store.Store and publishes job events to the
// Broadcaster on every state transition.
//
// This connects the store layer to the gRPC streaming layer without creating
// a dependency from store → grpc. The store package stays free of transport
// concerns; the wrapper adds broadcast calls at the boundary.
//
// Usage in cmd/api/main.go:
//
//	broadcaster := grpcserver.NewBroadcaster()
//	instrumentedStore := grpcserver.NewInstrumentedStore(pgStore, broadcaster)
//	// pass instrumentedStore to worker pool and gRPC server
type InstrumentedStore struct {
	store.Store
	broadcaster *Broadcaster
}

// NewInstrumentedStore wraps s with event broadcasting.
func NewInstrumentedStore(s store.Store, b *Broadcaster) *InstrumentedStore {
	return &InstrumentedStore{Store: s, broadcaster: b}
}

// MarkJobRunning publishes a "running" event after the DB update succeeds.
func (s *InstrumentedStore) MarkJobRunning(ctx context.Context, id uuid.UUID, workerID string) error {
	err := s.Store.MarkJobRunning(ctx, id, workerID)
	if err == nil {
		s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
			JobId:     id.String(),
			NewStatus: "running",
			WorkerId:  workerID,
			Timestamp: timestamppb.Now(),
		})
	}
	return err
}

// MarkJobCompleted publishes a "completed" event after the DB update succeeds.
func (s *InstrumentedStore) MarkJobCompleted(ctx context.Context, id uuid.UUID) error {
	err := s.Store.MarkJobCompleted(ctx, id)
	if err == nil {
		s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
			JobId:     id.String(),
			NewStatus: "completed",
			Timestamp: timestamppb.Now(),
		})
	}
	return err
}

// MarkJobFailed publishes a "failed" event after the DB update succeeds.
func (s *InstrumentedStore) MarkJobFailed(ctx context.Context, id uuid.UUID, errMsg string, nextRetryAt *time.Time) error {
	err := s.Store.MarkJobFailed(ctx, id, errMsg, nextRetryAt)
	if err == nil {
		s.broadcaster.Publish(id.String(), &orionv1.JobEvent{
			JobId:        id.String(),
			NewStatus:    "failed",
			ErrorMessage: errMsg,
			Timestamp:    timestamppb.Now(),
		})
	}
	return err
}
