package dispatch_test

import (
	"context"
	"sync"
	"testing"

	"github.com/moyoez/flowup/internal/dispatch"
)

// idempotentSink models an external API that dedups on the key: the effect is
// applied at most once regardless of how many times Deliver is called.
type idempotentSink struct {
	mu      sync.Mutex
	seen    map[string]bool
	effects int // distinct effects actually applied
	calls   int // total Deliver calls
}

func newSink() *idempotentSink { return &idempotentSink{seen: map[string]bool{}} }

func (s *idempotentSink) Deliver(_ context.Context, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.seen[key] {
		return nil // already applied at the destination
	}
	s.seen[key] = true
	s.effects++
	return nil
}

// Exactly-once effect even under at-least-once (duplicate) delivery.
func TestOutboxExactlyOnceEffect(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	sink := newSink()

	// Idempotent enqueue: same key twice → one pending entry.
	if err := st.EnqueueOutbox(ctx, "deploy-k", []byte(`{"env":"prod"}`)); err != nil {
		t.Fatal(err)
	}
	_ = st.EnqueueOutbox(ctx, "deploy-k", []byte(`{"env":"prod"}`))
	pending, _ := st.ClaimPendingOutbox(ctx, 10)
	if len(pending) != 1 {
		t.Fatalf("want 1 pending outbox entry, got %d", len(pending))
	}

	// First drain delivers it.
	n, err := dispatch.DeliverOutbox(ctx, st, sink, 10)
	if err != nil || n != 1 {
		t.Fatalf("drain: n=%d err=%v", n, err)
	}
	// Nothing left pending.
	if p, _ := st.ClaimPendingOutbox(ctx, 10); len(p) != 0 {
		t.Fatalf("want 0 pending after delivery, got %d", len(p))
	}

	// Simulate a crash-redelivery: the same key delivered again (the destination
	// dedups). The effect must still be exactly once.
	_ = sink.Deliver(ctx, "deploy-k", nil)
	if sink.calls < 2 {
		t.Fatalf("expected at least 2 delivery attempts, got %d", sink.calls)
	}
	if sink.effects != 1 {
		t.Fatalf("side effect must be applied exactly once, applied %d times", sink.effects)
	}
}
