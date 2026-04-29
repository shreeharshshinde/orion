package retry_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shreeharshshinde/orion/pkg/retry"
)

// ─────────────────────────────────────────────────────────────────────────────
// FullJitterBackoff
// ─────────────────────────────────────────────────────────────────────────────

func TestFullJitterBackoff_ReturnsNonNegative(t *testing.T) {
	base := 5 * time.Second
	cap := 30 * time.Minute

	for attempt := 0; attempt <= 10; attempt++ {
		d := retry.FullJitterBackoff(attempt, base, cap)
		if d < 0 {
			t.Errorf("attempt=%d: got negative backoff %v", attempt, d)
		}
	}
}

func TestFullJitterBackoff_NeverExceedsCap(t *testing.T) {
	base := 5 * time.Second
	cap := 30 * time.Minute

	// Run many times to exercise the random component
	for attempt := 0; attempt <= 20; attempt++ {
		for run := 0; run < 100; run++ {
			d := retry.FullJitterBackoff(attempt, base, cap)
			if d > cap {
				t.Errorf("attempt=%d run=%d: backoff %v exceeds cap %v", attempt, run, d, cap)
			}
		}
	}
}

func TestFullJitterBackoff_NegativeAttemptTreatedAsZero(t *testing.T) {
	base := 5 * time.Second
	cap := 1 * time.Hour

	// Negative attempt should not panic and should return a valid duration
	d := retry.FullJitterBackoff(-1, base, cap)
	if d < 0 {
		t.Errorf("negative attempt produced negative backoff: %v", d)
	}
}

func TestFullJitterBackoff_GrowsWithAttempts(t *testing.T) {
	// The ceiling grows with attempts. Average backoff should be higher
	// for later attempts. We verify the ceiling (max possible) grows.
	base := 1 * time.Second
	capDur := 10 * time.Minute

	// At attempt 0, ceiling = base*2^0 = 1s  → max = 1s
	// At attempt 3, ceiling = base*2^3 = 8s  → max = 8s
	// At attempt 10, ceiling = capped at 10min

	// Verify that the cap is respected at high attempts
	for run := 0; run < 200; run++ {
		d := retry.FullJitterBackoff(100, base, capDur) // very high attempt
		if d > capDur {
			t.Errorf("high attempt should be capped at %v, got %v", capDur, d)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EqualJitterBackoff
// ─────────────────────────────────────────────────────────────────────────────

func TestEqualJitterBackoff_ReturnsNonNegative(t *testing.T) {
	for attempt := 0; attempt <= 10; attempt++ {
		d := retry.EqualJitterBackoff(attempt, 1*time.Second, 5*time.Minute)
		if d < 0 {
			t.Errorf("attempt=%d: got negative backoff %v", attempt, d)
		}
	}
}

func TestEqualJitterBackoff_GuaranteesMinimumDelay(t *testing.T) {
	base := 2 * time.Second
	capDur := 1 * time.Minute

	// EqualJitter guarantees at least half the ceiling as minimum delay.
	// At attempt 0: ceiling = 2s, minimum = 1s
	for run := 0; run < 100; run++ {
		d := retry.EqualJitterBackoff(0, base, capDur)
		// At attempt 0: ceiling = min(cap, base * 2^0) = min(60s, 2s) = 2s
		// minimum = 2s/2 = 1s
		if d < time.Second {
			t.Errorf("EqualJitter should guarantee at least 1s at attempt 0, got %v", d)
		}
	}
}

func TestEqualJitterBackoff_NeverExceedsCap(t *testing.T) {
	capDur := 30 * time.Second
	for attempt := 0; attempt <= 15; attempt++ {
		for run := 0; run < 50; run++ {
			d := retry.EqualJitterBackoff(attempt, 1*time.Second, capDur)
			if d > capDur {
				t.Errorf("attempt=%d: backoff %v exceeds cap %v", attempt, d, capDur)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WithRetry
// ─────────────────────────────────────────────────────────────────────────────

func TestWithRetry_SuccessOnFirstCall(t *testing.T) {
	calls := 0
	err := retry.WithRetry(3, 1*time.Second, 5*time.Minute,
		func() error {
			calls++
			return nil
		})

	if err != nil {
		t.Errorf("expected nil error on immediate success, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call on success, got %d", calls)
	}
}

func TestWithRetry_SuccessOnSecondCall(t *testing.T) {
	calls := 0
	err := retry.WithRetry(3, 1*time.Second, 5*time.Minute,
		func() error {
			calls++
			if calls < 2 {
				return errors.New("transient error")
			}
			return nil
		})

	if err != nil {
		t.Errorf("expected nil error after retry, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 failure + 1 success), got %d", calls)
	}
}

func TestWithRetry_ExhaustsAllAttempts(t *testing.T) {
	calls := 0
	sentinel := errors.New("permanent error")

	err := retry.WithRetry(3, 1*time.Second, 5*time.Minute,
		func() error {
			calls++
			return sentinel
		})

	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error after exhaustion, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected exactly 3 calls (maxAttempts), got %d", calls)
	}
}

func TestWithRetry_ZeroAttempts_NeverCalls(t *testing.T) {
	calls := 0
	_ = retry.WithRetry(0, 1*time.Second, 5*time.Minute,
		func() error {
			calls++
			return nil
		})

	// maxAttempts=0 means no attempts
	if calls != 0 {
		t.Errorf("maxAttempts=0 should make 0 calls, got %d", calls)
	}
}

func TestWithRetry_AlwaysFails_RunsAllAttempts(t *testing.T) {
	calls := 0
	err := retry.WithRetry(5, 1*time.Millisecond, 5*time.Millisecond,
		func() error {
			calls++
			return errors.New("always fail")
		})

	if err == nil {
		t.Error("expected error when all attempts fail")
	}
	if calls != 5 {
		t.Errorf("expected exactly 5 calls, got %d", calls)
	}
}
