package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/shreeharshshinde/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// HandlerFunc
// ─────────────────────────────────────────────────────────────────────────────

// HandlerFunc is the function signature every inline job handler must implement.
//
// Receive:
//   - ctx  : may be cancelled on job deadline, worker shutdown, or SIGTERM.
//     Always honour ctx.Done() in long-running loops — see Slow handler
//     in handlers/handlers.go for the correct select pattern.
//   - job  : full job record. Read job.Payload.Args for caller-supplied
//     parameters. Read job.Attempt to know which retry this is.
//
// Return:
//   - nil   → pool marks job completed, ACKs Redis message (removes from PEL)
//   - error → pool marks job failed, sets next_retry_at, NACKs Redis message
//
// Must NOT:
//   - panic without recover (crashes the worker goroutine)
//   - modify job.Status, job.Attempt, or any DB state (pool owns state)
//   - ignore ctx.Done() during long work (blocks graceful shutdown)
type HandlerFunc func(ctx context.Context, job *domain.Job) error

// ─────────────────────────────────────────────────────────────────────────────
// Registry
// ─────────────────────────────────────────────────────────────────────────────

// Registry is a thread-safe map from handler name → HandlerFunc.
//
// Register all handlers before pool.Start() is called. The pool is
// read-heavy at runtime (every job looks up its handler), so we use
// sync.RWMutex to allow unlimited concurrent readers with no lock
// contention in the normal case.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

// NewRegistry creates an empty Registry ready for handler registration.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]HandlerFunc),
	}
}

// Register adds a handler under the given name.
//
// The name must exactly match job.Payload.HandlerName in submitted jobs
// (case-sensitive). Registering the same name twice silently overwrites
// the previous handler — useful for testing overrides.
//
// Panics if name is empty or fn is nil; these are startup-time programming
// errors that should be caught immediately, not silently swallowed.
func (r *Registry) Register(name string, fn HandlerFunc) {
	if name == "" {
		panic("worker.Registry.Register: handler name must not be empty")
	}
	if fn == nil {
		panic("worker.Registry.Register: handler function must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[name] = fn
}

// Get looks up a handler by exact name.
// Returns (handler, true) if found, (nil, false) otherwise.
// Safe to call concurrently from multiple goroutines.
func (r *Registry) Get(name string) (HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.handlers[name]
	return fn, ok
}

// List returns all registered handler names in sorted order.
// Useful for health-check endpoints and startup logging.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for logs
	return names
}

// Len returns the number of registered handlers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.handlers)
}

// ─────────────────────────────────────────────────────────────────────────────
// InlineExecutor
// ─────────────────────────────────────────────────────────────────────────────

// InlineExecutor implements the worker.Executor interface for jobs with
// type = "inline". It resolves job.Payload.HandlerName against the Registry
// and calls the registered HandlerFunc.
//
// InlineExecutor has no knowledge of PostgreSQL, Redis, or the state machine.
// It only knows: "find the handler, call it, return the result."
// The pool (pool.go) owns all state transitions and audit records.
type InlineExecutor struct {
	registry *Registry
	logger   *slog.Logger
}

// NewInlineExecutor creates an InlineExecutor backed by the given registry.
// The executor holds a pointer to the registry, so handlers registered after
// this call are visible at execution time.
//
// Panics if registry is nil — this is always a startup-time programming error.
func NewInlineExecutor(registry *Registry, logger *slog.Logger) *InlineExecutor {
	if registry == nil {
		panic("worker.NewInlineExecutor: registry must not be nil")
	}
	return &InlineExecutor{
		registry: registry,
		logger:   logger,
	}
}

// CanExecute returns true only for inline job types.
// The pool's resolveExecutor loops through all registered executors and
// picks the first one that returns true here.
func (e *InlineExecutor) CanExecute(jobType domain.JobType) bool {
	return jobType == domain.JobTypeInline
}

// Execute resolves and calls the handler registered under job.Payload.HandlerName.
//
// Error cases and their meanings:
//
//	empty handler_name  → programming error; job will retry then die
//	name not registered → config error; job will retry (useful if handler
//	                      is registered before next retry starts)
//	handler returns err → transient or permanent failure; pool retries
//	ctx cancelled       → deadline/shutdown; handler should have returned
//	                      ctx.Err() which the pool treats as a retriable failure
func (e *InlineExecutor) Execute(ctx context.Context, job *domain.Job) error {
	handlerName := job.Payload.HandlerName
	if handlerName == "" {
		return fmt.Errorf("inline job %s: payload.handler_name is empty", job.ID)
	}

	fn, ok := e.registry.Get(handlerName)
	if !ok {
		return fmt.Errorf(
			"handler %q is not registered on this worker (registered: %v)",
			handlerName, e.registry.List(),
		)
	}

	e.logger.Debug("calling inline handler",
		"job_id", job.ID,
		"job_name", job.Name,
		"handler", handlerName,
		"attempt", job.Attempt,
		"arg_keys", argKeys(job.Payload.Args),
	)

	start := time.Now()
	err := fn(ctx, job)
	elapsed := time.Since(start)

	if err != nil {
		e.logger.Warn("inline handler returned error",
			"job_id", job.ID,
			"handler", handlerName,
			"elapsed_ms", elapsed.Milliseconds(),
			"err", err,
		)
		return err
	}

	e.logger.Debug("inline handler succeeded",
		"job_id", job.ID,
		"handler", handlerName,
		"elapsed_ms", elapsed.Milliseconds(),
	)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// argKeys returns the keys of the args map for structured logging.
// We log keys but not values — values may contain credentials or PII.
func argKeys(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic for logs
	return keys
}
