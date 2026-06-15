package store_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/moyoez/flowup/internal/store"
)

func TestAESGCMRoundTripAndTamper(t *testing.T) {
	c, err := store.NewAESGCM(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Encrypt([]byte("hello-secret"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := c.Decrypt(ct)
	if err != nil || string(pt) != "hello-secret" {
		t.Fatalf("round-trip failed: %s err=%v", pt, err)
	}
	ct[len(ct)-1] ^= 0xff // tamper
	if _, err := c.Decrypt(ct); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
	if _, err := store.NewAESGCM([]byte("short")); err == nil {
		t.Fatal("non-32-byte key must be rejected")
	}
}

// Payloads are encrypted at rest: the underlying store holds ciphertext, the
// decorator returns plaintext.
func TestEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	inner := newStore(t)
	ciph, _ := store.NewAESGCM(make([]byte, 32))
	enc := store.NewEncrypted(inner, ciph)

	const secret = `{"api_key":"SK-SECRET-XYZ"}`
	if err := enc.PutStep(ctx, store.StepRecord{RunID: "r", StepKey: "n", Output: []byte(secret)}); err != nil {
		t.Fatal(err)
	}

	// Via the decorator → plaintext.
	got, ok, err := enc.GetStep(ctx, "r", "n")
	if err != nil || !ok || string(got.Output) != secret {
		t.Fatalf("decorator read: ok=%v err=%v out=%s", ok, err, got.Output)
	}
	// Via the underlying store → ciphertext, the secret never appears.
	raw, ok, _ := inner.GetStep(ctx, "r", "n")
	if !ok {
		t.Fatal("underlying step missing")
	}
	if strings.Contains(string(raw.Output), "SK-SECRET-XYZ") {
		t.Fatalf("secret stored in plaintext at rest: %s", raw.Output)
	}

	// Run input/output also round-trips.
	created, _, err := enc.CreateRun(ctx, store.RunRecord{RunID: "r2", PipelineID: "p", IdempotencyKey: "k2", Status: store.RunPending, Input: []byte(secret)})
	if err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	run, ok, _ := enc.GetRun(ctx, "r2")
	if !ok || string(run.Input) != secret {
		t.Fatalf("run input round-trip: %s", run.Input)
	}
	rawRun, _, _ := inner.GetRun(ctx, "r2")
	if strings.Contains(string(rawRun.Input), "SK-SECRET-XYZ") {
		t.Fatalf("run input stored plaintext: %s", rawRun.Input)
	}
}

// PurgeRuns enforces retention: runs older than the cutoff (and their children) go.
func TestPurgeRuns(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	_, _, _ = st.CreateRun(ctx, store.RunRecord{RunID: "old", PipelineID: "p", IdempotencyKey: "old", Status: store.RunOK, CreatedAt: "2020-01-01T00:00:00Z"})
	_, _, _ = st.CreateRun(ctx, store.RunRecord{RunID: "new", PipelineID: "p", IdempotencyKey: "new", Status: store.RunOK, CreatedAt: "2030-01-01T00:00:00Z"})
	_ = st.AppendEvent(ctx, store.EventRecord{RunID: "old", NodeID: "n", Type: "x", Data: []byte("{}")})

	n, err := st.PurgeRuns(ctx, "2025-01-01T00:00:00Z")
	if err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
	if _, ok, _ := st.GetRun(ctx, "old"); ok {
		t.Fatal("old run should be purged")
	}
	if _, ok, _ := st.GetRun(ctx, "new"); !ok {
		t.Fatal("new run must survive")
	}
	if evs, _ := st.ListEvents(ctx, "old"); len(evs) != 0 {
		t.Fatalf("old run's events should be purged, got %d", len(evs))
	}
}
