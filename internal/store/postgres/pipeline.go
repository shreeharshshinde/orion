package postgres

// pipeline.go implements the store.PipelineStore interface for PostgreSQL.
//
// This file is deliberately separate from db.go to keep each file focused:
//   db.go       — 12 job/worker/execution methods  (~798 lines)
//   pipeline.go — 7  pipeline methods              (this file)
//
// Both files are in the same `postgres` package and share the DB struct.
// Go compiles them together — splitting is purely for readability.
//
// SQL patterns used here:
//   - JSONB for dag_spec: marshal/unmarshal with encoding/json
//   - ON CONFLICT DO NOTHING for AddPipelineJob idempotency
//   - Single JOIN query for GetPipelineJobs (avoids N+1)
//   - Partial index on (status, created_at) for ListPipelinesByStatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// CreatePipeline
// ─────────────────────────────────────────────────────────────────────────────

// CreatePipeline inserts a new pipeline in pending status.
//
// The dag_spec is stored as JSONB. This means the spec shape can evolve
// (add new node fields, edge metadata) without schema migrations.
// UUID is generated here if not set, matching CreateJob's pattern.
func (db *DB) CreatePipeline(ctx context.Context, p *domain.Pipeline) (*domain.Pipeline, error) {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	if p.Status == "" {
		p.Status = domain.PipelineStatusPending
	}

	dagJSON, err := json.Marshal(p.DAGSpec)
	if err != nil {
		return nil, fmt.Errorf("marshaling dag_spec for pipeline %q: %w", p.Name, err)
	}

	const q = `
		INSERT INTO pipelines (id, name, status, dag_spec, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := db.pool.Exec(ctx, q,
		p.ID, p.Name, string(p.Status), dagJSON, p.CreatedAt, p.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("inserting pipeline %q: %w", p.Name, err)
	}
	return p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPipeline
// ─────────────────────────────────────────────────────────────────────────────

// GetPipeline retrieves a single pipeline by ID.
// Returns store.ErrNotFound if the pipeline does not exist.
func (db *DB) GetPipeline(ctx context.Context, id uuid.UUID) (*domain.Pipeline, error) {
	const q = `
		SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
		FROM pipelines
		WHERE id = $1`

	row := db.pool.QueryRow(ctx, q, id)
	p, err := scanPipeline(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("getting pipeline %s: %w", id, err)
	}
	return p, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPipelines
// ─────────────────────────────────────────────────────────────────────────────

// ListPipelines returns pipelines matching the filter, newest first.
// Used by GET /pipelines with optional ?status= query parameter.
func (db *DB) ListPipelines(ctx context.Context, filter store.PipelineFilter) ([]*domain.Pipeline, error) {
	var whereParts []string
	var args []any
	argIdx := 1

	if filter.Status != nil {
		whereParts = append(whereParts, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(*filter.Status))
		argIdx++
	}

	where := "TRUE"
	if len(whereParts) > 0 {
		where = strings.Join(whereParts, " AND ")
	}

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	// $N for limit and offset — safe because they are integers, not strings.
	args = append(args, limit, offset)
	q := fmt.Sprintf(`
		SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
		FROM pipelines
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)

	rows, err := db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing pipelines: %w", err)
	}
	defer rows.Close()

	var result []*domain.Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning pipeline row: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPipelinesByStatus
// ─────────────────────────────────────────────────────────────────────────────

// ListPipelinesByStatus is the scheduler's primary read for pipeline advancement.
// Returns pipelines in a given status ordered by created_at ASC (oldest first,
// so that a long-running pipeline doesn't starve newer ones forever).
//
// The partial index idx_pipelines_status_created (from migration 002) makes
// this query constant-time even as the pipeline table grows to millions of rows,
// because it only indexes 'pending' and 'running' rows — the tiny active set.
func (db *DB) ListPipelinesByStatus(ctx context.Context, status domain.PipelineStatus, limit int) ([]*domain.Pipeline, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT id, name, status, dag_spec, created_at, updated_at, completed_at
		FROM pipelines
		WHERE status = $1
		ORDER BY created_at ASC
		LIMIT $2`

	rows, err := db.pool.Query(ctx, q, string(status), limit)
	if err != nil {
		return nil, fmt.Errorf("listing pipelines by status %q: %w", status, err)
	}
	defer rows.Close()

	var result []*domain.Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning pipeline: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdatePipelineStatus
// ─────────────────────────────────────────────────────────────────────────────

// UpdatePipelineStatus transitions a pipeline to a new status.
//
// Sets completed_at for terminal states so the API can report "how long did
// this pipeline take?" via completed_at - created_at.
// The updated_at trigger fires automatically (defined in migration 001).
func (db *DB) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status domain.PipelineStatus) error {
	isTerminal := status == domain.PipelineStatusCompleted ||
		status == domain.PipelineStatusFailed ||
		status == domain.PipelineStatusCancelled

	var q string
	var args []any

	if isTerminal {
		q = `UPDATE pipelines SET status = $1, completed_at = NOW() WHERE id = $2`
		args = []any{string(status), id}
	} else {
		q = `UPDATE pipelines SET status = $1 WHERE id = $2`
		args = []any{string(status), id}
	}

	result, err := db.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("updating pipeline %s status to %q: %w", id, status, err)
	}
	if result.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AddPipelineJob
// ─────────────────────────────────────────────────────────────────────────────

// AddPipelineJob links a created job to a pipeline node.
//
// ON CONFLICT DO NOTHING is the critical idempotency guard:
//
// Crash scenario without ON CONFLICT:
//  1. Advancer calls CreateJob → job abc123 created
//  2. Scheduler crashes before AddPipelineJob
//  3. On restart: node has no entry in pipeline_jobs → still appears as unstarted
//  4. Advancer calls CreateJob again → duplicate job def456 created
//  5. AddPipelineJob(pipeline, node, def456) → succeeds
//     → Two jobs for the same node! abc123 becomes an orphan.
//
// With ON CONFLICT DO NOTHING (current implementation):
//
//	Step 4 creates a new job (old one becomes orphan, reclaimed in ~90s).
//	Step 5 succeeds on the new job_id.
//	→ One active job per node at all times. ✓
//
// The primary key on pipeline_jobs is (pipeline_id, job_id), which means
// the same job cannot be linked twice to the same pipeline.
func (db *DB) AddPipelineJob(ctx context.Context, pipelineID uuid.UUID, nodeID string, jobID uuid.UUID) error {
	const q = `
		INSERT INTO pipeline_jobs (pipeline_id, job_id, node_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (pipeline_id, job_id) DO NOTHING`

	if _, err := db.pool.Exec(ctx, q, pipelineID, jobID, nodeID); err != nil {
		return fmt.Errorf("linking job %s to pipeline %s node %q: %w", jobID, pipelineID, nodeID, err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPipelineJobs
// ─────────────────────────────────────────────────────────────────────────────

// GetPipelineJobs returns all node-to-job mappings for a pipeline,
// joined with the current status of each job.
//
// This is the advancement algorithm's primary read query — called once per
// pipeline per scheduler tick. One JOIN instead of N+1 queries.
//
// Why a JOIN and not separate queries?
//
//	For a 10-node pipeline, separate queries = 10 round-trips to PostgreSQL.
//	A JOIN = 1 round-trip with the index on pipeline_jobs.pipeline_id.
//	At 50 pipelines × 10 nodes × 2s tick = 5000 potential queries/second vs 50.
//
// Result is ordered by node_id (alphabetical) for deterministic iteration
// in tests.
func (db *DB) GetPipelineJobs(ctx context.Context, pipelineID uuid.UUID) ([]*store.PipelineJobStatus, error) {
	const q = `
		SELECT pj.node_id, pj.job_id, j.name, j.status
		FROM pipeline_jobs pj
		JOIN jobs j ON j.id = pj.job_id
		WHERE pj.pipeline_id = $1
		ORDER BY pj.node_id ASC`

	rows, err := db.pool.Query(ctx, q, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("getting pipeline jobs for %s: %w", pipelineID, err)
	}
	defer rows.Close()

	var result []*store.PipelineJobStatus
	for rows.Next() {
		pjs := &store.PipelineJobStatus{}
		var statusStr string
		if err := rows.Scan(&pjs.NodeID, &pjs.JobID, &pjs.JobName, &statusStr); err != nil {
			return nil, fmt.Errorf("scanning pipeline job row: %w", err)
		}
		pjs.JobStatus = domain.JobStatus(statusStr)
		result = append(result, pjs)
	}
	return result, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// scanPipeline — shared scanner for all SELECT queries
// ─────────────────────────────────────────────────────────────────────────────

// pipelineScanner abstracts over pgx.Row and pgx.Rows so scanPipeline
// works for both QueryRow (single row) and Query (multiple rows).
type pipelineScanner interface {
	Scan(dest ...any) error
}

// scanPipeline reads one row into a domain.Pipeline.
// Unmarshals the dag_spec JSONB column back into domain.DAGSpec.
func scanPipeline(row pipelineScanner) (*domain.Pipeline, error) {
	p := &domain.Pipeline{}
	var statusStr string
	var dagJSON []byte

	err := row.Scan(
		&p.ID,
		&p.Name,
		&statusStr,
		&dagJSON,
		&p.CreatedAt,
		&p.UpdatedAt,
		&p.CompletedAt, // *time.Time — pgx handles NULL → nil pointer
	)
	if err != nil {
		return nil, err
	}

	p.Status = domain.PipelineStatus(statusStr)

	// Unmarshal dag_spec JSONB back into the struct.
	// An empty payload '{}' unmarshal produces an empty DAGSpec (valid).
	if len(dagJSON) > 0 && string(dagJSON) != "{}" {
		if err := json.Unmarshal(dagJSON, &p.DAGSpec); err != nil {
			return nil, fmt.Errorf("unmarshaling dag_spec for pipeline %s: %w", p.ID, err)
		}
	}
	return p, nil
}
