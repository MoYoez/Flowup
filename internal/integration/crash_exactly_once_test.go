// Package integration holds real end-to-end tests: real processes, real HTTP,
// real crash and recovery — no mock worker, no scripted model.
package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
)

const childEnv = "FLOWUP_CRASH_CHILD"

// effectPipeline: a prep node, then a side-effect node that calls a real external
// service. Schemaless to keep the test focused on the durability path.
const effectPipeline = `pipeline: effect_once
version: 1
input_schema: { type: object }
nodes:
  - id: prep
    kind: native
    profile: { models: [m] }
    inputs: {}
    limits: { timeout_sec: 30 }
  - id: deliver
    kind: native
    needs: [prep]
    side_effect: true
    idempotency_key: "effect-{{ params.id }}"
    profile: { models: [m] }
    inputs: {}
    limits: { timeout_sec: 30 }
output: {}
`

// realDispatcher runs the deliver node as a real HTTP POST to an external service,
// keyed for idempotency by the run id. With crashOnDeliver it hard-exits the
// process right after the POST — a kill -9 mid-side-effect.
type realDispatcher struct {
	sinkURL        string
	crashOnDeliver bool
}

func (d realDispatcher) Dispatch(_ context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	ok := contracts.NodeResult{RunID: task.RunID, NodeID: task.NodeID, Status: contracts.StatusOK, Output: []byte("{}")}
	if task.NodeID != "deliver" {
		return ok, nil
	}
	req, _ := http.NewRequest(http.MethodPost, d.sinkURL+"/effect", nil)
	req.Header.Set("Idempotency-Key", task.RunID)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	if d.crashOnDeliver {
		os.Exit(137) // the external effect fired; die before the engine commits it
	}
	return ok, nil
}

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		runCrashChild()
		return
	}
	os.Exit(m.Run())
}

// runCrashChild drives the run with a dispatcher that hard-exits mid-side-effect.
func runCrashChild() {
	ctx := context.Background()
	st, err := store.NewSQLite(ctx, os.Getenv("FLOWUP_DB"))
	if err != nil {
		os.Exit(2)
	}
	eng := engine.New(durable.NewSelfBuilt(st), realDispatcher{sinkURL: os.Getenv("FLOWUP_SINK"), crashOnDeliver: true}, st, nil)
	p, err := pipelines.LoadBytes([]byte(effectPipeline))
	if err != nil {
		os.Exit(2)
	}
	eng.Register(p)
	params, _ := sonic.Marshal(map[string]any{"id": "1"})
	_, _ = eng.Invoke(ctx, contracts.Invocation{PipelineID: "effect_once", Params: params, IdempotencyKey: "run-1"})
	os.Exit(3) // unreachable: the dispatcher os.Exit(137)s first
}

// A real side effect fires exactly once even across a real process crash: the
// engine re-delivers on recovery (at-least-once), and the external service dedups
// by the idempotency key (exactly-once effect).
func TestExactlyOnceUnderRealCrash(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	applied := map[string]bool{}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		applied[r.Header.Get("Idempotency-Key")] = true // idempotent: set, never increment
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	db := filepath.Join(t.TempDir(), "crash.db")

	// Child process: drives the run until it hard-exits mid-side-effect.
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), childEnv+"=1", "FLOWUP_SINK="+sink.URL, "FLOWUP_DB="+db)
	if err := cmd.Run(); err == nil {
		t.Fatal("child should have crashed (non-zero exit), but it exited cleanly")
	}

	mu.Lock()
	preCrash := calls
	mu.Unlock()
	if preCrash != 1 {
		t.Fatalf("the side effect should have fired once before the crash, got %d", preCrash)
	}

	// Parent: a fresh engine over the SAME store recovers the pending run. The
	// deliver node re-dispatches (the claim was never finished), so the external
	// service is called a second time — and dedups it.
	ctx := context.Background()
	st, err := store.NewSQLite(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := engine.New(durable.NewSelfBuilt(st), realDispatcher{sinkURL: sink.URL}, st, nil)
	p, _ := pipelines.LoadBytes([]byte(effectPipeline))
	eng.Register(p)

	n, err := eng.Recover(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover: n=%d err=%v (want 1 recovered run)", n, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("want 2 total deliveries (1 pre-crash + 1 on recovery), got %d", calls)
	}
	if len(applied) != 1 {
		t.Fatalf("the external effect must be applied exactly once, got %d", len(applied))
	}
}
