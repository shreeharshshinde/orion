package grpc_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/google/uuid"
	grpcserver "github.com/shreeharshshinde/orion/internal/api/grpc"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
	orionv1 "github.com/shreeharshshinde/orion/proto/orion/v1"
)

const bufSize = 1 << 20 // 1 MiB

// ─── Test helpers ─────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func startTestServer(t *testing.T, fs store.Store) (orionv1.JobServiceClient, *grpcserver.Broadcaster) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	b := grpcserver.NewBroadcaster()
	grpcserver.RegisterGRPCServer(srv, grpcserver.NewServer(fs, b, testLogger()))

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop(); lis.Close() })

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return orionv1.NewJobServiceClient(conn), b
}

// fakeStore is a minimal store.Store implementation for unit tests.
type fakeStore struct {
	createJobFn           func(context.Context, *domain.Job) (*domain.Job, error)
	getJobFn              func(context.Context, uuid.UUID) (*domain.Job, error)
	getJobByIdempotencyFn func(context.Context, string) (*domain.Job, error)
	getPipelineFn         func(context.Context, uuid.UUID) (*domain.Pipeline, error)
}

func (f *fakeStore) CreateJob(ctx context.Context, job *domain.Job) (*domain.Job, error) {
	if f.createJobFn != nil {
		return f.createJobFn(ctx, job)
	}
	job.ID = uuid.New()
	now := time.Now()
	job.CreatedAt, job.UpdatedAt, job.Status = now, now, domain.JobStatusQueued
	return job, nil
}
func (f *fakeStore) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	if f.getJobFn != nil {
		return f.getJobFn(ctx, id)
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error) {
	if f.getJobByIdempotencyFn != nil {
		return f.getJobByIdempotencyFn(ctx, key)
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	if f.getPipelineFn != nil {
		return f.getPipelineFn(ctx, id)
	}
	return nil, store.ErrNotFound
}

// Remaining store.Store stubs — not exercised in gRPC unit tests.
func (f *fakeStore) ListJobs(_ context.Context, _ store.JobFilter) ([]*domain.Job, error) {
	return nil, nil
}
func (f *fakeStore) TransitionJobState(_ context.Context, _ uuid.UUID, _, _ domain.JobStatus, _ ...store.TransitionOption) error {
	return nil
}
func (f *fakeStore) ClaimPendingJobs(_ context.Context, _, _ string, _ int) ([]*domain.Job, error) {
	return nil, nil
}
func (f *fakeStore) MarkJobRunning(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (f *fakeStore) MarkJobCompleted(_ context.Context, _ uuid.UUID) error         { return nil }
func (f *fakeStore) MarkJobFailed(_ context.Context, _ uuid.UUID, _ string, _ *time.Time) error {
	return nil
}
func (f *fakeStore) ReclaimOrphanedJobs(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}
func (f *fakeStore) DeleteJob(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeStore) RecordExecution(_ context.Context, _ *domain.JobExecution) error { return nil }
func (f *fakeStore) GetExecutions(_ context.Context, _ uuid.UUID) ([]*domain.JobExecution, error) {
	return nil, nil
}
func (f *fakeStore) RegisterWorker(_ context.Context, _ *domain.Worker) error { return nil }
func (f *fakeStore) Heartbeat(_ context.Context, _ string) error              { return nil }
func (f *fakeStore) ListActiveWorkers(_ context.Context, _ time.Duration) ([]*domain.Worker, error) {
	return nil, nil
}
func (f *fakeStore) DeregisterWorker(_ context.Context, _ string) error { return nil }
func (f *fakeStore) CreatePipeline(_ context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	return p, nil
}
func (f *fakeStore) ListPipelines(_ context.Context, _ store.PipelineFilter) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) ListPipelinesByStatus(_ context.Context, _ domain.PipelineStatus, _ int) ([]*domain.Pipeline, error) {
	return nil, nil
}
func (f *fakeStore) UpdatePipelineStatus(_ context.Context, _ uuid.UUID, _ domain.PipelineStatus) error {
	return nil
}
func (f *fakeStore) AddPipelineJob(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID) error {
	return nil
}
func (f *fakeStore) GetPipelineJobs(_ context.Context, _ uuid.UUID) ([]*store.PipelineJobStatus, error) {
	return nil, nil
}

// QueueConfigStore stubs — Phase 8 (not exercised by gRPC tests)
func (f *fakeStore) ListQueueConfigs(_ context.Context) ([]*store.QueueConfig, error) {
	return nil, nil
}
func (f *fakeStore) GetQueueConfig(_ context.Context, _ string) (*store.QueueConfig, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) UpsertQueueConfig(_ context.Context, cfg *store.QueueConfig) (*store.QueueConfig, error) {
	return cfg, nil
}

// makeJob returns a minimal domain.Job for use in tests.
func makeJob(status domain.JobStatus) *domain.Job {
	now := time.Now()
	return &domain.Job{
		ID:        uuid.New(),
		Name:      "test-job",
		Type:      domain.JobTypeInline,
		Status:    status,
		QueueName: "default",
		Priority:  domain.PriorityNormal,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ─── SubmitJob tests ──────────────────────────────────────────────────────────

func TestSubmitJob_Valid(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	resp, err := client.SubmitJob(context.Background(), &orionv1.SubmitJobRequest{
		Name: "train-mnist", Type: "inline",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.JobId == "" {
		t.Fatal("expected non-empty job_id")
	}
	if resp.Status != "queued" {
		t.Fatalf("expected status=queued, got %s", resp.Status)
	}
}

func TestSubmitJob_MissingName(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	_, err := client.SubmitJob(context.Background(), &orionv1.SubmitJobRequest{Type: "inline"})
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", code)
	}
}

func TestSubmitJob_MissingType(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	_, err := client.SubmitJob(context.Background(), &orionv1.SubmitJobRequest{Name: "job"})
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", code)
	}
}

func TestSubmitJob_StoreError(t *testing.T) {
	fs := &fakeStore{
		createJobFn: func(_ context.Context, _ *domain.Job) (*domain.Job, error) {
			return nil, errors.New("db down")
		},
	}
	client, _ := startTestServer(t, fs)
	_, err := client.SubmitJob(context.Background(), &orionv1.SubmitJobRequest{Name: "j", Type: "inline"})
	if code := status.Code(err); code != codes.Internal {
		t.Fatalf("expected Internal, got %v", code)
	}
}

func TestSubmitJob_IdempotencyKey_ReturnsExisting(t *testing.T) {
	existing := makeJob(domain.JobStatusRunning)
	fs := &fakeStore{
		getJobByIdempotencyFn: func(_ context.Context, _ string) (*domain.Job, error) {
			return existing, nil
		},
	}
	client, _ := startTestServer(t, fs)
	resp, err := client.SubmitJob(context.Background(), &orionv1.SubmitJobRequest{
		Name: "j", Type: "inline", IdempotencyKey: "key-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.JobId != existing.ID.String() {
		t.Fatalf("expected existing job ID %s, got %s", existing.ID, resp.JobId)
	}
}

// ─── GetJob tests ─────────────────────────────────────────────────────────────

func TestGetJob_Found(t *testing.T) {
	job := makeJob(domain.JobStatusRunning)
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) { return job, nil },
	}
	client, _ := startTestServer(t, fs)
	resp, err := client.GetJob(context.Background(), &orionv1.GetJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "running" {
		t.Fatalf("expected running, got %s", resp.Status)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	_, err := client.GetJob(context.Background(), &orionv1.GetJobRequest{JobId: uuid.New().String()})
	if code := status.Code(err); code != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", code)
	}
}

func TestGetJob_InvalidUUID(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	_, err := client.GetJob(context.Background(), &orionv1.GetJobRequest{JobId: "not-a-uuid"})
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", code)
	}
}

// ─── WatchJob tests ───────────────────────────────────────────────────────────

func TestWatchJob_ImmediateTerminal(t *testing.T) {
	job := makeJob(domain.JobStatusCompleted)
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) { return job, nil },
	}
	client, _ := startTestServer(t, fs)
	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should receive exactly one event (current state) then EOF
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected first event, got error: %v", err)
	}
	if event.NewStatus != "completed" {
		t.Fatalf("expected completed, got %s", event.NewStatus)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after terminal, got %v", err)
	}
}

func TestWatchJob_JobNotFound(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: uuid.New().String()})
	if err != nil {
		t.Fatalf("unexpected dial error: %v", err)
	}
	_, err = stream.Recv()
	if code := status.Code(err); code != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", code)
	}
}

func TestWatchJob_InvalidUUID(t *testing.T) {
	client, _ := startTestServer(t, &fakeStore{})
	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: "bad"})
	if err != nil {
		t.Fatalf("unexpected dial error: %v", err)
	}
	_, err = stream.Recv()
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", code)
	}
}

func TestWatchJob_ClientDisconnect(t *testing.T) {
	job := makeJob(domain.JobStatusRunning)
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) { return job, nil },
	}
	client, _ := startTestServer(t, fs)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.WatchJob(ctx, &orionv1.WatchJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Receive the initial event
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("expected initial event: %v", err)
	}
	// Cancel the context — simulates client disconnect
	cancel()
	// Stream should close cleanly (Canceled or EOF)
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error after cancel, got nil")
	}
}

func TestWatchJob_BroadcasterEvent(t *testing.T) {
	job := makeJob(domain.JobStatusRunning)
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) { return job, nil },
	}
	client, broadcaster := startTestServer(t, fs)

	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Consume initial event
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("expected initial event: %v", err)
	}

	// Give the server goroutine time to subscribe
	time.Sleep(50 * time.Millisecond)

	// Publish a completed event via broadcaster
	broadcaster.Publish(job.ID.String(), &orionv1.JobEvent{
		JobId:     job.ID.String(),
		NewStatus: "completed",
	})

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected broadcaster event: %v", err)
	}
	if event.NewStatus != "completed" {
		t.Fatalf("expected completed, got %s", event.NewStatus)
	}
	// Stream should close after terminal event
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF after terminal, got %v", err)
	}
}

func TestWatchJob_PollFallback(t *testing.T) {
	job := makeJob(domain.JobStatusRunning)
	callCount := 0
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) {
			callCount++
			if callCount >= 2 {
				// Return completed on second poll
				j := makeJob(domain.JobStatusCompleted)
				j.ID = job.ID
				return j, nil
			}
			return job, nil
		},
	}
	client, _ := startTestServer(t, fs)

	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Initial event
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("expected initial event: %v", err)
	}
	// Poll fallback should detect the status change within ~1s
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected poll event: %v", err)
	}
	if event.NewStatus != "completed" {
		t.Fatalf("expected completed, got %s", event.NewStatus)
	}
}

func TestWatchJob_TerminalEventClosesStream(t *testing.T) {
	job := makeJob(domain.JobStatusRunning)
	fs := &fakeStore{
		getJobFn: func(_ context.Context, _ uuid.UUID) (*domain.Job, error) { return job, nil },
	}
	client, broadcaster := startTestServer(t, fs)

	stream, err := client.WatchJob(context.Background(), &orionv1.WatchJobRequest{JobId: job.ID.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("expected initial event: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	broadcaster.Publish(job.ID.String(), &orionv1.JobEvent{JobId: job.ID.String(), NewStatus: "dead"})

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected dead event: %v", err)
	}
	if event.NewStatus != "dead" {
		t.Fatalf("expected dead, got %s", event.NewStatus)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// ─── Broadcaster unit tests ───────────────────────────────────────────────────

func TestBroadcaster_Publish_NoSubscribers(t *testing.T) {
	b := grpcserver.NewBroadcaster()
	// Must not panic
	b.Publish("no-such-id", &orionv1.JobEvent{JobId: "no-such-id", NewStatus: "completed"})
}

func TestBroadcaster_MultipleSubscribers(t *testing.T) {
	b := grpcserver.NewBroadcaster()
	jobID := uuid.New().String()

	ch1, unsub1 := b.Subscribe(jobID)
	ch2, unsub2 := b.Subscribe(jobID)
	defer unsub1()
	defer unsub2()

	event := &orionv1.JobEvent{JobId: jobID, NewStatus: "completed"}
	b.Publish(jobID, event)

	for _, ch := range []<-chan *orionv1.JobEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.NewStatus != "completed" {
				t.Fatalf("expected completed, got %s", got.NewStatus)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout waiting for event")
		}
	}
}

func TestBroadcaster_UnsubscribeCleanup(t *testing.T) {
	b := grpcserver.NewBroadcaster()
	jobID := uuid.New().String()

	_, unsub := b.Subscribe(jobID)
	if b.SubscriberCount(jobID) != 1 {
		t.Fatalf("expected 1 subscriber, got %d", b.SubscriberCount(jobID))
	}
	unsub()
	if b.SubscriberCount(jobID) != 0 {
		t.Fatalf("expected 0 subscribers after unsub, got %d", b.SubscriberCount(jobID))
	}
}
