package runner_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/runner"
	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/tools"
)

func newEngine(t *testing.T, disp engine.Dispatcher, p *pipelines.Pipeline) (*engine.Engine, store.Store) {
	t.Helper()
	st, err := store.NewSQLite(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := engine.New(durable.NewSelfBuilt(st), disp, st, nil)
	eng.Register(p)
	return eng, st
}

func mustPipeline(t *testing.T, y string) *pipelines.Pipeline {
	t.Helper()
	p, err := pipelines.LoadBytes([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Router runs a native node (real command) end-to-end through the engine.
func TestRouterNativeNode(t *testing.T) {
	var argv string
	if runtime.GOOS == "windows" {
		argv = `['cmd','/c','echo routed']`
	} else {
		argv = `['sh','-c','echo routed']`
	}
	p := mustPipeline(t, `pipeline: nat
version: 1
input_schema: { type: object }
nodes:
  - id: run
    kind: native
    profile: { models: [native] }
    inputs: { argv: `+argv+` }
    limits: { timeout_sec: 30 }
output: { said: "{{ run.stdout }}" }
`)
	rt := runner.NewRouter(sandbox.NewSubprocess(), nil, nil)
	eng, _ := newEngine(t, rt, p)
	res, err := eng.Invoke(context.Background(), contracts.Invocation{PipelineID: "nat", Params: []byte(`{}`), IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK || !strings.Contains(string(res.Output), "routed") {
		t.Fatalf("want ok+routed, got %s/%s out=%s", res.Status, res.Reason, res.Output)
	}
}

// Router runs a semantic node (model + tool loop) end-to-end through the engine.
func TestRouterSemanticNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	sc := model.NewScripted(
		model.Response{ToolCalls: []model.ToolCall{{ID: "t", Name: "http_get", Args: json.RawMessage(`{"url":"` + srv.URL + `"}`)}}},
		model.Response{Text: `{"healthy":true}`},
	)
	reg := tools.NewRegistry(tools.NewHTTPGet())
	rt := runner.NewRouter(sandbox.NewSubprocess(), sc, reg)

	p := mustPipeline(t, `pipeline: aichk
version: 1
input_schema: { type: object }
nodes:
  - id: check
    kind: semantic
    profile: { models: [m], tools: [http_get] }
    inputs: { url: "{{ params.url }}" }
    limits: { max_steps: 5, timeout_sec: 30 }
    output_schema: { type: object, required: [healthy], properties: { healthy: {type: boolean} } }
output: { healthy: "{{ check.healthy }}" }
`)
	eng, _ := newEngine(t, rt, p)
	params, _ := sonic.Marshal(map[string]any{"url": srv.URL})
	res, err := eng.Invoke(context.Background(), contracts.Invocation{PipelineID: "aichk", Params: params, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK || !strings.Contains(string(res.Output), "true") {
		t.Fatalf("want ok+healthy, got %s/%s out=%s", res.Status, res.Reason, res.Output)
	}
}

// Without a model configured, a semantic node fails with a classified reason.
func TestRouterNoModel(t *testing.T) {
	rt := runner.NewRouter(sandbox.NewSubprocess(), nil, nil)
	p := mustPipeline(t, `pipeline: nomodel
version: 1
input_schema: { type: object }
nodes:
  - id: think
    kind: semantic
    profile: { models: [m] }
    inputs: {}
    limits: {}
output: {}
`)
	eng, _ := newEngine(t, rt, p)
	res, _ := eng.Invoke(context.Background(), contracts.Invocation{PipelineID: "nomodel", Params: []byte(`{}`), IdempotencyKey: "k"})
	if res.Status != contracts.StatusFailed || !strings.Contains(res.Reason, "model_runner_not_configured") {
		t.Fatalf("want failed/model_runner_not_configured, got %s/%s", res.Status, res.Reason)
	}
}
