package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/store"
)

// comspec returns the absolute path to cmd.exe (don't rely on PATH, which a
// cygwin-launched test may not carry).
func comspec() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return `C:\Windows\System32\cmd.exe`
}

// echoArgv returns an OS-appropriate command that prints s to stdout.
func echoArgv(s string) []string {
	if runtime.GOOS == "windows" {
		return []string{comspec(), "/c", "echo " + s}
	}
	return []string{"sh", "-c", "printf %s " + s}
}

// sleepArgv returns an OS-appropriate command that blocks for a few seconds.
// On Windows we invoke ping.exe by absolute path (a cygwin-launched test may not
// carry System32 on PATH).
func sleepArgv() []string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return []string{filepath.Join(root, "System32", "ping.exe"), "-n", "6", "127.0.0.1"}
	}
	return []string{"sh", "-c", "sleep 4"}
}

func TestSubprocessEcho(t *testing.T) {
	res, err := sandbox.NewSubprocess().Exec(context.Background(), sandbox.ExecSpec{
		Argv: echoArgv("hello"), TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "hello") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestSubprocessTimeout(t *testing.T) {
	res, err := sandbox.NewSubprocess().Exec(context.Background(), sandbox.ExecSpec{
		Argv: sleepArgv(), TimeoutSec: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("want TimedOut, got %+v", res)
	}
}

func TestRunnerNativeWrapsStdout(t *testing.T) {
	r := sandbox.NewRunner(sandbox.NewSubprocess())
	inputs, _ := sonic.Marshal(map[string]any{"argv": echoArgv("hi")})
	res, err := r.Dispatch(context.Background(), contracts.NodeTask{
		NodeID: "run", Kind: contracts.KindNative, Inputs: inputs,
		Limits: contracts.Limits{TimeoutSec: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok, got %s/%s", res.Status, res.Reason)
	}
	var m map[string]any
	_ = sonic.Unmarshal(res.Output, &m)
	if s, _ := m["stdout"].(string); !strings.Contains(s, "hi") {
		t.Fatalf("stdout not wrapped: %s", res.Output)
	}
}

// stubBackend returns canned output, for deterministic schema-path tests.
type stubBackend struct {
	out  string
	exit int
}

func (stubBackend) Name() string { return "stub" }
func (s stubBackend) Exec(context.Context, sandbox.ExecSpec) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{Stdout: []byte(s.out), ExitCode: s.exit}, nil
}

func TestRunnerSchemaValidation(t *testing.T) {
	argv, _ := sonic.Marshal(map[string]any{"argv": []string{"x"}})
	schema, _ := sonic.Marshal(map[string]any{
		"type": "object", "required": []string{"healthy"},
		"properties": map[string]any{"healthy": map[string]any{"type": "boolean"}},
	})

	// stdout is valid JSON matching the schema → ok with that output.
	okRes, _ := sandbox.NewRunner(stubBackend{out: `{"healthy":true}`}).Dispatch(context.Background(),
		contracts.NodeTask{NodeID: "v", Kind: contracts.KindNative, Inputs: argv, OutputSchema: schema})
	if okRes.Status != contracts.StatusOK || !strings.Contains(string(okRes.Output), "healthy") {
		t.Fatalf("want ok with healthy, got %s/%s out=%s", okRes.Status, okRes.Reason, okRes.Output)
	}

	// stdout is not schema-valid JSON → classified schema violation, never raw.
	badRes, _ := sandbox.NewRunner(stubBackend{out: "not json"}).Dispatch(context.Background(),
		contracts.NodeTask{NodeID: "v", Kind: contracts.KindNative, Inputs: argv, OutputSchema: schema})
	if badRes.Status != contracts.StatusFailed || badRes.Reason != contracts.ReasonSchemaViolation {
		t.Fatalf("want failed/%s, got %s/%s", contracts.ReasonSchemaViolation, badRes.Status, badRes.Reason)
	}
}

func TestRunnerRejectsNonNative(t *testing.T) {
	res, _ := sandbox.NewRunner(sandbox.NewSubprocess()).Dispatch(context.Background(),
		contracts.NodeTask{NodeID: "x", Kind: contracts.KindAgent})
	if res.Status != contracts.StatusFailed || res.Reason != "model_runner_not_configured" {
		t.Fatalf("want failed/model_runner_not_configured, got %s/%s", res.Status, res.Reason)
	}
}

// ContainerBackend runs a real command in a container. Gated on a present image
// (set FLOWUP_SANDBOX_IMAGE to a cached image to run; CI/fresh machines skip).
func TestContainerBackend(t *testing.T) {
	img := os.Getenv("FLOWUP_SANDBOX_IMAGE")
	if img == "" {
		t.Skip("set FLOWUP_SANDBOX_IMAGE=<cached linux image> to run the container backend test")
	}
	engine := os.Getenv("FLOWUP_SANDBOX_ENGINE")
	b := sandbox.NewContainer(engine, img)
	res, err := b.Exec(context.Background(), sandbox.ExecSpec{
		Argv: []string{"sh", "-c", "echo container-ok"}, TimeoutSec: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "container-ok") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// End-to-end: the engine runs a native pipeline through the sandbox Runner, which
// executes a REAL local command — no model, no mock. Proves the local sandbox
// slots behind the engine seam with zero engine changes.
func TestEngineNativeThroughSandbox(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "t.db")
	st, err := store.NewSQLite(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const y = `pipeline: local_check
version: 1
input_schema: { type: object }
nodes:
  - id: run
    kind: native
    needs: []
    requires_caps: [os]
    profile: { models: [native] }
    inputs: { argv: "{{ params.argv }}" }
    limits: { max_steps: 1, budget_usd: 0, timeout_sec: 30 }
output: { said: "{{ run.stdout }}" }
`
	p, err := pipelines.LoadBytes([]byte(y))
	if err != nil {
		t.Fatal(err)
	}

	eng := engine.New(durable.NewSelfBuilt(st), sandbox.NewRunner(sandbox.NewSubprocess()), st, nil)
	eng.Register(p)

	params, _ := sonic.Marshal(map[string]any{"argv": echoArgv("hello-local")})
	res, err := eng.Invoke(ctx, contracts.Invocation{
		PipelineID: "local_check", Params: params, IdempotencyKey: "native-e2e",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok, got %s/%s", res.Status, res.Reason)
	}
	var out map[string]any
	_ = sonic.Unmarshal(res.Output, &out)
	if s, _ := out["said"].(string); !strings.Contains(s, "hello-local") {
		t.Fatalf("pipeline output did not carry real command stdout: %s", res.Output)
	}
}
