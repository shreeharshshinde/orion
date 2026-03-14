package domain

import (
	"time"

	"github.com/google/uuid"
)

// PipelineStatus mirrors the job status machine at the pipeline level.
type PipelineStatus string

const (
	PipelineStatusPending   PipelineStatus = "pending"
	PipelineStatusRunning   PipelineStatus = "running"
	PipelineStatusCompleted PipelineStatus = "completed"
	PipelineStatusFailed    PipelineStatus = "failed"
	PipelineStatusCancelled PipelineStatus = "cancelled"
)

// Pipeline is a DAG of jobs. Edges encode dependencies.
type Pipeline struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Status      PipelineStatus `json:"status"`
	DAGSpec     DAGSpec        `json:"dag_spec"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
}

// DAGSpec holds the adjacency list representation of the pipeline graph.
// Nodes are job IDs (or logical names before jobs are created).
// This is stored as JSONB in PostgreSQL.
type DAGSpec struct {
	Nodes []DAGNode `json:"nodes"`
	Edges []DAGEdge `json:"edges"`
}

// DAGNode represents a job step in the pipeline.
type DAGNode struct {
	ID          string     `json:"id"` // logical name within pipeline
	JobTemplate JobPayload `json:"job_template"`
	JobID       *uuid.UUID `json:"job_id,omitempty"` // populated after job is created
}

// DAGEdge represents a dependency: Target depends on Source.
type DAGEdge struct {
	Source string `json:"source"` // DAGNode.ID
	Target string `json:"target"` // DAGNode.ID
}

// ReadyNodes returns the node IDs that have all dependencies completed.
// Caller passes in the set of already-completed node IDs.
func (d *DAGSpec) ReadyNodes(completed map[string]bool) []string {
	// Build dependency map
	deps := make(map[string][]string)
	for _, edge := range d.Edges {
		deps[edge.Target] = append(deps[edge.Target], edge.Source)
	}

	var ready []string
	for _, node := range d.Nodes {
		if completed[node.ID] {
			continue
		}
		allDepsComplete := true
		for _, dep := range deps[node.ID] {
			if !completed[dep] {
				allDepsComplete = false
				break
			}
		}
		if allDepsComplete {
			ready = append(ready, node.ID)
		}
	}
	return ready
}
