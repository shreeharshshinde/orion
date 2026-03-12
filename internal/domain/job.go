package domain

import (
	"github.com/google/uuid"
	"time"
)

// JobStatus represents all valid states in the job state machine.
// Transitions must go through Store.TransitionJobState to enforce CAS semantics.
type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusScheduled JobStatus = "scheduled"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusRetrying  JobStatus = "retrying"
	JobStatusDead      JobStatus = "dead" // exceeded max_retries, moved to DLQ
	JobStatusCancelled JobStatus = "cancelled"
)

// ValidTransitions defines the allowed state machine transitions.
// Any transition not in this map is illegal and must be rejected.
var ValidTransitions = map[JobStatus][]JobStatus{
	JobStatusQueued:    {JobStatusScheduled, JobStatusCancelled},
	JobStatusScheduled: {JobStatusRunning, JobStatusQueued, JobStatusCancelled},
	JobStatusRunning:   {JobStatusCompleted, JobStatusFailed},
	JobStatusFailed:    {JobStatusRetrying, JobStatusDead},
	JobStatusRetrying:  {JobStatusQueued},
}

// JobType defines the executor backend to use.
type JobType string

const (
	JobTypeInline     JobType = "inline"  // run inside the worker process
	JobTypeKubernetes JobType = "k8s_job" // launch a Kubernetes Job
)

// JobPriority is an integer 1–10. Higher = more urgent.
// Scheduler uses this for ordering within a queue.
type JobPriority int8

const (
	PriorityLow      JobPriority = 3
	PriorityNormal   JobPriority = 5
	PriorityHigh     JobPriority = 8
	PriorityCritical JobPriority = 10
)

// Job is the central domain entity. It represents a unit of ML work.
type Job struct {
	ID             uuid.UUID   `json:"id"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	Name           string      `json:"name"`
	Type           JobType     `json:"type"`
	QueueName      string      `json:"queue_name"`
	Priority       JobPriority `json:"priority"`
	Status         JobStatus   `json:"status"`
	Payload        JobPayload  `json:"payload"`
	MaxRetries     int         `json:"max_retries"`
	Attempt        int         `json:"attempt"`
	WorkerID       string      `json:"worker_id,omitempty"`
	ErrorMessage   string      `json:"error_message,omitempty"`
	ScheduledAt    *time.Time  `json:"scheduled_at,omitempty"` // nil = run ASAP
	Deadline       *time.Time  `json:"deadline,omitempty"`
	NextRetryAt    *time.Time  `json:"next_retry_at,omitempty"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	CompletedAt    *time.Time  `json:"completed_at,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// JobPayload carries the ML workload specification.
// For k8s_job type, the KubernetesSpec is populated.
// For inline type, HandlerName + Args are used.
type JobPayload struct {
	HandlerName    string          `json:"handler_name,omitempty"`
	Args           map[string]any  `json:"args,omitempty"`
	KubernetesSpec *KubernetesSpec `json:"kubernetes_spec,omitempty"`
}

// KubernetesSpec describes the Kubernetes Job to be launched.
type KubernetesSpec struct {
	Image           string            `json:"image"`
	Command         []string          `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Namespace       string            `json:"namespace"`
	ResourceRequest ResourceRequest   `json:"resources"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
	ServiceAccount  string            `json:"service_account,omitempty"`
	TTLSeconds      int32             `json:"ttl_seconds,omitempty"` // cleanup after completion
}

// ResourceRequest maps to Kubernetes resource requests/limits.
type ResourceRequest struct {
	CPU    string `json:"cpu"`    // e.g., "500m"
	Memory string `json:"memory"` // e.g., "1Gi"
	GPU    int    `json:"gpu,omitempty"`
}

// JobExecution is an immutable audit record of a single execution attempt.
// Never update rows in this table — only insert.
type JobExecution struct {
	ID         uuid.UUID  `json:"id"`
	JobID      uuid.UUID  `json:"job_id"`
	Attempt    int        `json:"attempt"`
	WorkerID   string     `json:"worker_id"`
	Status     JobStatus  `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	LogsRef    string     `json:"logs_ref,omitempty"` // S3/GCS URI
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CanTransitionTo checks if a state transition is valid per the state machine.
func (j *Job) CanTransitionTo(next JobStatus) bool {
	allowed, ok := ValidTransitions[j.Status]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

// IsTerminal returns true if the job has reached a final state.
func (j *Job) IsTerminal() bool {
	return j.Status == JobStatusCompleted ||
		j.Status == JobStatusDead ||
		j.Status == JobStatusCancelled
}

// IsRetryable returns true if the job has failed but hasn't exhausted retries.
func (j *Job) IsRetryable() bool {
	return j.Status == JobStatusFailed && j.Attempt < j.MaxRetries
}
