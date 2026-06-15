package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/faketest"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/trace"
)

// retry, then slide to the backup model.
func TestRetryThenSlideToBackupModel(t *testing.T) {
	ctx := context.Background()
	st, eng, w := stack(t)

	w.SetHandler("build", faketest.DemoBuild(1)) // attempt 1 fails (retriable), attempt 2 ok
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv("retry"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok, got %s reason=%s", res.Status, res.Reason)
	}
	if w.Calls("build") != 2 {
		t.Fatalf("want build dispatched twice, got %d", w.Calls("build"))
	}
	if got := modelOnAttempt(t, st, res.RunID, "build", 1); got != "claude-opus-4-6" {
		t.Fatalf("attempt 1 should use primary model, got %q", got)
	}
	if got := modelOnAttempt(t, st, res.RunID, "build", 2); got != "claude-sonnet-4-6" {
		t.Fatalf("attempt 2 should slide to backup model, got %q", got)
	}
}

// needs_human suspend, then resume by token.
func TestNeedsHumanThenResume(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)

	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(true, nil)) // needs human until resumed
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv("needs-human"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusNeedsHuman {
		t.Fatalf("want needs_human, got %s", res.Status)
	}
	if res.Handoff == nil || res.Handoff.ResumeToken == "" {
		t.Fatal("want a handoff with a resume token")
	}

	human, _ := sonic.Marshal(map[string]any{"approved": true})
	res2, err := eng.Resume(ctx, res.Handoff.ResumeToken, human)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != contracts.StatusOK {
		t.Fatalf("want ok after resume, got %s reason=%s", res2.Status, res2.Reason)
	}
	if res2.RunID != res.RunID {
		t.Fatalf("resume should continue the same run: %s vs %s", res.RunID, res2.RunID)
	}
}

// crash recovery replays committed steps instead of re-running them.
// The crash is simulated by panicking inside deploy AFTER build has committed,
// which leaves the run PENDING; a fresh executor then Recover()s it.
func TestCrashRecoveryReplaysNotReRun(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "t.db")
	st, err := store.NewSQLite(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	w := faketest.New()
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	// eng1 crashes SYNCHRONOUSLY when dispatching deploy (in the drive goroutine,
	// so the test can recover it) — after build has already committed. This leaves
	// the run PENDING, exactly as a real kill -9 would.
	eng1 := engine.New(durable.NewSelfBuilt(st), crashDispatcher{inner: w, crashOn: "deploy"}, st, nil)
	eng1.Register(mustBuiltin(t))

	func() {
		defer func() { _ = recover() }() // absorb the simulated crash
		_, _ = eng1.Invoke(ctx, inv("crash"))
	}()

	pend, err := st.ListPendingRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 {
		t.Fatalf("want 1 pending (crashed) run, got %d", len(pend))
	}
	runID := pend[0].RunID

	buildBefore, ok, _ := st.GetStep(ctx, runID, "build")
	if !ok {
		t.Fatal("build step must be committed before the crash")
	}
	buildCalls := w.Calls("build")

	// Fresh executor over the SAME store (models a new process with no in-memory
	// state) recovers the run; deploy now succeeds.
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	eng2 := engine.New(durable.NewSelfBuilt(st), w, st, nil)
	eng2.Register(mustBuiltin(t))

	n, err := eng2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 recovered run, got %d", n)
	}

	run, _, _ := st.GetRun(ctx, runID)
	if run.Status != store.RunOK {
		t.Fatalf("want ok after recovery, got %s reason=%s", run.Status, run.Reason)
	}
	if w.Calls("build") != buildCalls {
		t.Fatalf("build was RE-RUN on recovery (calls %d → %d); it must be replayed", buildCalls, w.Calls("build"))
	}
	buildAfter, _, _ := st.GetStep(ctx, runID, "build")
	if string(buildBefore.Output) != string(buildAfter.Output) {
		t.Fatalf("replayed build output changed: %s vs %s", buildBefore.Output, buildAfter.Output)
	}
}

// replay under the same idempotency key produces no duplicate side effects.
func TestIdempotentReplayNoDuplicateSideEffect(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)

	var deploys int32
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, &deploys))
	w.SetHandler("verify", faketest.DemoVerify())

	res1, err := eng.Invoke(ctx, inv("same-key"))
	if err != nil {
		t.Fatal(err)
	}
	res2, err := eng.Invoke(ctx, inv("same-key")) // identical idempotency key
	if err != nil {
		t.Fatal(err)
	}
	if res1.RunID != res2.RunID {
		t.Fatalf("same idempotency key must map to the same run: %s vs %s", res1.RunID, res2.RunID)
	}
	if res1.Status != contracts.StatusOK || res2.Status != contracts.StatusOK {
		t.Fatalf("both calls should be ok, got %s / %s", res1.Status, res2.Status)
	}
	if deploys != 1 {
		t.Fatalf("deploy side effect must run exactly once, ran %d times", deploys)
	}
}

// The fault boundary returns a classified failed reason, never a raw error.
func TestFailedIsClassifiedThreeState(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)

	w.SetHandler("build", faketest.DemoBuild(1, 2, 3)) // fail all attempts
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv("fail"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusFailed {
		t.Fatalf("want failed, got %s", res.Status)
	}
	if res.Reason == "" {
		t.Fatal("failed result must carry a classified reason")
	}
	if w.Calls("build") != 3 {
		t.Fatalf("want 3 attempts (max_attempts), got %d", w.Calls("build"))
	}
}

// Params that violate the input_schema are rejected at the boundary as failed.
func TestInvalidParamsRejected(t *testing.T) {
	ctx := context.Background()
	_, eng, _ := stack(t)

	params, _ := sonic.Marshal(map[string]any{
		"repo_url":   "https://github.com/acme/app",
		"target_env": "bogus", // not in enum [staging, prod]
	})
	res, err := eng.Invoke(ctx, contracts.Invocation{
		PipelineID:     "deploy_repo_to_paas",
		Params:         params,
		IdempotencyKey: "bad-params",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusFailed || res.Reason != "invalid_params" {
		t.Fatalf("want failed/invalid_params, got %s/%s", res.Status, res.Reason)
	}
}

// a raw interface error/stack from a node never crosses the boundary; the
// caller sees only a classified three-state reason.
func TestRawErrorNeverLeaksAcrossBoundary(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)

	raw := "panic: runtime error: invalid memory address\n\tgoroutine 1 [running]:\n\tmain.deploy(0x0)\n"
	w.SetHandler("build", func(contracts.NodeTask) contracts.NodeResult {
		return faketest.Failed(raw, false) // a raw, non-retriable error from the node
	})
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv("raw-error"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusFailed {
		t.Fatalf("want failed, got %s", res.Status)
	}
	for _, leak := range []string{"panic", "goroutine", "0x", "\n", "\t", "invalid memory"} {
		if strings.Contains(res.Reason, leak) {
			t.Fatalf("raw error leaked across the boundary: reason=%q contains %q", res.Reason, leak)
		}
	}
	if res.Reason != "build:"+contracts.ReasonNodeError {
		t.Fatalf("want classified build:node_error, got %q", res.Reason)
	}
}

// secrets in a node's output are redacted in the event log, but
// the operational output (operation_outputs) stays raw for downstream resolution.
func TestEventLogRedactedButOutputsRaw(t *testing.T) {
	ctx := context.Background()
	st, eng, w := stack(t)
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", func(contracts.NodeTask) contracts.NodeResult {
		return faketest.OK(map[string]any{"healthy": true, "status_code": 200, "api_key": "sk-SECRET-XYZ"})
	})

	res, err := eng.Invoke(ctx, inv("redact"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok, got %s reason=%s", res.Status, res.Reason)
	}

	evs, _ := st.ListEvents(ctx, res.RunID)
	sawVerifyOK := false
	for _, e := range evs {
		if e.NodeID == "verify" && e.Type == "node_ok" {
			sawVerifyOK = true
			if strings.Contains(string(e.Data), "sk-SECRET-XYZ") {
				t.Fatalf("secret leaked into the event log: %s", e.Data)
			}
			if !strings.Contains(string(e.Data), trace.RedactMask) {
				t.Fatalf("expected a redaction mask in the event: %s", e.Data)
			}
		}
	}
	if !sawVerifyOK {
		t.Fatal("no verify node_ok event found")
	}

	step, ok, _ := st.GetStep(ctx, res.RunID, "verify")
	if !ok || !strings.Contains(string(step.Output), "sk-SECRET-XYZ") {
		t.Fatalf("operational output must keep the raw value (for resolution/replay), got ok=%v %s", ok, step.Output)
	}
}

// An authorizer gates Invoke by caller: denied callers never run the pipeline.
func TestAuthorizerGate(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())
	eng.SetAuthorizer(func(c contracts.Caller, _ string) error {
		if c.Host == "evil" {
			return errors.New("denied")
		}
		return nil
	})

	params, _ := sonic.Marshal(map[string]any{"repo_url": "https://github.com/acme/app", "target_env": "staging"})
	denied, _ := eng.Invoke(ctx, contracts.Invocation{
		PipelineID: "deploy_repo_to_paas", Params: params, IdempotencyKey: "auth-deny",
		Caller: contracts.Caller{Host: "evil"},
	})
	if denied.Status != contracts.StatusFailed || denied.Reason != contracts.ReasonUnauthorized {
		t.Fatalf("want failed/unauthorized, got %s/%s", denied.Status, denied.Reason)
	}

	allowed, _ := eng.Invoke(ctx, contracts.Invocation{
		PipelineID: "deploy_repo_to_paas", Params: params, IdempotencyKey: "auth-allow",
		Caller: contracts.Caller{Host: "openclaw"},
	})
	if allowed.Status != contracts.StatusOK {
		t.Fatalf("want ok for allowed caller, got %s/%s", allowed.Status, allowed.Reason)
	}
}

// helpers

func stack(t *testing.T) (store.Store, *engine.Engine, *faketest.Fake) {
	t.Helper()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "t.db")
	st, err := store.NewSQLite(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := faketest.New()
	eng := engine.New(durable.NewSelfBuilt(st), w, st, nil)
	eng.Register(mustBuiltin(t))
	return st, eng, w
}

func mustBuiltin(t *testing.T) *pipelines.Pipeline {
	t.Helper()
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func inv(key string) contracts.Invocation {
	params, _ := sonic.Marshal(map[string]any{
		"repo_url":   "https://github.com/acme/app",
		"target_env": "staging",
	})
	return contracts.Invocation{
		PipelineID:     "deploy_repo_to_paas",
		Params:         params,
		IdempotencyKey: key,
		Caller:         contracts.Caller{Host: "test"},
	}
}

// crashDispatcher delegates to inner but panics synchronously on crashOn — a
// kill -9 mid-run inside the engine's goroutine, recoverable by the test.
type crashDispatcher struct {
	inner   engine.Dispatcher
	crashOn string
}

func (c crashDispatcher) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	if task.NodeID == c.crashOn {
		panic("simulated kill -9")
	}
	return c.inner.Dispatch(ctx, task)
}

func modelOnAttempt(t *testing.T, st store.Store, runID, nodeID string, attempt int) string {
	t.Helper()
	evs, err := st.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.NodeID == nodeID && e.Type == "node_started" && e.Attempt == attempt {
			var d map[string]any
			_ = sonic.Unmarshal(e.Data, &d)
			s, _ := d["model"].(string)
			return s
		}
	}
	return ""
}

// limits.timeout_sec is enforced: an overrunning attempt becomes a retriable
// timeout that folds straight into retry + model-slide.
func TestNodeTimeoutFoldsIntoRetry(t *testing.T) {
	ctx := context.Background()
	_, eng, w := stack(t)

	const y = `pipeline: timeout_demo
version: 1
input_schema: {type: object}
nodes:
  - id: build
    kind: agent
    needs: []
    profile: {models: [m1, m2]}
    inputs: {}
    limits: {max_steps: 1, budget_usd: 0, timeout_sec: 1}
    retry: {max_attempts: 2, on: [retriable]}
    output_schema:
      type: object
      required: [ok]
      properties: {ok: {type: boolean}}
output: {done: "{{ build.ok }}"}
`
	p, err := pipelines.LoadBytes([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	eng.Register(p)

	w.SetHandler("build", func(task contracts.NodeTask) contracts.NodeResult {
		if task.Attempt == 1 {
			time.Sleep(1500 * time.Millisecond) // overruns the 1s timeout
		}
		return faketest.OK(map[string]any{"ok": true})
	})

	res, err := eng.Invoke(ctx, contracts.Invocation{
		PipelineID:     "timeout_demo",
		Params:         []byte(`{}`),
		IdempotencyKey: "timeout-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok after timeout→retry, got %s reason=%s", res.Status, res.Reason)
	}
	if w.Calls("build") != 2 {
		t.Fatalf("want 2 attempts (timeout then ok), got %d", w.Calls("build"))
	}
}

// Single-node replay re-runs a node from recorded inputs without polluting the
// original run's events, recorded outputs, or side effects.
func TestReplayNodeDoesNotPolluteRun(t *testing.T) {
	ctx := context.Background()
	st, eng, w := stack(t)
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv("replay"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("setup run not ok: %s", res.Status)
	}

	eventsBefore, _ := st.ListEvents(ctx, res.RunID)
	verifyBefore, _, _ := st.GetStep(ctx, res.RunID, "verify")
	deployCalls := w.Calls("deploy")

	rep, err := eng.ReplayNode(ctx, res.RunID, "verify")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != contracts.StatusOK {
		t.Fatalf("replay verify want ok, got %s reason=%s", rep.Status, rep.Reason)
	}

	eventsAfter, _ := st.ListEvents(ctx, res.RunID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("replay polluted the event log: %d → %d", len(eventsBefore), len(eventsAfter))
	}
	verifyAfter, _, _ := st.GetStep(ctx, res.RunID, "verify")
	if string(verifyAfter.Output) != string(verifyBefore.Output) {
		t.Fatalf("replay changed the recorded verify output")
	}
	if w.Calls("deploy") != deployCalls {
		t.Fatalf("replay re-dispatched the side-effect node deploy: %d → %d", deployCalls, w.Calls("deploy"))
	}
}
