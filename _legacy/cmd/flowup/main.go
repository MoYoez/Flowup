// Command flowup is the developer CLI for the pipeline truth source: validate a
// definition, derive its Mermaid view, or run a pipeline end-to-end on the real
// sandbox + model executor.
//
//	flowup validate pipeline.yaml
//	flowup mermaid  pipeline.yaml [-o out.mermaid]
//	flowup run      pipeline.yaml [--params '<json>']
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/mcp"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/runner"
	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/trace"
)

func main() {
	if shutdown, err := trace.InitFromEnv(); err == nil {
		defer func() { _ = shutdown(context.Background()) }()
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "new":
		err = cmdNew(args)
	case "validate":
		err = cmdValidate(args)
	case "mermaid":
		err = cmdMermaid(args)
	case "run":
		err = cmdRun(args)
	case "trace":
		err = cmdTrace(args)
	case "replay":
		err = cmdReplay(args)
	case "invoke":
		err = cmdInvoke(args)
	case "mcp":
		err = cmdMCP(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `flowup — reliable pipeline CLI for agents

  flowup new      <name>                        scaffold a starter pipeline (native node, runnable)
  flowup validate <pipeline.yaml>               validate a definition (DAG / refs / casting)
  flowup mermaid  <pipeline.yaml> [-o file]     derive a Mermaid view from a definition
  flowup run      <pipeline.yaml> [flags]       run end-to-end on the sandbox
                  flags: --db file --params '<json>' --image <img> [--engine podman|docker] --container
  flowup trace    --db file <run_id>            print a run's event log
  flowup replay   --db file --pipelines dir <run_id> <node>   re-run one node from recorded inputs
  flowup invoke   [--db file] [--pipelines dir] < invocation.json   Invocation JSON in, three-state JSON out
  flowup mcp      [--db file] [--pipelines dir]  MCP server (stdio): run_pipeline / list_pipelines

Sample pipelines live in examples/. --db defaults to an in-memory store (discarded after the run).
`)
}

// loadRequired loads a pipeline from path; the path is required.
func loadRequired(path string) (*pipelines.Pipeline, error) {
	if path == "" {
		return nil, fmt.Errorf("specify a pipeline file, e.g. examples/ask.pipeline.yaml")
	}
	return pipelines.Load(path)
}

func firstNonFlag(args []string) string {
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' {
			return a
		}
	}
	return ""
}

func cmdValidate(args []string) error {
	p, err := loadRequired(firstNonFlag(args))
	if err != nil {
		return err
	}
	order, _ := p.TopoOrder()
	fmt.Printf("OK  pipeline=%s version=%d nodes=%d\n", p.Pipeline, p.Version, len(p.Nodes))
	fmt.Printf("    topo order: ")
	for i, n := range order {
		if i > 0 {
			fmt.Print(" → ")
		}
		fmt.Print(n.ID)
	}
	fmt.Println()
	return nil
}

func cmdMermaid(args []string) error {
	var out string
	var path string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--out":
			if i+1 < len(args) {
				out = args[i+1]
				i++
			}
		default:
			if path == "" && args[i][0] != '-' {
				path = args[i]
			}
		}
	}
	p, err := loadRequired(path)
	if err != nil {
		return err
	}
	m := p.Mermaid()
	fmt.Print(m)
	if out != "" {
		if err := os.WriteFile(out, []byte(m), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nwrote %s\n", out)
	}
	return nil
}

func cmdRun(args []string) error {
	ctx := context.Background()
	pos, flags := parseArgs(args, "--db", "--params", "--image", "--engine")
	path := first(pos)
	p, err := loadRequired(path)
	if err != nil {
		return err
	}

	st, err := openStore(ctx, flags["--db"])
	if err != nil {
		return err
	}
	defer st.Close()

	// Real node executor: native nodes run in the sandbox (subprocess by default,
	// container with --image); semantic/agent nodes need a model (FLOWUP_MODEL).
	mc := model.New()
	var backend sandbox.Backend
	if img := flags["--image"]; img != "" {
		backend = sandbox.NewContainer(flags["--engine"], img)
	} else {
		backend = sandbox.NewSubprocess()
	}
	disp := runner.NewRouter(backend, mc, runner.DefaultTools(backend))
	fmt.Fprintf(os.Stderr, "executor: router (sandbox=%s, model=%s)\n", backend.Name(), modelName(mc))

	eng := engine.New(durable.NewSelfBuilt(st), disp, st, trace.Logger(slog.LevelWarn))
	eng.Register(p)

	params := json.RawMessage(flags["--params"])
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}

	res, err := eng.Invoke(ctx, contracts.Invocation{
		PipelineID:     p.Pipeline,
		Params:         params,
		IdempotencyKey: "flowup-run",
		Caller:         contracts.Caller{Host: "cli"},
	})
	if err != nil {
		return err
	}

	printResult(res)
	printTrace(ctx, st, res.RunID)
	if flags["--db"] != "" {
		fmt.Printf("\n(state persisted to %s — try: flowup trace --db %s %s)\n", flags["--db"], flags["--db"], res.RunID)
	}
	if res.Status != contracts.StatusOK {
		os.Exit(1)
	}
	return nil
}

// cmdNew scaffolds a starter pipeline ("a new skill") that runs out of the box.
func cmdNew(args []string) error {
	pos, _ := parseArgs(args)
	name := first(pos)
	if name == "" {
		return fmt.Errorf("usage: flowup new <name>")
	}
	file := name + ".pipeline.yaml"
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("%s already exists", file)
	}
	if err := os.WriteFile(file, []byte(scaffoldYAML(filepath.Base(name))), 0o644); err != nil {
		return err
	}
	fmt.Printf("created %s\n\nnext:\n  flowup validate %s\n  flowup mermaid  %s\n  flowup run      %s\n", file, file, file, file)
	return nil
}

// scaffoldYAML returns a minimal, immediately-runnable pipeline with one native
// node (a real command, no model needed). argv is OS-appropriate for THIS host.
func scaffoldYAML(name string) string {
	var argv string
	if runtime.GOOS == "windows" {
		cs := os.Getenv("ComSpec")
		if cs == "" {
			cs = `C:\Windows\System32\cmd.exe`
		}
		argv = fmt.Sprintf("['%s', '/c', 'echo hello from %s']", cs, name)
	} else {
		argv = fmt.Sprintf("['sh', '-c', 'echo hello from %s']", name)
	}
	return fmt.Sprintf(`# %s — a starter Flowup pipeline ("a skill").
# You edit this YAML (the single source of truth); the deterministic engine runs
# it and exposes it through the one host skill. Visualize with: flowup mermaid.
pipeline: %s
version: 1
description: "starter pipeline generated by 'flowup new'"

# Invocation.params is validated against this before the run.
input_schema:
  type: object

nodes:
  # native = a deterministic command run in the sandbox (no model needed).
  # semantic/agent = model-backed (need a model runner; not wired yet).
  - id: greet
    kind: native
    needs: []
    requires_caps: [os]
    profile: { models: [native] }
    inputs:
      argv: %s
    limits: { max_steps: 1, budget_usd: 0, timeout_sec: 30 }
    # No output_schema → the runner wraps stdout as {"stdout": "...", "exit_code": 0}.
    # Add an output_schema and emit JSON to get a typed, validated output instead.

# The pipeline's typed output (the 'ok' result).
output:
  result: "{{ greet.stdout }}"
`, name, name, argv)
}

func modelName(mc model.Client) string {
	if mc == nil {
		return "none"
	}
	return mc.Name()
}

func newLease() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func cmdTrace(args []string) error {
	ctx := context.Background()
	pos, flags := parseArgs(args, "--db")
	if flags["--db"] == "" || len(pos) < 1 {
		return fmt.Errorf("usage: flowup trace --db <path> <run_id>")
	}
	st, err := openStore(ctx, flags["--db"])
	if err != nil {
		return err
	}
	defer st.Close()
	evs, err := st.ListEvents(ctx, pos[0])
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		fmt.Printf("no events for run %s\n", pos[0])
		return nil
	}
	printTrace(ctx, st, pos[0])
	return nil
}

func cmdReplay(args []string) error {
	ctx := context.Background()
	pos, flags := parseArgs(args, "--db", "--pipelines")
	if flags["--db"] == "" || flags["--pipelines"] == "" || len(pos) < 2 {
		return fmt.Errorf("usage: flowup replay --db <path> --pipelines <dir> <run_id> <node_id>")
	}
	st, err := openStore(ctx, flags["--db"])
	if err != nil {
		return err
	}
	defer st.Close()

	eng := realEngine(st, slog.LevelError)
	ps, err := pipelines.LoadDir(flags["--pipelines"])
	if err != nil {
		return err
	}
	for _, p := range ps {
		eng.Register(p)
	}

	res, err := eng.ReplayNode(ctx, pos[0], pos[1])
	if err != nil {
		return err
	}
	fmt.Printf("replay node=%s status=%s\n", res.NodeID, res.Status)
	if len(res.Output) > 0 {
		fmt.Printf("output: %s\n", string(res.Output))
	}
	if res.Reason != "" {
		fmt.Printf("reason: %s\n", res.Reason)
	}
	fmt.Println("(original run is untouched: replay runs in a shadow context)")
	return nil
}

// cmdInvoke is the host-facing call surface: read an Invocation as JSON from
// stdin, run (or resume) it, and write the three-state InvocationResult as JSON
// to stdout. A single skill in, typed three states out, never a raw error.
func cmdInvoke(args []string) error {
	ctx := context.Background()
	_, flags := parseArgs(args, "--db", "--pipelines")

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var inv contracts.Invocation
	if err := sonic.Unmarshal(raw, &inv); err != nil {
		return fmt.Errorf("parse Invocation JSON from stdin: %w", err)
	}

	st, err := openStore(ctx, flags["--db"])
	if err != nil {
		return err
	}
	defer st.Close()
	eng := realEngine(st, slog.LevelError)
	if pd := flags["--pipelines"]; pd != "" {
		ps, err := pipelines.LoadDir(pd)
		if err != nil {
			return err
		}
		for _, p := range ps {
			eng.Register(p)
		}
	}

	var res contracts.InvocationResult
	if inv.ResumeToken != "" {
		// On resume, Params carries the human's response for the waiting node.
		res, err = eng.Resume(ctx, inv.ResumeToken, inv.Params)
	} else {
		res, err = eng.Invoke(ctx, inv)
	}
	if err != nil {
		return err
	}

	out, _ := sonic.ConfigDefault.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if res.Status == contracts.StatusFailed {
		os.Exit(1)
	}
	return nil
}

// cmdMCP runs an MCP (stdio) server exposing run_pipeline / list_pipelines to any
// MCP-capable host. native pipelines run for real; model nodes need FLOWUP_MODEL.
func cmdMCP(args []string) error {
	ctx := context.Background()
	_, flags := parseArgs(args, "--db", "--pipelines")

	st, err := openStore(ctx, flags["--db"])
	if err != nil {
		return err
	}
	defer st.Close()

	backend := sandbox.NewSubprocess()
	disp := runner.NewRouter(backend, model.New(), runner.DefaultTools(backend))
	eng := engine.New(durable.NewSelfBuilt(st), disp, st, trace.Logger(slog.LevelError))
	if pd := flags["--pipelines"]; pd != "" {
		ps, err := pipelines.LoadDir(pd)
		if err != nil {
			return err
		}
		for _, p := range ps {
			eng.Register(p)
		}
	}
	fmt.Fprintf(os.Stderr, "flowup mcp: serving %v over stdio\n", eng.Registered())
	return mcp.Serve(ctx, os.Stdin, os.Stdout, "flowup", "0.1", mcpHandler{eng: eng})
}

type mcpHandler struct{ eng *engine.Engine }

func (m mcpHandler) Tools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "run_pipeline",
			Description: "Run a predefined Flowup pipeline; returns a three-state result (ok with typed output / failed with a classified reason / needs_human with a resume_token). Never a raw error.",
			InputSchema: json.RawMessage(`{"type":"object","required":["pipeline_id"],"properties":{"pipeline_id":{"type":"string"},"params":{"type":"object"},"idempotency_key":{"type":"string"},"resume_token":{"type":"string"}}}`),
		},
		{
			Name:        "list_pipelines",
			Description: "List the pipeline ids this server can run.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
}

func (m mcpHandler) Call(ctx context.Context, name string, args json.RawMessage) (string, bool) {
	switch name {
	case "list_pipelines":
		b, _ := sonic.Marshal(map[string]any{"pipelines": m.eng.Registered()})
		return string(b), false
	case "run_pipeline":
		var a struct {
			PipelineID     string          `json:"pipeline_id"`
			Params         json.RawMessage `json:"params"`
			IdempotencyKey string          `json:"idempotency_key"`
			ResumeToken    string          `json:"resume_token"`
		}
		if err := sonic.Unmarshal(args, &a); err != nil {
			return "bad arguments: " + err.Error(), true
		}
		var res contracts.InvocationResult
		if a.ResumeToken != "" {
			res, _ = m.eng.Resume(ctx, a.ResumeToken, a.Params)
		} else {
			if a.IdempotencyKey == "" {
				a.IdempotencyKey = "mcp-" + newLease()
			}
			res, _ = m.eng.Invoke(ctx, contracts.Invocation{
				PipelineID: a.PipelineID, Params: a.Params,
				IdempotencyKey: a.IdempotencyKey, Caller: contracts.Caller{Host: "mcp"},
			})
		}
		b, _ := sonic.ConfigDefault.MarshalIndent(res, "", "  ")
		return string(b), res.Status == contracts.StatusFailed
	}
	return "unknown tool: " + name, true
}

// realEngine builds an engine over the real Router (sandbox + optional model).
func realEngine(st store.Store, level slog.Level) *engine.Engine {
	backend := sandbox.NewSubprocess()
	disp := runner.NewRouter(backend, model.New(), runner.DefaultTools(backend))
	return engine.New(durable.NewSelfBuilt(st), disp, st, trace.Logger(level))
}

func openStore(ctx context.Context, db string) (store.Store, error) {
	if db == "" {
		db = ":memory:"
	}
	return store.NewSQLite(ctx, db)
}

// parseArgs splits args into positionals and value-flags (e.g. "--db path").
func parseArgs(args []string, valueFlags ...string) ([]string, map[string]string) {
	vf := make(map[string]bool, len(valueFlags))
	for _, f := range valueFlags {
		vf[f] = true
	}
	var pos []string
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if vf[a] && i+1 < len(args) {
			flags[a] = args[i+1]
			i++
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			continue
		}
		pos = append(pos, a)
	}
	return pos, flags
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func printResult(res contracts.InvocationResult) {
	fmt.Printf("\nrun_id : %s\nstatus : %s\n", res.RunID, res.Status)
	if len(res.Output) > 0 {
		fmt.Printf("output : %s\n", string(res.Output))
	}
	if res.Reason != "" {
		fmt.Printf("reason : %s\n", res.Reason)
	}
	if res.Handoff != nil {
		fmt.Printf("handoff: %s (resume_token=%s)\n", res.Handoff.What, res.Handoff.ResumeToken)
	}
}

func printTrace(ctx context.Context, st store.Store, runID string) {
	evs, err := st.ListEvents(ctx, runID)
	if err != nil || len(evs) == 0 {
		return
	}
	fmt.Println("\nevent log (replay truth source):")
	for _, e := range evs {
		fmt.Printf("  [%s] node=%s attempt=%d %s\n", e.Type, e.NodeID, e.Attempt, string(e.Data))
	}
}
