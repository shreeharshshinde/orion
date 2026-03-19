package retry

import (
	"math"
	"math/rand"
	"time"
)

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