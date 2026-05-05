// Package scheduler — ratelimiter.go
//
// Implements a thread-safe token bucket rate limiter per queue.
//
// Algorithm:
//   - Each queue has an independent bucket that refills at RatePerSec tokens/second.
//   - Tokens accumulate up to Burst (burst capacity).
//   - One token is consumed per job dispatched.
//   - If the bucket is empty, Allow() returns false and the job is skipped until
//     the next scheduler tick when tokens have refilled.
//
// Why float64 for tokens:
//   At 10 tokens/sec with a 50ms tick, integer arithmetic would add 0 tokens per tick
//   and the rate limiter would never fire. float64 allows fractional accumulation.
package scheduler

import (
	"sync"
	"time"
)

// BucketConfig holds the parameters for one queue's token bucket.
// Exported so cmd/scheduler/main.go can build the map without importing internals.
type BucketConfig struct {
	RatePerSec float64 // tokens added per second
	Burst      int     // maximum token accumulation (burst capacity)
}

// QueueRateLimiter holds independent token buckets for each queue.
// All methods are thread-safe via the mu mutex.
type QueueRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens     float64   // current token count (float for sub-second refills)
	maxTokens  float64   // burst capacity
	refillRate float64   // tokens added per second
	lastRefill time.Time // when tokens were last added
}

// NewQueueRateLimiter creates a QueueRateLimiter with one bucket per queue.
// All buckets start full (tokens == burst) so the first batch dispatches immediately.
func NewQueueRateLimiter(configs map[string]BucketConfig) *QueueRateLimiter {
	rl := &QueueRateLimiter{
		buckets: make(map[string]*tokenBucket, len(configs)),
	}
	for name, cfg := range configs {
		rl.buckets[name] = &tokenBucket{
			tokens:     float64(cfg.Burst), // start full
			maxTokens:  float64(cfg.Burst),
			refillRate: cfg.RatePerSec,
			lastRefill: time.Now(),
		}
	}
	return rl
}

// Allow returns true if one token is available for the given queue, consuming it
// atomically. Returns false if the bucket is empty (rate limited).
//
// Refill happens lazily on each call: elapsed time since lastRefill is multiplied
// by refillRate and added to the bucket, capped at maxTokens.
func (rl *QueueRateLimiter) Allow(queueName string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, ok := rl.buckets[queueName]
	if !ok {
		return true // unknown queue: no limit applied
	}

	// Refill tokens based on elapsed time since last refill.
	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.tokens = minFloat(bucket.maxTokens, bucket.tokens+elapsed*bucket.refillRate)
	bucket.lastRefill = now

	if bucket.tokens < 1.0 {
		return false // rate limited
	}
	bucket.tokens--
	return true
}

// UpdateConfig replaces the configuration for a queue's bucket.
// Called when the scheduler reloads queue_config from PostgreSQL (live reload).
// If the queue does not exist yet, a new bucket is created starting full.
func (rl *QueueRateLimiter) UpdateConfig(queueName string, ratePerSec float64, burst int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if bucket, ok := rl.buckets[queueName]; ok {
		bucket.refillRate = ratePerSec
		bucket.maxTokens = float64(burst)
		// Cap current tokens to new max if it decreased.
		if bucket.tokens > bucket.maxTokens {
			bucket.tokens = bucket.maxTokens
		}
	} else {
		rl.buckets[queueName] = &tokenBucket{
			tokens:     float64(burst),
			maxTokens:  float64(burst),
			refillRate: ratePerSec,
			lastRefill: time.Now(),
		}
	}
}

// Available returns the approximate current token count for a queue.
// Used for the GET /queues/{name}/stats API endpoint and Prometheus metrics.
func (rl *QueueRateLimiter) Available(queueName string) float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if b, ok := rl.buckets[queueName]; ok {
		return b.tokens
	}
	return 0
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
