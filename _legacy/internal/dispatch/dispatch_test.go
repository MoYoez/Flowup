package dispatch_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/dispatch"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/faketest"
	"github.com/moyoez/flowup/internal/pipelines"
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

// capability routing + lease/visibility-timeout reclaim + exactly-once.
// A worker without the required cap can't claim; a dead worker's lease is
// reclaimed; the reclaimed worker's late result is rejected (exactly-once).
func TestCapsRoutingLeaseReclaimExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	task := contracts.NodeTask{RunID: "r1", NodeID: "build", Attempt: 1, RequiresCaps: []string{"os"}}
	payload, _ := sonic.Marshal(task)
	id := dispatch.TaskID(task)
	if err := st.EnqueueTask(ctx, store.TaskRecord{
		TaskID: id, RunID: "r1", NodeID: "build", Attempt: 1,
		Caps: "os", Payload: payload, Status: store.TaskQueued,
	}); err != nil {
		t.Fatal(err)
	}

	// Capability routing: a worker without "os" cannot claim an os-task.
	if _, ok, err := st.ClaimTask(ctx, "no-os", []string{"x11"}, 1000); err != nil || ok {
		t.Fatalf("worker without os must not claim os task: ok=%v err=%v", ok, err)
	}

	// Worker A (has os) claims with a short lease, then "dies" (never completes).
	recA, ok, err := st.ClaimTask(ctx, "A", []string{"os"}, 10)
	if err != nil || !ok {
		t.Fatalf("A claim: ok=%v err=%v", ok, err)
	}

	// Lease expires → reaper requeues exactly one.
	time.Sleep(40 * time.Millisecond)
	n, err := st.ReclaimExpiredTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 reclaimed task, got %d", n)
	}

	// Worker B claims the requeued task and completes it.
	recB, ok, err := st.ClaimTask(ctx, "B", []string{"os"}, 1000)
	if err != nil || !ok {
		t.Fatalf("B claim: ok=%v err=%v", ok, err)
	}
	resB, _ := sonic.Marshal(contracts.NodeResult{NodeID: "build", Status: contracts.StatusOK, Output: []byte(`{"by":"B"}`)})
	accepted, err := st.CompleteTask(ctx, recB.TaskID, "B", store.TaskDone, resB)
	if err != nil || !accepted {
		t.Fatalf("B complete: accepted=%v err=%v", accepted, err)
	}

	// Worker A comes back late — its completion is REJECTED (exactly-once).
	resA, _ := sonic.Marshal(contracts.NodeResult{NodeID: "build", Status: contracts.StatusOK, Output: []byte(`{"by":"A"}`)})
	accepted, err = st.CompleteTask(ctx, recA.TaskID, "A", store.TaskDone, resA)
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("reclaimed worker A's late completion must be rejected")
	}

	// Exactly one result recorded, and it is B's.
	got, _, _ := st.GetTask(ctx, id)
	if got.Status != store.TaskDone {
		t.Fatalf("task status = %q, want done", got.Status)
	}
	var res contracts.NodeResult
	_ = sonic.Unmarshal(got.Result, &res)
	if string(res.Output) != `{"by":"B"}` {
		t.Fatalf("recorded result = %s, want B's", res.Output)
	}
}

// Backpressure: with no matching worker, a claim returns nothing (the task waits).
func TestBackpressureNoMatchingWorker(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	task := contracts.NodeTask{RunID: "r1", NodeID: "build", Attempt: 1, RequiresCaps: []string{"gpu"}}
	payload, _ := sonic.Marshal(task)
	_ = st.EnqueueTask(ctx, store.TaskRecord{
		TaskID: dispatch.TaskID(task), RunID: "r1", NodeID: "build", Attempt: 1,
		Caps: "gpu", Payload: payload, Status: store.TaskQueued,
	})
	if _, ok, err := st.ClaimTask(ctx, "cpu-only", []string{"os"}, 1000); err != nil || ok {
		t.Fatalf("no gpu worker should claim: ok=%v err=%v", ok, err)
	}
}

// End-to-end: the engine runs a full pipeline through the durable queue and a
// pull-based Worker, unaware that dispatch is now out-of-process-shaped.
func TestQueueDispatchEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st := newStore(t)

	mock := faketest.New()
	mock.SetHandler("build", faketest.DemoBuild())
	mock.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	mock.SetHandler("verify", faketest.DemoVerify())

	wkr := dispatch.NewWorker("w1", []string{"os"}, st, mock)
	go func() { _ = wkr.Run(ctx) }()

	eng := engine.New(durable.NewSelfBuilt(st), dispatch.NewQueueDispatcher(st), st, nil)
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	eng.Register(p)

	params, _ := sonic.Marshal(map[string]any{"repo_url": "https://github.com/acme/app", "target_env": "staging"})
	res, err := eng.Invoke(ctx, contracts.Invocation{
		PipelineID:     "deploy_repo_to_paas",
		Params:         params,
		IdempotencyKey: "q-e2e",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok via queue dispatch, got %s reason=%s", res.Status, res.Reason)
	}
}
