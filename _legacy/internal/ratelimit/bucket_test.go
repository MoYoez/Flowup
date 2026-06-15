package ratelimit_test

import (
	"context"
	"testing"

	"github.com/moyoez/flowup/internal/ratelimit"
)

func TestBucketBurstThenEmpty(t *testing.T) {
	// burst of 2, negligible refill within the test window.
	b := ratelimit.New(0.0001, 2)
	if !b.Allow() || !b.Allow() {
		t.Fatal("first two within burst should be allowed")
	}
	if b.Allow() {
		t.Fatal("third should be denied (burst exhausted)")
	}
}

func TestBucketWaitRespectsContext(t *testing.T) {
	b := ratelimit.New(0, 0) // never refills, no burst
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Wait(ctx); err == nil {
		t.Fatal("Wait must return when ctx is cancelled and no token is available")
	}
}
