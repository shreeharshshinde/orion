# ADR-010: Weighted Fair Scheduling Across Queues

**Date:** 2024-03-15
**Status:** Accepted
**Authors:** Orion Platform Team

---

## Context

Before Phase 8, the scheduler's `scheduleQueuedJobs` function issued a single SQL query:

```sql
SELECT * FROM jobs WHERE status = 'queued'
ORDER BY priority DESC, created_at ASC
LIMIT 50
```

This is correct but has a starvation problem. If 1000 low-priority batch jobs are queued and 5 high-priority interactive jobs are queued, a batch of 50 will contain all 5 high-priority jobs and 45 low-priority jobs — the high-priority jobs are served, but low-priority jobs accumulate indefinitely when the high queue is never empty.

Conversely, if the low queue is empty, the full batch goes to high-priority jobs with no waste — the system is efficient when load is uneven. The problem only appears when all queues have pending work simultaneously.

Three scheduling approaches were considered:

1. **Strict priority** — always dispatch high-priority jobs first, low-priority only when high is empty
2. **Round-robin** — alternate between queues regardless of priority
3. **Weighted fair queuing** — give each queue a proportional share of the batch, with higher-weight queues getting larger shares

## Decision

**Use weighted fair scheduling** — each queue gets a fraction of the scheduler's dispatch batch proportional to its configured weight. Separate SQL queries per queue replace the single ORDER BY priority query.

```go
// ComputeAllocations distributes BatchSize slots across queues by weight.
func ComputeAllocations(configs map[string]QueueAllocation, batchSize int) []QueueAllocation {
    // Sort by weight descending — highest-weight queue gets first claim on capacity
    // For each queue: limit = floor(batchSize * weight), capped at remaining capacity
    // Unused capacity flows to lower-weight queues
}

// FetchReadyJobs issues one ListJobs query per queue with its computed limit.
// Results are returned in weight-descending order (high queue jobs first).
func (fq *FairQueue) FetchReadyJobs(ctx context.Context) ([]*domain.Job, error) {
    for _, alloc := range fq.queues { // sorted: high → default → low
        jobs, _ := fq.store.ListJobs(ctx, store.JobFilter{
            Status:    &queued,
            QueueName: &alloc.QueueName,
            Limit:     alloc.Limit,
        })
        allJobs = append(allJobs, jobs...)
    }
    return allJobs, nil
}
```

Default weights and their effect on a batch of 50:

| Queue | Weight | Allocation | Guarantee |
|---|---|---|---|
| high | 0.8 | 40 slots | High-priority work always gets majority |
| default | 0.6 | Up to 10 remaining | Balanced work always gets some service |
| low | 0.2 | Up to remaining | Batch work always makes progress |

When a queue has fewer jobs than its allocation, unused slots flow to the next queue. A high queue with only 3 jobs releases 37 slots to default and low.

Evaluated options:

| Algorithm | High-priority latency | Low-priority starvation | Configuration | Complexity |
|---|---|---|---|---|
| Strict priority | Optimal | Permanent under load | None | Low |
| Round-robin | High (waits for others) | None | None | Low |
| **Weighted fair** | **Near-optimal** | **Bounded (weight-proportional)** | **Per-queue weight** | **Medium** |
| Deficit round-robin | Near-optimal | None | Per-queue quantum | High |

Strict priority was rejected because it permanently starves low-priority queues under sustained load — unacceptable for a platform where batch preprocessing jobs fund the compute budget that enables interactive training jobs.

Round-robin was rejected because it gives equal treatment to high and low priority queues — high-priority interactive jobs would wait for batch jobs to take their turn.

Weighted fair scheduling gives high-priority queues first and largest claim on capacity while guaranteeing low-priority queues always make progress — the correct tradeoff for a mixed ML workload platform.

## Consequences

**Positive:**
- High-priority jobs always get `weight_high / sum(weights)` fraction of dispatch capacity — predictable latency
- Low-priority jobs always make progress — no permanent starvation regardless of high queue load
- Unused capacity from sparse queues flows to busier queues automatically
- Weights are configurable at runtime via `PUT /queues/{name}` — no restart needed to rebalance
- The separate-query-per-queue approach is index-friendly: each query hits the partial index `idx_jobs_status_queue` rather than scanning across all queued jobs

**Negative:**
- Three SQL queries per scheduler tick instead of one. At 30 ticks/minute with 3 queues = 90 queries/minute — negligible for PostgreSQL but worth noting
- Weight configuration requires operator judgment. Default weights (0.8/0.6/0.2) are not normalized — they express relative priority, not fractions of 1.0. This can confuse operators expecting percentages.
- A queue with weight=0.8 can still be starved if the rate limiter (ADR-009) exhausts its tokens before filling its allocation

## Implementation Notes

Located in `internal/scheduler/fairqueue.go`. The `ComputeAllocations` function caps each queue's allocation at the remaining batch capacity after higher-weight queues have claimed their share — ensuring the total dispatched never exceeds `BatchSize`. Within each queue's allocation, jobs are still ordered by `priority DESC, created_at ASC` — so the existing per-job priority field remains meaningful for ordering within a queue.

## Related Decisions

- ADR-001: Redis Streams — fair scheduling governs how fast jobs enter Redis; Redis delivers them to workers without further ordering
- ADR-009: Token bucket rate limiting — the rate limiter is the second enforcement layer after the fair scheduler; both must allow a job for it to be dispatched
- ADR-003: CAS state transitions — `TransitionJobState(queued → scheduled)` is called per-job after `FetchReadyJobs`; CAS ensures two concurrent schedulers cannot both claim the same job even under fair scheduling