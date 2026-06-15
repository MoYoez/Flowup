// Package ratelimit is a token-bucket limiter for resource-level limits — e.g.
// throttling calls to one external dependency (a PaaS API, a model) so a fan-out
// of nodes can't overwhelm it. Standalone, no dependencies.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Bucket is a token bucket: up to `burst` tokens, refilled at `perSec`/second.
type Bucket struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	refill float64 // tokens per second
	last   time.Time
}

// New builds a bucket that allows bursts up to `burst`, refilling `perSec`/s.
func New(perSec, burst float64) *Bucket {
	return &Bucket{tokens: burst, max: burst, refill: perSec, last: time.Now()}
}

func (b *Bucket) refillLocked() {
	now := time.Now()
	b.tokens = min(b.max, b.tokens+now.Sub(b.last).Seconds()*b.refill)
	b.last = now
}

// Allow consumes one token if available (non-blocking).
func (b *Bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Wait blocks until a token is available or ctx is done.
func (b *Bucket) Wait(ctx context.Context) error {
	for {
		if b.Allow() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
