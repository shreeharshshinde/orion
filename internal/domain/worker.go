package domain

import "time"

// WorkerStatus represents the operational state of a worker instance.
type WorkerStatus string

const (
	WorkerStatusIdle     WorkerStatus = "idle"
	WorkerStatusBusy     WorkerStatus = "busy"
	WorkerStatusDraining WorkerStatus = "draining" // graceful shutdown in progress
	WorkerStatusOffline  WorkerStatus = "offline"
)

// Worker represents a registered worker instance in the system.
type Worker struct {
	ID           string       `json:"id"`
	Hostname     string       `json:"hostname"`
	QueueNames   []string     `json:"queue_names"`
	Concurrency  int          `json:"concurrency"`
	ActiveJobs   int          `json:"active_jobs"`
	Status       WorkerStatus `json:"status"`
	LastHeartbeat time.Time  `json:"last_heartbeat"`
	RegisteredAt time.Time   `json:"registered_at"`
}

// IsAlive returns false if the worker has missed heartbeats beyond the TTL.
func (w *Worker) IsAlive(ttl time.Duration) bool {
	return time.Since(w.LastHeartbeat) <= ttl
}

// AvailableSlots returns how many more concurrent jobs this worker can accept.
func (w *Worker) AvailableSlots() int {
	slots := w.Concurrency - w.ActiveJobs
	if slots < 0 {
		return 0
	}
	return slots
}