package retry

import (
	"context"
	"math"
	"math/rand"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Do — context-aware retry wrapper
// ─────────────────────────────────────────────────────────────────────────────

// Options configures the Do retry loop.
type Options struct {
	MaxAttempts int           // total attempts (default 3)
	Base        time.Duration // initial backoff (default 100ms)
	Cap         time.Duration // maximum backoff (default 30s)
}

// Option is a functional option for Do.
type Option func(*Options)

// WithMaxAttempts sets the maximum number of attempts.
func WithMaxAttempts(n int) Option { return func(o *Options) { o.MaxAttempts = n } }

// WithBase sets the initial backoff duration.
func WithBase(d time.Duration) Option { return func(o *Options) { o.Base = d } }

// WithCap sets the maximum backoff duration.
func WithCap(d time.Duration) Option { return func(o *Options) { o.Cap = d } }

// Do calls fn repeatedly with full-jitter exponential backoff until fn returns
// nil, all attempts are exhausted, or ctx is cancelled.
//
// The sleep between attempts is interrupted immediately when ctx is cancelled,
// so callers never wait longer than the context deadline allows.
//
// Returns ctx.Err() if the context is cancelled during a sleep, or the last
// error returned by fn if all attempts are exhausted.
func Do(ctx context.Context, fn func(ctx context.Context) error, opts ...Option) error {
	o := Options{MaxAttempts: 3, Base: 100 * time.Millisecond, Cap: 30 * time.Second}
	for _, opt := range opts {
		opt(&o)
	}

	var lastErr error
	for i := 0; i < o.MaxAttempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lastErr = fn(ctx); lastErr == nil {
			return nil
		}
		if i < o.MaxAttempts-1 {
			delay := FullJitterBackoff(i, o.Base, o.Cap)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return lastErr
}

// FullJitterBackoff computes a random delay between 0 and min(cap, base * 2^attempt).
//
// Full jitter produces the best thundering-herd prevention compared to
// equal jitter or pure exponential. Reference: AWS "Exponential Backoff And Jitter" (2015).
//
// Parameters:
//   - attempt: zero-indexed attempt number (0 = first failure)
//   - base: initial backoff duration
//   - cap: maximum backoff duration
func FullJitterBackoff(attempt int, base, cap time.Duration) time.Duration {
	// Exponential ceiling
	exp := base * time.Duration(math.Pow(2, float64(attempt)))
	if exp > cap {
		exp = cap
	}
	// Random value in [0, exp)
	if exp <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(exp)))
}

// EqualJitterBackoff splits the range — half is guaranteed, half is random.
// Less aggressive spreading than FullJitter but guarantees minimum progress.
func EqualJitterBackoff(attempt int, base, cap time.Duration) time.Duration {
	exp := base * time.Duration(math.Pow(2, float64(attempt)))
	if exp > cap {
		exp = cap
	}
	half := exp / 2
	return half + time.Duration(rand.Int63n(int64(half+1)))
}

// WithRetry executes fn up to maxAttempts times, sleeping between failures.
// Returns nil on first success, or the last error if all attempts fail.
// Use this for infrastructure calls (DB, Redis) that should retry transparently.
func WithRetry(attempts int, base, cap time.Duration, fn func() error) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < attempts-1 {
			delay := FullJitterBackoff(i, base, cap)
			time.Sleep(delay)
		}
	}
	return lastErr
}