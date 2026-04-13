// Package postgres_test contains unit tests that do NOT require a database.
//
// These tests run as part of normal `go test ./...` with no special flags.
// They test pure functions, error helpers, and logic that can be validated
// without a real PostgreSQL connection.
package postgres_test

import (
	"errors"
	"testing"

	"github.com/shreeharshshinde/orion/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// StoreError and sentinel error tests
// ─────────────────────────────────────────────────────────────────────────────

func TestStoreError_Is_SameCode(t *testing.T) {
	err := store.ErrNotFound
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("errors.Is should return true for same sentinel")
	}
}

func TestStoreError_Is_DifferentCode(t *testing.T) {
	if errors.Is(store.ErrNotFound, store.ErrStateConflict) {
		t.Error("ErrNotFound should not match ErrStateConflict")
	}
}

func TestStoreError_Is_NonStoreError(t *testing.T) {
	other := errors.New("some other error")
	if errors.Is(other, store.ErrNotFound) {
		t.Error("a non-StoreError should not match ErrNotFound")
	}
}

func TestStoreError_Error_WithMessage(t *testing.T) {
	e := &store.StoreError{Code: "NOT_FOUND", Message: "job 123 not found"}
	got := e.Error()
	want := "NOT_FOUND: job 123 not found"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestStoreError_Error_WithoutMessage(t *testing.T) {
	e := &store.StoreError{Code: "STATE_CONFLICT"}
	got := e.Error()
	want := "STATE_CONFLICT"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsNotFound / IsStateConflict helpers (convenience functions)
// ─────────────────────────────────────────────────────────────────────────────

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrNotFound matches", store.ErrNotFound, true},
		{"ErrStateConflict no match", store.ErrStateConflict, false},
		{"ErrDuplicate no match", store.ErrDuplicate, false},
		{"nil no match", nil, false},
		{"generic error no match", errors.New("other"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := store.IsNotFound(tc.err)
			if got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsStateConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ErrStateConflict matches", store.ErrStateConflict, true},
		{"ErrNotFound no match", store.ErrNotFound, false},
		{"nil no match", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := store.IsStateConflict(tc.err)
			if got != tc.want {
				t.Errorf("IsStateConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TransitionOption tests
// ─────────────────────────────────────────────────────────────────────────────

func TestWithWorkerID(t *testing.T) {
	u := &store.TransitionUpdateExported{}
	store.WithWorkerID("worker-abc")(u)
	if u.WorkerID == nil || *u.WorkerID != "worker-abc" {
		t.Errorf("WithWorkerID: got %v, want 'worker-abc'", u.WorkerID)
	}
}

func TestWithError(t *testing.T) {
	u := &store.TransitionUpdateExported{}
	store.WithError("something went wrong")(u)
	if u.ErrorMessage == nil || *u.ErrorMessage != "something went wrong" {
		t.Errorf("WithError: got %v", u.ErrorMessage)
	}
}

func TestMultipleOptions_Applied(t *testing.T) {
	u := &store.TransitionUpdateExported{}
	store.WithWorkerID("w1")(u)
	store.WithError("err msg")(u)

	if u.WorkerID == nil || *u.WorkerID != "w1" {
		t.Error("WorkerID not set")
	}
	if u.ErrorMessage == nil || *u.ErrorMessage != "err msg" {
		t.Error("ErrorMessage not set")
	}
	// Other fields should remain nil.
	if u.NextRetryAt != nil {
		t.Error("NextRetryAt should be nil")
	}
	if u.StartedAt != nil {
		t.Error("StartedAt should be nil")
	}
}