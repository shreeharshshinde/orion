// Package scheduler — fairqueue.go
//
// Implements weighted fair dispatch across queues.
//
// Problem with ORDER BY priority alone:
//   A single ListJobs query with ORDER BY priority DESC, LIMIT N means the
//   highest-priority queue always consumes the full batch when it has enough jobs.
//   Low-priority queues starve even if they've been waiting for hours.
//
// Phase 8 solution — per-queue allocation:
//   Each queue gets an independent slice of the batch proportional to its weight.
//   Unused capacity from a queue with few jobs flows to lower-priority queues.
//   This guarantees every queue gets service proportional to its weight.
//
// Example with batchSize=50, weights high=0.8, default=0.6, low=0.2:
//   high    → min(40, 50) = 40 slots
//   default → min(30, 10) = 10 slots  (only 10 remaining)
//   low     → 0 slots                 (none remaining)
//   But if high has only 3 jobs: high=3, default=30, low=10, 7 unused.
package scheduler

import (
	"context"
	"log/slog"

	"github.com/shreeharshshinde/orion/internal/domain"
	"github.com/shreeharshshinde/orion/internal/store"
)

// QueueAllocation holds the dispatch plan for one queue in a scheduler tick.
type QueueAllocation struct {
	QueueName string  // e.g. "orion:queue:high"
	Limit     int     // how many jobs to fetch and dispatch from this queue this tick
	Weight    float64 // configured weight (0.0–1.0); used by ComputeAllocations
}

// FairQueue fetches jobs from all queues according to their weighted allocations
// and applies the rate limiter before each dispatch.
type FairQueue struct {
	queues  []QueueAllocation // sorted: highest weight first
	limiter *QueueRateLimiter
	store   store.Store
	logger  *slog.Logger
}

// NewFairQueue creates a FairQueue from the given allocations.
// Allocations are sorted by weight descending so high-priority queues are
// fetched and dispatched first within each tick.
func NewFairQueue(
	allocations []QueueAllocation,
	limiter *QueueRateLimiter,
	s store.Store,
	logger *slog.Logger,
) *FairQueue {
	sorted := make([]QueueAllocation, len(allocations))
	copy(sorted, allocations)
	sortByWeightDesc(sorted)
	return &FairQueue{queues: sorted, limiter: limiter, store: s, logger: logger}
}

// FetchReadyJobs fetches jobs from all queues according to their allocations.
// Each queue is queried independently — avoids one queue's backlog affecting another.
// Returns jobs in dispatch priority order (highest-weight queue first).
func (fq *FairQueue) FetchReadyJobs(ctx context.Context) ([]*domain.Job, error) {
	var allJobs []*domain.Job

	for _, alloc := range fq.queues {
		if alloc.Limit <= 0 {
			continue
		}

		queueName := alloc.QueueName
		status := domain.JobStatusQueued

		jobs, err := fq.store.ListJobs(ctx, store.JobFilter{
			Status:    &status,
			QueueName: &queueName,
			Limit:     alloc.Limit,
		})
		if err != nil {
			// Log and continue — a single queue failure should not block others.
			fq.logger.Error("failed to list jobs for queue",
				"queue", queueName, "err", err)
			continue
		}
		allJobs = append(allJobs, jobs...)
	}

	return allJobs, nil
}

// AllowDispatch checks the rate limiter before dispatching a job.
// Returns true if the job should be dispatched this tick.
func (fq *FairQueue) AllowDispatch(queueName string) bool {
	return fq.limiter.Allow(queueName)
}

// ComputeAllocations calculates per-queue dispatch limits for a given total batch size.
// Processes queues in weight-descending order; unused capacity flows to lower queues.
// The sum of returned Limit values never exceeds batchSize.
//
// Called once per scheduler tick to build the FairQueue.
func ComputeAllocations(queueCfgs map[string]QueueAllocation, batchSize int) []QueueAllocation {
	// Sort by weight descending so highest-weight queues get first claim.
	sorted := make([]QueueAllocation, 0, len(queueCfgs))
	for _, v := range queueCfgs {
		sorted = append(sorted, v)
	}
	sortByWeightDesc(sorted)

	result := make([]QueueAllocation, 0, len(sorted))
	remaining := batchSize

	for _, cfg := range sorted {
		limit := int(float64(batchSize) * cfg.Weight)
		if limit > remaining {
			limit = remaining
		}
		result = append(result, QueueAllocation{
			QueueName: cfg.QueueName,
			Weight:    cfg.Weight,
			Limit:     limit,
		})
		remaining -= limit
		if remaining <= 0 {
			break
		}
	}
	return result
}

// sortByWeightDesc sorts allocations by Weight descending (insertion sort — N is always small).
func sortByWeightDesc(a []QueueAllocation) {
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j].Weight < key.Weight {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}
