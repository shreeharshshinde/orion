package worker_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/worker"
	"github.com/shreeharshshinde/orion/internal/worker/handlers"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

// makeInlineJob creates a minimal inline job for testing.
func makeInlineJob(handlerName string, args map[string]any) *domain.Job {
	return &domain.Job{
		ID:      uuid.New(),
		Name:    "test-job-" + handlerName,
		Type:    domain.JobTypeInline,
		Attempt: 0,
		Payload: domain.JobPayload{
			HandlerName: handlerName,
			Args:        args,
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry — basic operations
// ─────────────────────────────────────────────────────────────────────────────

func TestRegistry_NewRegistry_IsEmpty(t *testing.T) {
	r := worker.NewRegistry()
	if r.Len() != 0 {
		t.Errorf("new registry should be empty, got len=%d", r.Len())
	}
}

func TestRegistry_Register_And_Get(t *testing.T) {
	r := worker.NewRegistry()
	called := false

	r.Register("test_handler", func(ctx context.Context, job *domain.Job) error {
		called = true
		return nil
	})

	fn, ok := r.Get("test_handler")
	if !ok {
		t.Fatal("expected to find registered handler 'test_handler'")
	}
	_ = fn(context.Background(), makeInlineJob("test_handler", nil))
	if !called {
		t.Error("expected handler function to be called")
	}
}

func TestRegistry_Get_Unregistered(t *testing.T) {
	r := worker.NewRegistry()
	_, ok := r.Get("not_registered")
	if ok {
		t.Error("Get should return false for unregistered handler")
	}
}

func TestRegistry_Register_Overwrites(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("h", func(_ context.Context, _ *domain.Job) error {
		return errors.New("first version")
	})
	r.Register("h", func(_ context.Context, _ *domain.Job) error {
		return errors.New("second version")
	})

	fn, _ := r.Get("h")
	err := fn(context.Background(), makeInlineJob("h", nil))
	if err == nil || err.Error() != "second version" {
		t.Errorf("expected 'second version' error, got: %v", err)
	}
}

func TestRegistry_Len(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("a", handlers.Noop)
	r.Register("b", handlers.Noop)
	r.Register("c", handlers.Noop)

	if r.Len() != 3 {
		t.Errorf("expected Len()=3, got %d", r.Len())
	}
}

func TestRegistry_List_SortedAlphabetically(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("zebra", handlers.Noop)
	r.Register("apple", handlers.Noop)
	r.Register("mango", handlers.Noop)

	names := r.List()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	// Should be sorted: apple, mango, zebra
	if names[0] != "apple" || names[1] != "mango" || names[2] != "zebra" {
		t.Errorf("expected sorted list [apple mango zebra], got %v", names)
	}
}

func TestRegistry_Register_PanicsOnEmptyName(t *testing.T) {
	r := worker.NewRegistry()
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic when registering with empty name")
		}
	}()
	r.Register("", handlers.Noop)
}

func TestRegistry_Register_PanicsOnNilHandler(t *testing.T) {
	r := worker.NewRegistry()
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic when registering nil handler")
		}
	}()
	r.Register("valid_name", nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry — concurrent access (run with -race to verify no data races)
// ─────────────────────────────────────────────────────────────────────────────

func TestRegistry_ConcurrentReadWrite(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("base", handlers.Noop)

	var wg sync.WaitGroup

	// 100 concurrent readers
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Get("base")
			r.List()
			r.Len()
		}()
	}

	// 10 concurrent writers (occasional)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Register("base", handlers.Noop)
		}()
	}

	wg.Wait()
	// If the -race detector passes, the RWMutex is working correctly.
}

// ─────────────────────────────────────────────────────────────────────────────
// InlineExecutor — CanExecute routing
// ─────────────────────────────────────────────────────────────────────────────

func TestInlineExecutor_CanExecute_Inline(t *testing.T) {
	r := worker.NewRegistry()
	e := worker.NewInlineExecutor(r, testLogger())

	if !e.CanExecute(domain.JobTypeInline) {
		t.Error("InlineExecutor must return true for JobTypeInline")
	}
}

func TestInlineExecutor_CanExecute_Kubernetes(t *testing.T) {
	r := worker.NewRegistry()
	e := worker.NewInlineExecutor(r, testLogger())

	if e.CanExecute(domain.JobTypeKubernetes) {
		t.Error("InlineExecutor must return false for JobTypeKubernetes (handled by Phase 4)")
	}
}

func TestInlineExecutor_NewInlineExecutor_PanicsOnNilRegistry(t *testing.T) {
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic when registry is nil")
		}
	}()
	worker.NewInlineExecutor(nil, testLogger())
}

// ─────────────────────────────────────────────────────────────────────────────
// InlineExecutor — Execute paths
// ─────────────────────────────────────────────────────────────────────────────

func TestInlineExecutor_Execute_Success(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("noop", handlers.Noop)
	e := worker.NewInlineExecutor(r, testLogger())

	err := e.Execute(context.Background(), makeInlineJob("noop", nil))
	if err != nil {
		t.Errorf("noop handler should return nil, got: %v", err)
	}
}

func TestInlineExecutor_Execute_HandlerError(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("always_fail", handlers.AlwaysFail)
	e := worker.NewInlineExecutor(r, testLogger())

	err := e.Execute(context.Background(), makeInlineJob("always_fail", nil))
	if err == nil {
		t.Error("always_fail handler should return a non-nil error")
	}
}

func TestInlineExecutor_Execute_UnregisteredHandler_ReturnsDescriptiveError(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("noop", handlers.Noop) // registered so List() is non-empty
	e := worker.NewInlineExecutor(r, testLogger())

	err := e.Execute(context.Background(), makeInlineJob("not_registered", nil))
	if err == nil {
		t.Fatal("expected error for unregistered handler")
	}
	// Error message should contain the missing handler name for easy debugging.
	if !contains(err.Error(), "not_registered") {
		t.Errorf("error should mention the missing handler name, got: %v", err)
	}
}

func TestInlineExecutor_Execute_EmptyHandlerName_ReturnsError(t *testing.T) {
	r := worker.NewRegistry()
	e := worker.NewInlineExecutor(r, testLogger())

	err := e.Execute(context.Background(), makeInlineJob("", nil))
	if err == nil {
		t.Error("expected error for empty handler_name")
	}
}

func TestInlineExecutor_Execute_ArgsPassedThrough(t *testing.T) {
	r := worker.NewRegistry()

	var capturedArgs map[string]any
	r.Register("capture", func(_ context.Context, job *domain.Job) error {
		capturedArgs = job.Payload.Args
		return nil
	})
	e := worker.NewInlineExecutor(r, testLogger())

	args := map[string]any{
		"model":   "resnet50",
		"epochs":  10.0,
		"dataset": "imagenet",
	}
	_ = e.Execute(context.Background(), makeInlineJob("capture", args))

	if capturedArgs == nil {
		t.Fatal("handler did not receive args")
	}
	if capturedArgs["model"] != "resnet50" {
		t.Errorf("expected model=resnet50, got %v", capturedArgs["model"])
	}
	if capturedArgs["epochs"] != 10.0 {
		t.Errorf("expected epochs=10.0, got %v", capturedArgs["epochs"])
	}
}

func TestInlineExecutor_Execute_AttemptPassedThrough(t *testing.T) {
	r := worker.NewRegistry()

	var capturedAttempt int
	r.Register("capture_attempt", func(_ context.Context, job *domain.Job) error {
		capturedAttempt = job.Attempt
		return nil
	})
	e := worker.NewInlineExecutor(r, testLogger())

	job := makeInlineJob("capture_attempt", nil)
	job.Attempt = 2 // simulating 3rd retry
	_ = e.Execute(context.Background(), job)

	if capturedAttempt != 2 {
		t.Errorf("expected attempt=2, got %d", capturedAttempt)
	}
}

func TestInlineExecutor_Execute_ContextCancellationRespected(t *testing.T) {
	r := worker.NewRegistry()
	r.Register("slow_handler", func(ctx context.Context, _ *domain.Job) error {
		select {
		case <-time.After(60 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	e := worker.NewInlineExecutor(r, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := e.Execute(ctx, makeInlineJob("slow_handler", nil))
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected context cancellation error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("handler did not respect context (ran %v, expected <500ms)", elapsed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in handlers
// ─────────────────────────────────────────────────────────────────────────────

func TestHandlers_Noop_AlwaysReturnsNil(t *testing.T) {
	err := handlers.Noop(context.Background(), makeInlineJob("noop", nil))
	if err != nil {
		t.Errorf("Noop must return nil, got: %v", err)
	}
}

func TestHandlers_Echo_AlwaysReturnsNil(t *testing.T) {
	args := map[string]any{"key": "value", "num": 42.0}
	err := handlers.Echo(context.Background(), makeInlineJob("echo", args))
	if err != nil {
		t.Errorf("Echo must return nil, got: %v", err)
	}
}

func TestHandlers_AlwaysFail_ReturnsNonNilError(t *testing.T) {
	job := makeInlineJob("always_fail", nil)
	job.Attempt = 3

	err := handlers.AlwaysFail(context.Background(), job)
	if err == nil {
		t.Error("AlwaysFail must return a non-nil error")
	}
	// Error should include the attempt number for verification
	if !contains(err.Error(), "3") {
		t.Errorf("error should contain attempt number 3, got: %v", err)
	}
}

func TestHandlers_Slow_CompletesWithZeroDuration(t *testing.T) {
	job := makeInlineJob("slow", map[string]any{"duration_seconds": 0.0})
	err := handlers.Slow(context.Background(), job)
	if err != nil {
		t.Errorf("Slow with duration=0 should succeed, got: %v", err)
	}
}

func TestHandlers_Slow_RespectsContextCancellation(t *testing.T) {
	job := makeInlineJob("slow", map[string]any{"duration_seconds": 60.0})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := handlers.Slow(ctx, job)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Slow with 60s duration should return error when context is cancelled")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected context error, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Slow did not respect ctx cancellation, ran for %v", elapsed)
	}
}

func TestHandlers_Slow_UsesDefaultDuration(t *testing.T) {
	// When no duration_seconds arg is provided, default is 5s.
	// We verify this by cancelling immediately and checking the error.
	job := makeInlineJob("slow", nil) // no args

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := handlers.Slow(ctx, job)
	// Should return context error (cancelled), not complete normally
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestHandlers_PanicRecover_CatchesPanic(t *testing.T) {
	panicHandler := func(_ context.Context, _ *domain.Job) error {
		panic("simulated panic in handler")
	}
	safeHandler := handlers.PanicRecover(panicHandler)

	err := safeHandler(context.Background(), makeInlineJob("panic_test", nil))
	if err == nil {
		t.Error("PanicRecover should convert panic to error")
	}
	if !contains(err.Error(), "panicked") {
		t.Errorf("error should mention panic, got: %v", err)
	}
}

func TestHandlers_PanicRecover_PassesThroughNormalReturn(t *testing.T) {
	normalHandler := func(_ context.Context, _ *domain.Job) error {
		return nil
	}
	safeHandler := handlers.PanicRecover(normalHandler)

	err := safeHandler(context.Background(), makeInlineJob("normal", nil))
	if err != nil {
		t.Errorf("PanicRecover should pass through nil return, got: %v", err)
	}
}

func TestHandlers_PanicRecover_PassesThroughError(t *testing.T) {
	expectedErr := errors.New("normal error")
	errorHandler := func(_ context.Context, _ *domain.Job) error {
		return expectedErr
	}
	safeHandler := handlers.PanicRecover(errorHandler)

	err := safeHandler(context.Background(), makeInlineJob("error_test", nil))
	if !errors.Is(err, expectedErr) {
		t.Errorf("PanicRecover should pass through normal errors, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper
// ─────────────────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(substr) == 0 ||
			(len(s) > 0 && indexString(s, substr) >= 0))
}

func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
