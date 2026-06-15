package dispatch

import (
	"context"
	"time"

	"github.com/moyoez/flowup/internal/store"
)

// Sink delivers a side effect to an external system, keyed by an idempotency key
// the destination dedups on. Deliver MUST be idempotent: the outbox is
// at-least-once, so the same key may be delivered more than once (e.g. after a
// crash between deliver and mark). Idempotency at the destination makes the
// effect exactly-once.
type Sink interface {
	Deliver(ctx context.Context, key string, payload []byte) error
}

// DeliverOutbox drains up to `limit` pending outbox entries through the sink,
// marking each delivered. A sink error leaves that entry pending for the next
// pass (at-least-once). Returns how many were delivered.
func DeliverOutbox(ctx context.Context, st store.Store, sink Sink, limit int) (int, error) {
	entries, err := st.ClaimPendingOutbox(ctx, limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if err := sink.Deliver(ctx, e.Key, e.Payload); err != nil {
			continue // retry next pass
		}
		if err := st.MarkDelivered(ctx, e.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RunOutbox drains the outbox on a ticker until ctx is cancelled.
func RunOutbox(ctx context.Context, st store.Store, sink Sink, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = DeliverOutbox(ctx, st, sink, 100)
		}
	}
}
