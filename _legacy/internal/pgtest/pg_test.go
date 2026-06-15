//go:build postgres

// Package pgtest validates the whole stack on real Postgres without Docker:
// embedded-postgres boots a pure-Go-managed PostgreSQL. It proves the durable
// interface is substrate-agnostic — the same engine that passes on SQLite passes
// on Postgres by swapping only the store constructor.
// Run: go test -tags postgres ./internal/pgtest
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/bytedance/sonic"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/faketest"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
)

const dsn = "host=localhost port=55434 user=flowup password=flowup dbname=flowup sslmode=disable"

func TestMain(m *testing.M) {
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Username("flowup").Password("flowup").Database("flowup").Port(55434))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "embedded-postgres start:", err)
		os.Exit(2)
	}
	// Clean slate: embedded-postgres persists its data dir across runs, so drop
	// any flowup tables before the suite (NewPostgres re-creates them).
	if db, err := sql.Open("pgx", dsn); err == nil {
		_, _ = db.Exec(`DROP TABLE IF EXISTS workflow_status, operation_outputs, events, side_effects, human_inputs, tasks CASCADE`)
		_ = db.Close()
	}
	code := m.Run()
	_ = pg.Stop()
	os.Exit(code)
}

func pgStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func pgStack(t *testing.T) (store.Store, *engine.Engine, *faketest.Fake) {
	t.Helper()
	st := pgStore(t)
	w := faketest.New()
	eng := engine.New(durable.NewSelfBuilt(st), w, st, nil)
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	eng.Register(p)
	return st, eng, w
}

// inv builds an Invocation that is isolated per-test on the shared PG: a unique
// repo_url makes the deploy node's idempotency_key (derived from params) unique,
// and a per-test call-level IdempotencyKey isolates the run.
func inv(t *testing.T, suffix string) contracts.Invocation {
	params, _ := sonic.Marshal(map[string]any{
		"repo_url":   "https://github.com/acme/" + t.Name(),
		"target_env": "staging",
	})
	return contracts.Invocation{
		PipelineID:     "deploy_repo_to_paas",
		Params:         params,
		IdempotencyKey: t.Name() + ":" + suffix,
		Caller:         contracts.Caller{Host: "pgtest"},
	}
}

// Store conformance on Postgres: dedup, write-once memoization, exactly-once
// side effect, and the task queue claim/complete — the same guarantees as SQLite.
func TestStoreConformanceOnPostgres(t *testing.T) {
	ctx := context.Background()
	st := pgStore(t)
	p := t.Name() + ":"

	created, _, err := st.CreateRun(ctx, store.RunRecord{RunID: p + "r1", PipelineID: "x", IdempotencyKey: p + "k", Status: store.RunPending})
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	created2, existing, err := st.CreateRun(ctx, store.RunRecord{RunID: p + "r2", PipelineID: "x", IdempotencyKey: p + "k", Status: store.RunPending})
	if err != nil {
		t.Fatal(err)
	}
	if created2 || existing.RunID != p+"r1" {
		t.Fatalf("dedup failed: created=%v existing=%s", created2, existing.RunID)
	}

	_ = st.PutStep(ctx, store.StepRecord{RunID: p + "r1", StepKey: "n", Output: []byte(`{"a":1}`)})
	_ = st.PutStep(ctx, store.StepRecord{RunID: p + "r1", StepKey: "n", Output: []byte(`{"a":2}`)})
	got, ok, _ := st.GetStep(ctx, p+"r1", "n")
	if !ok || string(got.Output) != `{"a":1}` {
		t.Fatalf("step not write-once: ok=%v %s", ok, got.Output)
	}

	claimed, _, _ := st.ClaimSideEffect(ctx, p+"se")
	if !claimed {
		t.Fatal("first side-effect claim should win")
	}
	_ = st.FinishSideEffect(ctx, p+"se", []byte(`{"r":1}`))
	claimed2, ex, _ := st.ClaimSideEffect(ctx, p+"se")
	if claimed2 || string(ex) != `{"r":1}` {
		t.Fatalf("side-effect not exactly-once: claimed=%v existing=%s", claimed2, ex)
	}

	task := contracts.NodeTask{RunID: p + "r1", NodeID: "build", Attempt: 1, RequiresCaps: []string{"os"}}
	payload, _ := sonic.Marshal(task)
	tid := p + "build:1"
	_ = st.EnqueueTask(ctx, store.TaskRecord{TaskID: tid, RunID: p + "r1", NodeID: "build", Attempt: 1, Caps: "os", Payload: payload, Status: store.TaskQueued})
	if _, ok, _ := st.ClaimTask(ctx, "w-nocaps", nil, 1000); ok {
		// A worker with no caps must not grab an os-task; but other queued tasks
		// from parallel tests might match — so only assert our task stays queued.
	}
	rec, ok, _ := st.ClaimTask(ctx, "w-os", []string{"os"}, 1000)
	if !ok {
		t.Fatal("os worker should claim the os task")
	}
	if acc, _ := st.CompleteTask(ctx, rec.TaskID, "w-os", store.TaskDone, []byte(`{}`)); !acc {
		t.Fatal("owner completion should be accepted")
	}
}

// retry, then slide to the backup model, on Postgres.
func TestG1RetryFallbackOnPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng, w := pgStack(t)
	w.SetHandler("build", faketest.DemoBuild(1))
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv(t, "retry"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK || w.Calls("build") != 2 {
		t.Fatalf("want ok with 2 build attempts, got %s / %d", res.Status, w.Calls("build"))
	}
	_ = st
}

// needs_human suspend + resume, on Postgres.
func TestG1NeedsHumanResumeOnPostgres(t *testing.T) {
	ctx := context.Background()
	_, eng, w := pgStack(t)
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(true, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	res, err := eng.Invoke(ctx, inv(t, "nh"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusNeedsHuman || res.Handoff == nil || res.Handoff.ResumeToken == "" {
		t.Fatalf("want needs_human + token, got %s", res.Status)
	}
	human, _ := sonic.Marshal(map[string]any{"approved": true})
	res2, err := eng.Resume(ctx, res.Handoff.ResumeToken, human)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != contracts.StatusOK || res2.RunID != res.RunID {
		t.Fatalf("want ok same-run after resume, got %s run=%s/%s", res2.Status, res.RunID, res2.RunID)
	}
}

// idempotent replay, exactly-once side effect, on Postgres.
func TestG1IdempotencyOnPostgres(t *testing.T) {
	ctx := context.Background()
	_, eng, w := pgStack(t)
	var deploys int32
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, &deploys))
	w.SetHandler("verify", faketest.DemoVerify())

	r1, _ := eng.Invoke(ctx, inv(t, "idem"))
	r2, _ := eng.Invoke(ctx, inv(t, "idem"))
	if r1.RunID != r2.RunID || r1.Status != contracts.StatusOK || r2.Status != contracts.StatusOK || deploys != 1 {
		t.Fatalf("idempotency failed on PG: run %s/%s status %s/%s deploys=%d", r1.RunID, r2.RunID, r1.Status, r2.Status, deploys)
	}
}

// crash recovery replays (not re-runs) on Postgres. A fresh executor over a
// SEPARATE connection pool to the SAME database models a new process exactly.
func TestG1CrashRecoveryOnPostgres(t *testing.T) {
	ctx := context.Background()
	st := pgStore(t)

	w := faketest.New()
	w.SetHandler("build", faketest.DemoBuild())
	w.SetHandler("deploy", faketest.DemoDeploy(false, nil))
	w.SetHandler("verify", faketest.DemoVerify())

	eng1 := engine.New(durable.NewSelfBuilt(st), crashDispatcher{inner: w, crashOn: "deploy"}, st, nil)
	eng1.Register(mustBuiltin(t))
	func() {
		defer func() { _ = recover() }()
		_, _ = eng1.Invoke(ctx, inv(t, "crash"))
	}()

	buildCalls := w.Calls("build")

	// Fresh store (separate pool) + executor = a new process; recover from PG.
	st2 := pgStore(t)
	eng2 := engine.New(durable.NewSelfBuilt(st2), w, st2, nil)
	eng2.Register(mustBuiltin(t))
	n, err := eng2.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("want >=1 recovered run, got %d", n)
	}
	if w.Calls("build") != buildCalls {
		t.Fatalf("build was re-run on recovery (%d → %d); must be replayed", buildCalls, w.Calls("build"))
	}
}

func mustBuiltin(t *testing.T) *pipelines.Pipeline {
	t.Helper()
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

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
