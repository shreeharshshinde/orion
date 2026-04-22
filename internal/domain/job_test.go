package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// State machine — ValidTransitions
// ─────────────────────────────────────────────────────────────────────────────

func TestCanTransitionTo_ValidTransitions(t *testing.T) {
	cases := []struct {
		from domain.JobStatus
		to   domain.JobStatus
		want bool
	}{
		// queued is the starting state
		{domain.JobStatusQueued, domain.JobStatusScheduled, true},
		{domain.JobStatusQueued, domain.JobStatusCancelled, true},
		{domain.JobStatusQueued, domain.JobStatusRunning, false}, // must pass through scheduled
		{domain.JobStatusQueued, domain.JobStatusCompleted, false},
		{domain.JobStatusQueued, domain.JobStatusFailed, false},

		// scheduled → running or back to queued (reclaim) or cancelled
		{domain.JobStatusScheduled, domain.JobStatusRunning, true},
		{domain.JobStatusScheduled, domain.JobStatusQueued, true},
		{domain.JobStatusScheduled, domain.JobStatusCancelled, true},
		{domain.JobStatusScheduled, domain.JobStatusCompleted, false},
		{domain.JobStatusScheduled, domain.JobStatusFailed, false},

		// running → only terminal or failure
		{domain.JobStatusRunning, domain.JobStatusCompleted, true},
		{domain.JobStatusRunning, domain.JobStatusFailed, true},
		{domain.JobStatusRunning, domain.JobStatusQueued, false}, // no going back
		{domain.JobStatusRunning, domain.JobStatusScheduled, false},
		{domain.JobStatusRunning, domain.JobStatusRetrying, false},

		// failed → retry or dead
		{domain.JobStatusFailed, domain.JobStatusRetrying, true},
		{domain.JobStatusFailed, domain.JobStatusDead, true},
		{domain.JobStatusFailed, domain.JobStatusQueued, false},
		{domain.JobStatusFailed, domain.JobStatusRunning, false},

		// retrying → back to queued
		{domain.JobStatusRetrying, domain.JobStatusQueued, true},
		{domain.JobStatusRetrying, domain.JobStatusRunning, false},
		{domain.JobStatusRetrying, domain.JobStatusFailed, false},

		// terminal states — no outbound transitions
		{domain.JobStatusCompleted, domain.JobStatusQueued, false},
		{domain.JobStatusCompleted, domain.JobStatusFailed, false},
		{domain.JobStatusDead, domain.JobStatusQueued, false},
		{domain.JobStatusDead, domain.JobStatusRetrying, false},
		{domain.JobStatusCancelled, domain.JobStatusQueued, false},
		{domain.JobStatusCancelled, domain.JobStatusRunning, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.from)+"→"+string(tc.to), func(t *testing.T) {
			job := &domain.Job{Status: tc.from}
			got := job.CanTransitionTo(tc.to)
			if got != tc.want {
				t.Errorf("CanTransitionTo(%s→%s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsTerminal
// ─────────────────────────────────────────────────────────────────────────────

func TestIsTerminal(t *testing.T) {
	cases := []struct {
		status domain.JobStatus
		want   bool
	}{
		{domain.JobStatusQueued, false},
		{domain.JobStatusScheduled, false},
		{domain.JobStatusRunning, false},
		{domain.JobStatusFailed, false},
		{domain.JobStatusRetrying, false},
		{domain.JobStatusCompleted, true}, // terminal
		{domain.JobStatusDead, true},      // terminal
		{domain.JobStatusCancelled, true}, // terminal
	}

	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			job := &domain.Job{Status: tc.status}
			if got := job.IsTerminal(); got != tc.want {
				t.Errorf("IsTerminal(%s) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsRetryable
// ─────────────────────────────────────────────────────────────────────────────

func TestIsRetryable_WhenFailedAndUnderLimit(t *testing.T) {
	job := &domain.Job{
		Status:     domain.JobStatusFailed,
		Attempt:    1,
		MaxRetries: 3,
	}
	if !job.IsRetryable() {
		t.Error("job with attempt=1, max_retries=3 should be retryable")
	}
}

func TestIsRetryable_WhenFailedAndAtLimit(t *testing.T) {
	// attempt == max_retries means we've used all retries
	job := &domain.Job{
		Status:     domain.JobStatusFailed,
		Attempt:    3,
		MaxRetries: 3,
	}
	if job.IsRetryable() {
		t.Error("job with attempt=3, max_retries=3 should NOT be retryable")
	}
}

func TestIsRetryable_WhenFailedAndOverLimit(t *testing.T) {
	job := &domain.Job{
		Status:     domain.JobStatusFailed,
		Attempt:    5,
		MaxRetries: 3,
	}
	if job.IsRetryable() {
		t.Error("job with attempt=5, max_retries=3 should NOT be retryable")
	}
}

func TestIsRetryable_WhenNotFailed(t *testing.T) {
	// Only failed jobs are retryable
	for _, status := range []domain.JobStatus{
		domain.JobStatusQueued,
		domain.JobStatusScheduled,
		domain.JobStatusRunning,
		domain.JobStatusCompleted,
		domain.JobStatusRetrying,
		domain.JobStatusDead,
		domain.JobStatusCancelled,
	} {
		job := &domain.Job{Status: status, Attempt: 0, MaxRetries: 5}
		if job.IsRetryable() {
			t.Errorf("status=%s should not be retryable (only 'failed' is)", status)
		}
	}
}

func TestIsRetryable_ZeroMaxRetries(t *testing.T) {
	job := &domain.Job{
		Status:     domain.JobStatusFailed,
		Attempt:    0,
		MaxRetries: 0, // no retries allowed
	}
	if job.IsRetryable() {
		t.Error("max_retries=0 means no retries — should not be retryable even on first failure")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Priority constants
// ─────────────────────────────────────────────────────────────────────────────

func TestPriorityOrdering(t *testing.T) {
	if domain.PriorityLow >= domain.PriorityNormal {
		t.Error("PriorityLow must be less than PriorityNormal")
	}
	if domain.PriorityNormal >= domain.PriorityHigh {
		t.Error("PriorityNormal must be less than PriorityHigh")
	}
	if domain.PriorityHigh >= domain.PriorityCritical {
		t.Error("PriorityHigh must be less than PriorityCritical")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker domain
// ─────────────────────────────────────────────────────────────────────────────

func TestWorker_IsAlive_RecentHeartbeat(t *testing.T) {
	w := &domain.Worker{
		LastHeartbeat: time.Now().Add(-10 * time.Second),
	}
	if !w.IsAlive(30 * time.Second) {
		t.Error("worker with heartbeat 10s ago should be alive with 30s TTL")
	}
}

func TestWorker_IsAlive_StaleHeartbeat(t *testing.T) {
	w := &domain.Worker{
		LastHeartbeat: time.Now().Add(-60 * time.Second),
	}
	if w.IsAlive(30 * time.Second) {
		t.Error("worker with heartbeat 60s ago should NOT be alive with 30s TTL")
	}
}

func TestWorker_AvailableSlots_Normal(t *testing.T) {
	w := &domain.Worker{Concurrency: 10, ActiveJobs: 3}
	if got := w.AvailableSlots(); got != 7 {
		t.Errorf("AvailableSlots() = %d, want 7", got)
	}
}

func TestWorker_AvailableSlots_Full(t *testing.T) {
	w := &domain.Worker{Concurrency: 10, ActiveJobs: 10}
	if got := w.AvailableSlots(); got != 0 {
		t.Errorf("AvailableSlots() = %d, want 0 when full", got)
	}
}

func TestWorker_AvailableSlots_NeverNegative(t *testing.T) {
	w := &domain.Worker{Concurrency: 5, ActiveJobs: 8} // over-committed
	if got := w.AvailableSlots(); got != 0 {
		t.Errorf("AvailableSlots() = %d, want 0 (never negative)", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline DAG — ReadyNodes
// ─────────────────────────────────────────────────────────────────────────────

func TestReadyNodes_NoEdges_AllReady(t *testing.T) {
	dag := &domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
		},
		Edges: nil, // no dependencies
	}
	ready := dag.ReadyNodes(map[string]bool{})
	if len(ready) != 3 {
		t.Errorf("with no edges, all 3 nodes should be ready, got %d", len(ready))
	}
}

func TestReadyNodes_LinearChain_OnlyFirstReady(t *testing.T) {
	// a → b → c: only "a" has no dependencies
	dag := &domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.DAGEdge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "c"},
		},
	}
	ready := dag.ReadyNodes(map[string]bool{})
	if len(ready) != 1 || ready[0] != "a" {
		t.Errorf("only 'a' should be ready initially, got %v", ready)
	}
}

func TestReadyNodes_LinearChain_AfterFirstCompletes(t *testing.T) {
	dag := &domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []domain.DAGEdge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "c"},
		},
	}
	ready := dag.ReadyNodes(map[string]bool{"a": true})
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("after 'a' completes, 'b' should be ready, got %v", ready)
	}
}

func TestReadyNodes_AllCompleted_NoneReady(t *testing.T) {
	dag := &domain.DAGSpec{
		Nodes: []domain.DAGNode{{ID: "a"}, {ID: "b"}},
		Edges: []domain.DAGEdge{{Source: "a", Target: "b"}},
	}
	ready := dag.ReadyNodes(map[string]bool{"a": true, "b": true})
	if len(ready) != 0 {
		t.Errorf("with all completed, nothing should be ready, got %v", ready)
	}
}

func TestReadyNodes_DiamondDAG(t *testing.T) {
	// preprocess → train, preprocess → validate
	// train + validate → deploy
	dag := &domain.DAGSpec{
		Nodes: []domain.DAGNode{
			{ID: "preprocess"},
			{ID: "train"},
			{ID: "validate"},
			{ID: "deploy"},
		},
		Edges: []domain.DAGEdge{
			{Source: "preprocess", Target: "train"},
			{Source: "preprocess", Target: "validate"},
			{Source: "train", Target: "deploy"},
			{Source: "validate", Target: "deploy"},
		},
	}

	// Initially only preprocess is ready
	ready := dag.ReadyNodes(map[string]bool{})
	if len(ready) != 1 || ready[0] != "preprocess" {
		t.Errorf("initially only preprocess ready, got %v", ready)
	}

	// After preprocess: train AND validate both ready
	ready = dag.ReadyNodes(map[string]bool{"preprocess": true})
	if len(ready) != 2 {
		t.Errorf("after preprocess, train+validate should be ready, got %v", ready)
	}

	// After preprocess+train (but not validate): deploy still not ready
	ready = dag.ReadyNodes(map[string]bool{"preprocess": true, "train": true})
	if len(ready) != 1 || ready[0] != "validate" {
		t.Errorf("with preprocess+train done, only validate should be ready, got %v", ready)
	}

	// After all prerequisites: deploy is ready
	ready = dag.ReadyNodes(map[string]bool{"preprocess": true, "train": true, "validate": true})
	if len(ready) != 1 || ready[0] != "deploy" {
		t.Errorf("with preprocess+train+validate done, deploy should be ready, got %v", ready)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Job struct field defaults and zero-values
// ─────────────────────────────────────────────────────────────────────────────

func TestJob_ZeroValueAttempt(t *testing.T) {
	job := &domain.Job{}
	if job.Attempt != 0 {
		t.Errorf("zero-value Attempt should be 0, got %d", job.Attempt)
	}
}

func TestJob_UUIDNilByDefault(t *testing.T) {
	job := &domain.Job{}
	if job.ID != uuid.Nil {
		t.Error("zero-value Job.ID should be uuid.Nil")
	}
}

func TestJob_TypeConstants(t *testing.T) {
	if domain.JobTypeInline != "inline" {
		t.Errorf("JobTypeInline = %q, want \"inline\"", domain.JobTypeInline)
	}
	if domain.JobTypeKubernetes != "k8s_job" {
		t.Errorf("JobTypeKubernetes = %q, want \"k8s_job\"", domain.JobTypeKubernetes)
	}
}
