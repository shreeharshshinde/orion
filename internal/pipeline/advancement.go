// Package pipeline implements DAG advancement for Orion pipelines.
//
// The Advancer is the engine of Phase 5. It is called by the scheduler
// on every tick and moves every active pipeline forward by exactly one wave:
//
//  1. Find all pipelines in pending or running status
//  2. For each: load node statuses, detect what's ready, create jobs
//  3. Detect completion (all nodes done) or failure (any node dead)
//  4. On failure: cascade-cancel all downstream nodes that haven't started
//
// The Advancer is stateless — all state lives in PostgreSQL.
// It holds only its dependencies: a store.Store and a logger.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/google/uuid"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/observability"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// Advancer
// ─────────────────────────────────────────────────────────────────────────────

// Advancer advances all active pipelines on every scheduler tick.
// Phase 6 adds: Prometheus metrics + OTel spans to every advancement step.
type Advancer struct {
	store   store.Store
	metrics *observability.Metrics // nil-safe: all metric calls are guarded
	logger  *slog.Logger
}

// NewAdvancer creates an Advancer. m may be nil in tests.
func NewAdvancer(s store.Store, m *observability.Metrics, logger *slog.Logger) *Advancer {
	return &Advancer{store: s, metrics: m, logger: logger}
}

// AdvanceAll advances every pipeline in pending or running status.
// Called by the scheduler on every schedule tick (default: every 2 seconds).
//
// Processing strategy:
//   - Pending pipelines: kick off root nodes, transition to running
//   - Running pipelines: check completed set, create next-wave jobs
//   - Completed pipelines: already terminal, skipped by the query
//
// If one pipeline fails to advance, the error is logged and we continue
// to the next pipeline. One broken pipeline must never block all others.
func (a *Advancer) AdvanceAll(ctx context.Context) error {
	// Span: covers the entire AdvanceAll call including all pipeline fetches
	ctx, span := observability.Tracer("orion.pipeline").Start(ctx, "advancer.advance_all")
	defer span.End()

	// Histogram: measure how long one full advancement cycle takes
	cycleStart := time.Now()
	defer func() {
		if a.metrics != nil {
			a.metrics.AdvancerCycleDuration.Observe(time.Since(cycleStart).Seconds())
		}
	}()

	// Collect both statuses in two queries rather than one OR query.
	// OR queries can't use partial indexes efficiently; two queries each
	// hitting a partial index are faster.
	pending, err := a.store.ListPipelinesByStatus(ctx, domain.PipelineStatusPending, 50)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("listing pending pipelines: %w", err)
	}

	running, err := a.store.ListPipelinesByStatus(ctx, domain.PipelineStatusRunning, 50)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("listing running pipelines: %w", err)
	}

	toAdvance := make([]*domain.Pipeline, 0, len(pending)+len(running))
	toAdvance = append(toAdvance, pending...)
	toAdvance = append(toAdvance, running...)

	span.SetAttributes(attribute.Int("pipelines.to_advance", len(toAdvance)))

	for _, p := range toAdvance {
		if advErr := a.advanceOne(ctx, p); advErr != nil {
			// Log per-pipeline errors at Error level but continue.
			// A bad dag_spec or transient DB error on one pipeline
			// must not block advancement of healthy pipelines.
			a.logger.Error("failed to advance pipeline",
				"pipeline_id", p.ID,
				"pipeline_name", p.Name,
				"status", p.Status,
				"err", advErr,
			)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// advanceOne — the core algorithm
// ─────────────────────────────────────────────────────────────────────────────

// advanceOne advances a single pipeline by one logical step.
//
// Step sequence:
//  1. Load all pipeline_jobs (node_id → job_id → job_status) in one query
//  2. Build completed set and createdNodes map
//  3. Detect dead nodes → cascade cancel downstream → pipeline failed
//  4. Transition pending → running on first ever advance
//  5. Call ReadyNodes(completed) → get unlocked nodes
//  6. For each ready node not yet created: CreateJob + AddPipelineJob
//  7. If all nodes completed → pipeline completed
//
// Idempotency is guaranteed by step 6's guard: if the scheduler runs this
// function twice in rapid succession, the second call finds all ready nodes
// already in createdNodes and skips CreateJob for all of them.
//
// Phase 6: emits pipeline metrics at every state transition point.
func (a *Advancer) advanceOne(ctx context.Context, p *domain.Pipeline) error {
	// Child span per pipeline for per-pipeline latency tracking in Jaeger
	_, span := observability.Tracer("orion.pipeline").Start(ctx, "advancer.advance_pipeline") // Note: we do NOT use ctx from span here to avoid span chaining issues with the
	// store calls below — they create their own DB spans via startDBSpan.

	span.SetAttributes(
		attribute.String("pipeline.id", p.ID.String()),
		attribute.String("pipeline.name", p.Name),
		attribute.String("pipeline.status", string(p.Status)),
		attribute.Int("pipeline.nodes", len(p.DAGSpec.Nodes)),
	)
	defer span.End()

	a.logger.Debug("advancing pipeline",
		"pipeline_id", p.ID,
		"pipeline_name", p.Name,
		"status", p.Status,
		"nodes", len(p.DAGSpec.Nodes),
		"edges", len(p.DAGSpec.Edges),
	)

	// ── Step 1: Load all current node→job statuses in one JOIN query ─────────
	pipelineJobs, err := a.store.GetPipelineJobs(ctx, p.ID)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("loading pipeline jobs: %w", err)
	}

	// ── Step 2: Build lookup maps ─────────────────────────────────────────────
	// completed: node_id → true  (input to ReadyNodes)
	// createdNodes: node_id → *PipelineJobStatus  (idempotency guard)
	completed := make(map[string]bool, len(pipelineJobs))
	createdNodes := make(map[string]*store.PipelineJobStatus, len(pipelineJobs))

	for _, pj := range pipelineJobs {
		createdNodes[pj.NodeID] = pj
		if pj.JobStatus == domain.JobStatusCompleted {
			completed[pj.NodeID] = true
		}
	}

	// ── Step 3: Failure detection ─────────────────────────────────────────────
	// A node reaching "dead" status means it has exhausted all retries.
	// The pipeline cannot recover — cancel all downstream nodes and mark failed.
	for _, pj := range pipelineJobs {
		if pj.JobStatus == domain.JobStatusDead {
			a.logger.Warn("pipeline node reached dead state — failing pipeline",
				"pipeline_id", p.ID,
				"pipeline_name", p.Name,
				"failed_node", pj.NodeID,
				"job_id", pj.JobID,
			)
			span.SetAttributes(attribute.String("pipeline.failed_node", pj.NodeID))
			span.SetStatus(codes.Error, "node dead: "+pj.NodeID)

			a.logCascadeCancellation(p, pj.NodeID, createdNodes)

			// Metric: pipeline failure + duration
			if a.metrics != nil {
				a.metrics.PipelineFailed.WithLabelValues(p.Name).Inc()
				if !p.CreatedAt.IsZero() {
					a.metrics.PipelineDuration.WithLabelValues("failed").
						Observe(time.Since(p.CreatedAt).Seconds())
				}
				a.metrics.PipelineNodesTotal.WithLabelValues(p.Name, "dead").Inc()
			}

			return a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusFailed)
		}
	}

	// ── Step 4: pending → running on first advance ────────────────────────────
	// A pipeline is pending until the very first scheduler tick touches it.
	// Transition to running now so the API shows meaningful progress.
	if p.Status == domain.PipelineStatusPending {
		if err := a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusRunning); err != nil {
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("transitioning pipeline to running: %w", err)
		}
		p.Status = domain.PipelineStatusRunning

		// Metric: pipeline started
		if a.metrics != nil {
			a.metrics.PipelineStarted.WithLabelValues(p.Name).Inc()
		}

		a.logger.Info("pipeline started",
			"pipeline_id", p.ID,
			"pipeline_name", p.Name,
			"node_count", len(p.DAGSpec.Nodes),
		)
	}

	// ── Step 5: Find ready nodes ──────────────────────────────────────────────
	// ReadyNodes is a pure function on the DAG spec. It returns every node
	// whose ALL dependencies are in the completed set. On the first tick with
	// an empty completed set, it returns all root nodes (nodes with no incoming edges).
	readyNodeIDs := p.DAGSpec.ReadyNodes(completed)
	span.SetAttributes(attribute.Int("pipeline.ready_nodes", len(readyNodeIDs)))

	// ── Step 6: Create jobs for ready nodes ───────────────────────────────────
	createdThisTick := 0
	for _, nodeID := range readyNodeIDs {
		// Idempotency guard: skip nodes that already have a job.
		// This is the critical check that prevents duplicate job creation
		// when the scheduler runs AdvanceAll twice in rapid succession.
		if _, exists := createdNodes[nodeID]; exists {
			continue
		}

		// Locate the node template in the DAG spec.
		node := findNode(p.DAGSpec, nodeID)
		if node == nil {
			// This should never happen if validateDAG in the handler is correct.
			// Log at error so it's visible in monitoring.
			a.logger.Error("ready node not found in dag_spec — possible DAG corruption",
				"pipeline_id", p.ID,
				"node_id", nodeID,
			)
			continue
		}

		// Create the job from the node's template.
		// Jobs inside pipelines are named "pipeline-name/node-id" for easy
		// correlation when looking at GET /jobs?queue=default.
		jobType := jobTypeFromPayload(node.JobTemplate)
		queueName := queueNameFromPayload(node.JobTemplate)

		job := &domain.Job{
			Name:       fmt.Sprintf("%s/%s", p.Name, nodeID),
			Type:       jobType,
			QueueName:  queueName,
			Priority:   domain.PriorityNormal,
			Payload:    node.JobTemplate,
			MaxRetries: 3,
		}

		created, err := a.store.CreateJob(ctx, job)
		if err != nil {
			// Log and skip this node — the next tick will retry.
			// This is safe: if CreateJob fails, AddPipelineJob is never called,
			// so the node remains absent from createdNodes on the next tick.
			a.logger.Error("failed to create job for pipeline node — will retry next tick",
				"pipeline_id", p.ID,
				"node_id", nodeID,
				"job_type", jobType,
				"err", err,
			)
			continue
		}

		// Link the job to the pipeline node.
		// ON CONFLICT DO NOTHING means this is idempotent even if the
		// scheduler crashes after CreateJob but before AddPipelineJob.
		if err := a.store.AddPipelineJob(ctx, p.ID, nodeID, created.ID); err != nil {
			a.logger.Error("failed to link job to pipeline node",
				"pipeline_id", p.ID,
				"node_id", nodeID,
				"job_id", created.ID,
				"err", err,
			)
			continue
		}

		// Metric: node started
		if a.metrics != nil {
			a.metrics.PipelineNodesTotal.WithLabelValues(p.Name, "started").Inc()
		}

		createdThisTick++
		a.logger.Info("created job for pipeline node",
			"pipeline_id", p.ID,
			"pipeline_name", p.Name,
			"node_id", nodeID,
			"job_id", created.ID,
			"job_type", jobType,
			"queue", queueName,
		)
	}

	if createdThisTick > 0 {
		span.SetAttributes(attribute.Int("pipeline.jobs_created", createdThisTick))
		a.logger.Info("advanced pipeline wave",
			"pipeline_id", p.ID,
			"pipeline_name", p.Name,
			"jobs_created", createdThisTick,
		)
	}

	// ── Step 7: Completion detection ──────────────────────────────────────────
	// All nodes must appear in the completed set for the pipeline to complete.
	// We check against the DAG spec (ground truth), not the pipeline_jobs table
	// (which may have a lag if AddPipelineJob just ran in this tick).
	if allNodesCompleted(p.DAGSpec, completed) {
		a.logger.Info("pipeline completed — all nodes finished",
			"pipeline_id", p.ID,
			"pipeline_name", p.Name,
			"total_nodes", len(p.DAGSpec.Nodes),
		)

		// Metric: completion counter + duration
		if a.metrics != nil {
			a.metrics.PipelineCompleted.WithLabelValues(p.Name).Inc()
			if !p.CreatedAt.IsZero() {
				a.metrics.PipelineDuration.WithLabelValues("completed").
					Observe(time.Since(p.CreatedAt).Seconds())
			}
		}

		return a.store.UpdatePipelineStatus(ctx, p.ID, domain.PipelineStatusCompleted)
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Cascade cancellation
// ─────────────────────────────────────────────────────────────────────────────

// logCascadeCancellation logs which downstream nodes will not be started
// because an upstream node failed. In Phase 5 this is informational only —
// nodes that haven't been created simply never get created.
//
// Phase 6 enhancement: create explicit "cancelled" job records for every
// downstream node so they appear in GET /pipelines/{id}/jobs with
// status=cancelled for full audit trail.
func (a *Advancer) logCascadeCancellation(
	p *domain.Pipeline,
	failedNodeID string,
	createdNodes map[string]*store.PipelineJobStatus,
) {
	downstream := findDownstream(p.DAGSpec, failedNodeID)
	for _, nodeID := range downstream {
		if _, created := createdNodes[nodeID]; !created {
			a.logger.Info("downstream node will not start (upstream failed)",
				"pipeline_id", p.ID,
				"failed_node", failedNodeID,
				"cancelled_node", nodeID,
			)
			if a.metrics != nil {
				a.metrics.PipelineNodesTotal.WithLabelValues(p.Name, "cancelled").Inc()
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ─────────────────────────────────────────────────────────────────────────────
// Graph helpers
// ─────────────────────────────────────────────────────────────────────────────

// findNode returns a pointer to the DAGNode with the given ID, or nil if absent.
// Uses a pointer into the slice so the caller reads the original struct, not a copy.
func findNode(dag domain.DAGSpec, nodeID string) *domain.DAGNode {
	for i := range dag.Nodes {
		if dag.Nodes[i].ID == nodeID {
			return &dag.Nodes[i]
		}
	}
	return nil
}

// findDownstream returns all node IDs reachable from startNodeID following
// edges in the forward direction (source → target). Uses BFS.
//
// Example DAG: preprocess → train → evaluate → deploy
// findDownstream("train") → ["evaluate", "deploy"]
//
// This function is used for cascade cancellation: when a node dies, all
// nodes reachable from it will never be able to run (their dependency chain
// is broken).
func findDownstream(dag domain.DAGSpec, startNodeID string) []string {
	// Build adjacency list: source → []targets
	adj := make(map[string][]string, len(dag.Edges))
	for _, edge := range dag.Edges {
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
	}

	// BFS from startNodeID, collecting all reachable nodes.
	visited := make(map[string]bool)
	queue := []string{startNodeID}
	var downstream []string

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, next := range adj[current] {
			if !visited[next] {
				visited[next] = true
				downstream = append(downstream, next)
				queue = append(queue, next)
			}
		}
	}
	return downstream
}

// allNodesCompleted returns true if every node in the DAG appears in
// the completed set. An empty DAG with zero nodes never completes (guard
// against accidentally completing a pipeline with a malformed spec).
func allNodesCompleted(dag domain.DAGSpec, completed map[string]bool) bool {
	if len(dag.Nodes) == 0 {
		return false
	}
	for _, node := range dag.Nodes {
		if !completed[node.ID] {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Job creation helpers
// ─────────────────────────────────────────────────────────────────────────────

// jobTypeFromPayload infers domain.JobType from the node's job template.
// If kubernetes_spec is present → k8s_job. Otherwise → inline.
func jobTypeFromPayload(payload domain.JobPayload) domain.JobType {
	if payload.KubernetesSpec != nil {
		return domain.JobTypeKubernetes
	}
	return domain.JobTypeInline
}

// queueNameFromPayload returns the queue for a pipeline job.
// Pipeline jobs always use the default queue unless we add per-node
// queue configuration in a future phase.
func queueNameFromPayload(_ domain.JobPayload) string {
	return "default"
}

// ─────────────────────────────────────────────────────────────────────────────
// Exported helpers used by advancement_test.go
// ─────────────────────────────────────────────────────────────────────────────

// FindDownstream is the exported version for white-box testing.
func FindDownstream(dag domain.DAGSpec, startNodeID string) []string {
	return findDownstream(dag, startNodeID)
}

// AllNodesCompleted is the exported version for white-box testing.
func AllNodesCompleted(dag domain.DAGSpec, completed map[string]bool) bool {
	return allNodesCompleted(dag, completed)
}

// JobTypeFromPayload is exported for testing.
func JobTypeFromPayload(payload domain.JobPayload) domain.JobType {
	return jobTypeFromPayload(payload)
}

// ─────────────────────────────────────────────────────────────────────────────
// FakeStore helpers (for testing without embedding)
// ─────────────────────────────────────────────────────────────────────────────

// PipelineJobStatusSlice is a convenience type for building test data.
type PipelineJobStatusSlice = []*store.PipelineJobStatus

// MakePipelineJobStatus builds a PipelineJobStatus for test assertions.
func MakePipelineJobStatus(nodeID string, jobID uuid.UUID, status domain.JobStatus) *store.PipelineJobStatus {
	return &store.PipelineJobStatus{
		NodeID:    nodeID,
		JobID:     jobID,
		JobName:   "test-pipeline/" + nodeID,
		JobStatus: status,
	}
}
