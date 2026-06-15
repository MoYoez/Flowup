package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/moyoez/flowup/internal/store"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewSQLite(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateRunDedupsByIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	created, _, err := st.CreateRun(ctx, store.RunRecord{
		RunID: "r1", PipelineID: "p", IdempotencyKey: "key", Status: store.RunPending,
	})
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	created2, existing, err := st.CreateRun(ctx, store.RunRecord{
		RunID: "r2", PipelineID: "p", IdempotencyKey: "key", Status: store.RunPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("duplicate idempotency key should not create a new run")
	}
	if existing.RunID != "r1" {
		t.Fatalf("want existing run r1, got %s", existing.RunID)
	}
}

func TestStepIsWriteOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	if err := st.PutStep(ctx, store.StepRecord{RunID: "r", StepKey: "n", Output: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	// Second write for the same (run, step) must be ignored — this is what makes
	// replay deterministic (recorded output never changes).
	if err := st.PutStep(ctx, store.StepRecord{RunID: "r", StepKey: "n", Output: []byte(`{"a":2}`)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetStep(ctx, "r", "n")
	if err != nil || !ok {
		t.Fatalf("get step: ok=%v err=%v", ok, err)
	}
	if string(got.Output) != `{"a":1}` {
		t.Fatalf("want write-once first value, got %s", got.Output)
	}
}

func TestSideEffectClaimedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	claimed, _, err := st.ClaimSideEffect(ctx, "k")
	if err != nil || !claimed {
		t.Fatalf("first claim should win: claimed=%v err=%v", claimed, err)
	}
	if err := st.FinishSideEffect(ctx, "k", []byte(`{"deployment_id":"dep-1"}`)); err != nil {
		t.Fatal(err)
	}
	claimed2, existing, err := st.ClaimSideEffect(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if claimed2 {
		t.Fatal("second claim must lose (exactly-once)")
	}
	if string(existing) != `{"deployment_id":"dep-1"}` {
		t.Fatalf("want recorded result on second claim, got %s", existing)
	}
}

func TestEventsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	for i := 1; i <= 3; i++ {
		if err := st.AppendEvent(ctx, store.EventRecord{
			RunID: "r", NodeID: "build", Attempt: i, Type: "node_started", Data: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := st.ListEvents(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	if evs[0].Attempt != 1 || evs[2].Attempt != 3 {
		t.Fatalf("events should be ordered by insertion: %+v", evs)
	}
}

func TestHumanInputRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	if _, ok, _ := st.GetHumanInput(ctx, "r", "deploy"); ok {
		t.Fatal("no human input expected yet")
	}
	if err := st.PutHumanInput(ctx, "r", "deploy", []byte(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	data, ok, err := st.GetHumanInput(ctx, "r", "deploy")
	if err != nil || !ok {
		t.Fatalf("get human input: ok=%v err=%v", ok, err)
	}
	if string(data) != `{"approved":true}` {
		t.Fatalf("want stored human input, got %s", data)
	}
}
