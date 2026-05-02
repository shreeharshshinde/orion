package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/store"
	orionv1 "github.com/shreeharshshinde/orion/proto/orion/v1"
)

const (
	// pollInterval is how often WatchJob polls the DB when no broadcaster event
	// arrives. Phase 8 replaces this with PG LISTEN/NOTIFY for sub-10ms latency.
	pollInterval = 500 * time.Millisecond
)

// Server implements orionv1.JobServiceServer.
type Server struct {
	orionv1.UnimplementedJobServiceServer
	store       store.Store
	broadcaster *Broadcaster
	logger      *slog.Logger
}

// NewServer creates a gRPC Server.
func NewServer(s store.Store, b *Broadcaster, logger *slog.Logger) *Server {
	return &Server{store: s, broadcaster: b, logger: logger}
}

// RegisterGRPCServer registers the Server with a grpc.Server.
func RegisterGRPCServer(grpcSrv *grpc.Server, s *Server) {
	orionv1.RegisterJobServiceServer(grpcSrv, s)
}

// ─── SubmitJob ────────────────────────────────────────────────────────────────

func (s *Server) SubmitJob(ctx context.Context, req *orionv1.SubmitJobRequest) (*orionv1.Job, error) {
	ctx, span := observability.Tracer("orion.grpc").Start(ctx, "grpc.SubmitJob")
	defer span.End()

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Type == "" {
		return nil, status.Error(codes.InvalidArgument, "type is required")
	}

	// Idempotency: return existing job if key already used
	if req.IdempotencyKey != "" {
		existing, err := s.store.GetJobByIdempotencyKey(ctx, req.IdempotencyKey)
		if err == nil {
			return domainJobToProto(existing), nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.Internal, "checking idempotency key: %v", err)
		}
	}

	job := &domain.Job{
		Name:           req.Name,
		Type:           domain.JobType(req.Type),
		QueueName:      req.QueueName,
		Priority:       domain.JobPriority(req.Priority),
		MaxRetries:     int(req.MaxRetries),
		IdempotencyKey: req.IdempotencyKey,
		Payload:        protoPayloadToDomain(req.Payload),
	}
	if job.Priority == 0 {
		job.Priority = domain.PriorityNormal
	}
	if job.MaxRetries == 0 {
		job.MaxRetries = 3
	}

	created, err := s.store.CreateJob(ctx, job)
	if err != nil {
		s.logger.Error("gRPC SubmitJob: store error", "err", err)
		return nil, status.Errorf(codes.Internal, "creating job: %v", err)
	}

	span.SetAttributes(attribute.String("job.id", created.ID.String()))
	return domainJobToProto(created), nil
}

// ─── GetJob ───────────────────────────────────────────────────────────────────

func (s *Server) GetJob(ctx context.Context, req *orionv1.GetJobRequest) (*orionv1.Job, error) {
	ctx, span := observability.Tracer("orion.grpc").Start(ctx, "grpc.GetJob")
	defer span.End()

	id, err := uuid.Parse(req.JobId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "job_id must be a valid UUID")
	}

	job, err := s.store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "job not found")
		}
		return nil, status.Errorf(codes.Internal, "getting job: %v", err)
	}

	span.SetAttributes(
		attribute.String("job.id", job.ID.String()),
		attribute.String("job.status", string(job.Status)),
	)
	return domainJobToProto(job), nil
}

// ─── WatchJob ─────────────────────────────────────────────────────────────────

// WatchJob streams job status events until the job reaches a terminal state
// or the client disconnects.
//
// Event delivery (Phase 7 — broadcaster + polling fallback):
//  1. Send current status as synthetic first event.
//  2. Subscribe to broadcaster for real-time events (fast path: <1ms).
//  3. Poll DB every 500ms as fallback for missed broadcast events.
//  4. Close stream on terminal state or client disconnect.
func (s *Server) WatchJob(req *orionv1.WatchJobRequest, stream grpc.ServerStreamingServer[orionv1.JobEvent]) error {
	ctx := stream.Context()

	id, err := uuid.Parse(req.JobId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "job_id must be a valid UUID")
	}

	job, err := s.store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return status.Error(codes.NotFound, "job not found")
		}
		return status.Errorf(codes.Internal, "getting job: %v", err)
	}

	s.logger.Info("gRPC WatchJob started", "job_id", id, "status", job.Status)

	// Send current status as first event so client knows the starting state
	if err := stream.Send(&orionv1.JobEvent{
		JobId:          id.String(),
		JobName:        job.Name,
		PreviousStatus: string(job.Status),
		NewStatus:      string(job.Status),
		Timestamp:      timestamppb.Now(),
	}); err != nil {
		return err
	}

	if job.IsTerminal() {
		return nil // already done — close immediately
	}

	ch, unsubscribe := s.broadcaster.Subscribe(id.String())
	defer unsubscribe() // CRITICAL: prevents goroutine/channel leak

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastStatus := job.Status

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("gRPC WatchJob: client disconnected", "job_id", id)
			return nil

		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
			if isTerminalStatus(event.NewStatus) {
				return nil
			}
			lastStatus = domain.JobStatus(event.NewStatus)

		case <-ticker.C:
			// Polling fallback: catch status changes missed by broadcaster
			current, err := s.store.GetJob(ctx, id)
			if err != nil {
				s.logger.Warn("gRPC WatchJob: poll error", "job_id", id, "err", err)
				continue
			}
			if current.Status != lastStatus {
				event := &orionv1.JobEvent{
					JobId:          id.String(),
					JobName:        current.Name,
					PreviousStatus: string(lastStatus),
					NewStatus:      string(current.Status),
					WorkerId:       current.WorkerID,
					ErrorMessage:   current.ErrorMessage,
					Attempt:        int32(current.Attempt),
					Timestamp:      timestamppb.Now(),
				}
				if err := stream.Send(event); err != nil {
					return err
				}
				lastStatus = current.Status
				if current.IsTerminal() {
					return nil
				}
			}
		}
	}
}

// ─── WatchPipeline ────────────────────────────────────────────────────────────

func (s *Server) WatchPipeline(req *orionv1.WatchPipelineRequest, stream grpc.ServerStreamingServer[orionv1.PipelineEvent]) error {
	ctx := stream.Context()

	id, err := uuid.Parse(req.PipelineId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "pipeline_id must be a valid UUID")
	}

	pipeline, err := s.store.GetPipeline(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return status.Error(codes.NotFound, "pipeline not found")
		}
		return status.Errorf(codes.Internal, "getting pipeline: %v", err)
	}

	if err := stream.Send(&orionv1.PipelineEvent{
		PipelineId:     id.String(),
		PipelineName:   pipeline.Name,
		PipelineStatus: string(pipeline.Status),
		Timestamp:      timestamppb.Now(),
	}); err != nil {
		return err
	}

	if isPipelineTerminal(string(pipeline.Status)) {
		return nil
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	lastStatus := pipeline.Status

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			current, err := s.store.GetPipeline(ctx, id)
			if err != nil {
				continue
			}
			if current.Status != lastStatus {
				if err := stream.Send(&orionv1.PipelineEvent{
					PipelineId:     id.String(),
					PipelineName:   current.Name,
					PipelineStatus: string(current.Status),
					Timestamp:      timestamppb.Now(),
				}); err != nil {
					return err
				}
				lastStatus = current.Status
				if isPipelineTerminal(string(current.Status)) {
					return nil
				}
			}
		}
	}
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

func domainJobToProto(j *domain.Job) *orionv1.Job {
	pb := &orionv1.Job{
		JobId:        j.ID.String(),
		Name:         j.Name,
		Type:         string(j.Type),
		Status:       string(j.Status),
		QueueName:    j.QueueName,
		Priority:     int32(j.Priority),
		Attempt:      int32(j.Attempt),
		MaxRetries:   int32(j.MaxRetries),
		WorkerId:     j.WorkerID,
		ErrorMessage: j.ErrorMessage,
		CreatedAt:    timestamppb.New(j.CreatedAt),
		UpdatedAt:    timestamppb.New(j.UpdatedAt),
	}
	if j.CompletedAt != nil {
		pb.CompletedAt = timestamppb.New(*j.CompletedAt)
	}
	return pb
}

func protoPayloadToDomain(p *orionv1.JobPayload) domain.JobPayload {
	if p == nil {
		return domain.JobPayload{}
	}
	payload := domain.JobPayload{HandlerName: p.HandlerName}
	if len(p.Args) > 0 {
		payload.Args = make(map[string]any, len(p.Args))
		for k, v := range p.Args {
			payload.Args[k] = v
		}
	}
	return payload
}

func isTerminalStatus(s string) bool {
	switch domain.JobStatus(s) {
	case domain.JobStatusCompleted, domain.JobStatusDead, domain.JobStatusCancelled:
		return true
	}
	return false
}

func isPipelineTerminal(s string) bool {
	switch domain.PipelineStatus(s) {
	case domain.PipelineStatusCompleted, domain.PipelineStatusFailed, domain.PipelineStatusCancelled:
		return true
	}
	return false
}
