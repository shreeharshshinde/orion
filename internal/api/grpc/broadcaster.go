// Package grpc implements the Orion gRPC service and its supporting types.
// broadcaster.go: in-memory pub/sub hub for real-time job events.
package grpc

import (
	"sync"

	orionv1 "github.com/shreeharshshinde/orion/proto/orion/v1"
)

// Broadcaster is a thread-safe, in-memory fan-out hub for job events.
//
// Architecture:
//   - Each job ID maps to a set of subscriber channels.
//   - When a state transition occurs, Publish fans out to all subscribers.
//   - Subscribers receive events via buffered channels (capacity 16).
//   - Slow consumers don't block publishers — overflow events are dropped.
//     The polling fallback in WatchJob catches any missed events.
//
// Memory safety: subscribers MUST call the returned unsubscribe func when done.
// WatchJob defers unsubscribe() immediately after Subscribe() to prevent leaks.
type Broadcaster struct {
	mu          sync.RWMutex
	subscribers map[string]map[uint64]chan *orionv1.JobEvent
	nextID      uint64
}

// NewBroadcaster creates an empty Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subscribers: make(map[string]map[uint64]chan *orionv1.JobEvent),
	}
}

// Publish sends an event to all subscribers watching the given job ID.
// Non-blocking: if a subscriber's buffer is full, the event is dropped.
// Safe to call from multiple goroutines simultaneously.
func (b *Broadcaster) Publish(jobID string, event *orionv1.JobEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs, ok := b.subscribers[jobID]
	if !ok {
		return // no subscribers — fast path
	}
	for _, ch := range subs {
		select {
		case ch <- event:
		default: // buffer full — drop; polling fallback catches it
		}
	}
}

// Subscribe returns a buffered channel for events on the given job ID,
// and an unsubscribe function that MUST be called when done.
//
//	ch, unsub := b.Subscribe(jobID)
//	defer unsub()
//	for event := range ch { ... }
func (b *Broadcaster) Subscribe(jobID string) (<-chan *orionv1.JobEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	subID := b.nextID
	ch := make(chan *orionv1.JobEvent, 16)

	if b.subscribers[jobID] == nil {
		b.subscribers[jobID] = make(map[uint64]chan *orionv1.JobEvent)
	}
	b.subscribers[jobID][subID] = ch

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subscribers[jobID]; ok {
			delete(subs, subID)
			close(ch)
			if len(subs) == 0 {
				delete(b.subscribers, jobID)
			}
		}
	}
	return ch, unsubscribe
}

// SubscriberCount returns the number of active subscribers for a job ID.
// Used in tests and health-check metrics.
func (b *Broadcaster) SubscriberCount(jobID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[jobID])
}
