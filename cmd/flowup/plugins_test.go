package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

// The test binary is a real, portable subprocess fixture; no shell is required.
func TestLocalPluginProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	mode := os.Args[separator+1]
	var req struct {
		ProtocolVersion int            `json:"protocol_version"`
		RunID           string         `json:"run_id"`
		StepID          string         `json:"step_id"`
		IdempotencyKey  string         `json:"idempotency_key"`
		Input           map[string]any `json:"input"`
	}
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(10)
	}
	if req.ProtocolVersion != 1 || req.RunID == "" || req.StepID == "" {
		os.Exit(11)
	}
	text, _ := req.Input["text"].(string)
	switch mode {
	case "invalid":
		fmt.Print("not-json")
	case "trailing":
		fmt.Print(`{"output":{"text":"first"}} {"output":{}}`)
	case "missing":
		fmt.Print(`{}`)
	case "schema":
		fmt.Print(`{"output":{"text":123}}`)
	case "exit":
		fmt.Fprint(os.Stderr, "sensitive-stderr")
		os.Exit(7)
	case "large":
		fmt.Print(strings.Repeat("x", 2<<20))
	case "sleep":
		time.Sleep(5 * time.Second)
	case "env":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"output": map[string]any{"text": os.Getenv("FLOWUP_PLUGIN_TEST_ALLOWED") + ":" + os.Getenv("FLOWUP_PLUGIN_TEST_SECRET")}})
	case "file":
		data, err := os.ReadFile(os.Args[separator+2])
		if err != nil {
			os.Exit(13)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"output": map[string]any{"text": string(data)}})
	case "transient":
		counter := req.Input["counter"].(string)
		old, _ := os.ReadFile(counter)
		_ = os.WriteFile(counter, append(old, 'x'), 0600)
		fmt.Print(`{"error":{"message":"temporarily unavailable","transient":true}}`)
	case "secret-error":
		secret := req.Input["token"]
		if headers, ok := req.Input["headers"].(map[string]any); ok {
			secret = headers["authorization"]
		}
		if tokens, ok := req.Input["tokens"].([]any); ok {
			secret = tokens[0]
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"error": map[string]any{"message": secret}})
	default:
		if mode == "external" && req.IdempotencyKey == "" {
			os.Exit(12)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"output": map[string]any{"text": strings.ToUpper(text)}})
	}
	os.Exit(0)
}

type localPluginFixture struct {
	dir, manifest, workflow, inputs, database, code string
	spec                                            map[string]any
}

func newLocalPluginFixture(t *testing.T, mode string, approval bool) localPluginFixture {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	f := localPluginFixture{dir: dir, manifest: filepath.Join(dir, "plugins.yaml"), workflow: filepath.Join(dir, "workflow.yaml"), inputs: filepath.Join(dir, "inputs.json"), database: filepath.Join(dir, "state.db"), code: filepath.Join(dir, "code.txt")}
	require.NoError(t, os.WriteFile(f.code, []byte("original"), 0600))
	f.spec = map[string]any{
		"name": "local.echo", "version": "1.0.0", "command": []string{executable, "-test.run=^TestLocalPluginProcess$", "--", mode},
		"files": []string{"code.txt"}, "effect": "read_only", "timeout": "3s",
		"input_schema":  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
		"output_schema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
	}
	gate := ""
	if approval {
		gate = "  - id: review\n    uses: approval\n    with: {message: Continue}\n"
	}
	wf := "name: local-plugin\nversion: 1\nsteps:\n" + gate + "  - id: echo\n    uses: local.echo\n    with: {text: hello}\noutputs:\n  result: ${{ steps.echo.output.text }}\n"
	require.NoError(t, os.WriteFile(f.workflow, []byte(wf), 0600))
	require.NoError(t, os.WriteFile(f.inputs, []byte(`{}`), 0600))
	f.save(t)
	return f
}

func (f localPluginFixture) save(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "plugins": []any{f.spec}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.manifest, data, 0600))
}

func pluginCLI(args ...string) (int, string, string) {
	var out, err bytes.Buffer
	code := runCLI(context.Background(), &out, &err, args)
	return code, out.String(), err.String()
}

func (f localPluginFixture) run() (int, string, string) {
	return pluginCLI("run", f.workflow, "--inputs", f.inputs, "--db", f.database, "--plugins", f.manifest)
}

func TestLocalPluginValidateAndRun(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", false)
	code, out, err := pluginCLI("validate", f.workflow, "--plugins", f.manifest)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, "workflow valid")
	code, out, err = f.run()
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `output: {"result":"HELLO"}`)
	id := outputField(t, out, "run_id")
	code, out, err = pluginCLI("trace", id, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, "local.echo")
}

func TestLocalPluginApproveUsesSavedBindings(t *testing.T) {
	f := newLocalPluginFixture(t, "external", true)
	f.spec["effect"] = "external"
	f.save(t)
	code, out, err := f.run()
	require.Equal(t, 0, code, err)
	approval := outputField(t, out, "approval_id")
	require.NoError(t, os.Remove(f.manifest))
	t.Chdir(t.TempDir())
	code, out, err = pluginCLI("approve", approval, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `output: {"result":"HELLO"}`)
}

func TestLocalPluginChangedFileBlocksApprovalWithoutDeciding(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", true)
	code, out, err := f.run()
	require.Equal(t, 0, code, err)
	approval := outputField(t, out, "approval_id")
	require.NoError(t, os.WriteFile(f.code, []byte("changed"), 0600))
	code, _, err = pluginCLI("approve", approval, "--db", f.database)
	require.Equal(t, 1, code)
	require.Contains(t, err, "changed")
	s, openErr := store.OpenSQLite(f.database)
	require.NoError(t, openErr)
	a, getErr := s.GetApproval(context.Background(), approval)
	require.NoError(t, getErr)
	require.Equal(t, store.ApprovalPending, a.Status)
	require.NoError(t, s.Close())
	require.NoError(t, os.WriteFile(f.code, []byte("original"), 0600))
	code, out, err = pluginCLI("approve", approval, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, "succeeded")
}

func TestLocalPluginRejectDoesNotRequireFiles(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", true)
	code, out, err := f.run()
	require.Equal(t, 0, code, err)
	approval := outputField(t, out, "approval_id")
	require.NoError(t, os.Remove(f.code))
	code, out, err = pluginCLI("reject", approval, "--db", f.database)
	require.Equal(t, 1, code)
	require.Empty(t, err)
	require.Contains(t, out, "rejected")
}

func TestLocalPluginFailures(t *testing.T) {
	for _, mode := range []string{"invalid", "trailing", "missing", "schema", "exit", "large", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalPluginFixture(t, mode, false)
			if mode == "sleep" {
				f.spec["timeout"] = "150ms"
				f.save(t)
			}
			started := time.Now()
			code, out, err := f.run()
			require.Equal(t, 1, code)
			require.Contains(t, out, "failed")
			require.NotContains(t, err+out, "sensitive-stderr")
			if mode == "invalid" {
				require.Contains(t, err+out, "invalid JSON response")
			}
			require.Less(t, time.Since(started), 4*time.Second)
		})
	}
}

func TestLocalPluginEnvironmentIsExplicit(t *testing.T) {
	t.Setenv("FLOWUP_PLUGIN_TEST_ALLOWED", "visible")
	t.Setenv("FLOWUP_PLUGIN_TEST_SECRET", "hidden")
	f := newLocalPluginFixture(t, "env", false)
	f.spec["env"] = []string{"FLOWUP_PLUGIN_TEST_ALLOWED"}
	f.save(t)
	code, out, err := f.run()
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `"visible:"`)
	require.NotContains(t, out, "hidden")
}

func TestLocalPluginValidationDoesNotLaunchProcess(t *testing.T) {
	f := newLocalPluginFixture(t, "exit", false)
	code, _, err := pluginCLI("validate", f.workflow, "--plugins", f.manifest)
	require.Equal(t, 0, code, err)
}

func TestLocalPluginCannotReplaceBuiltIn(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", false)
	f.spec["name"] = "json.select"
	f.save(t)
	code, _, err := pluginCLI("validate", f.workflow, "--plugins", f.manifest)
	require.Equal(t, 2, code)
	require.Contains(t, err, "duplicate action")
}

func TestLocalPluginInstallAndUninstall(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", false)
	t.Chdir(f.dir)
	code, out, err := pluginCLI("plugin", "install", f.manifest)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, "local.echo")
	// Installation copies declared files; removing the source must not break it.
	require.NoError(t, os.Remove(f.code))
	require.NoError(t, os.Remove(f.manifest))
	code, out, err = pluginCLI("run", f.workflow, "--inputs", f.inputs, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `"HELLO"`)
	code, out, err = pluginCLI("plugin", "uninstall", "local.echo")
	require.Equal(t, 0, code, err)
	require.Contains(t, out, "local.echo")
	code, _, err = pluginCLI("validate", f.workflow)
	require.Equal(t, 2, code)
	require.Contains(t, err, "unknown action")
}

func TestLocalPluginReadRetriesButWriteDoesNot(t *testing.T) {
	for _, effect := range []string{"read_only", "external"} {
		t.Run(effect, func(t *testing.T) {
			f := newLocalPluginFixture(t, "transient", false)
			f.spec["effect"] = effect
			f.save(t)
			counter := filepath.Join(f.dir, "counter.txt")
			value, _ := json.Marshal(counter)
			source, _ := os.ReadFile(f.workflow)
			source = []byte(strings.Replace(string(source), "with: {text: hello}", "with: {text: hello, counter: "+string(value)+"}", 1))
			require.NoError(t, os.WriteFile(f.workflow, source, 0600))
			code, out, err := f.run()
			require.Equal(t, 1, code, err)
			require.Contains(t, out, "failed")
			calls, errRead := os.ReadFile(counter)
			require.NoError(t, errRead)
			want := 3
			if effect == "external" {
				want = 1
			}
			require.Len(t, calls, want)
		})
	}
}

func TestLocalPluginSecretErrorsAndStoredInputsAreRedacted(t *testing.T) {
	for _, path := range []string{"token", "headers.*", "tokens[0]"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("FLOWUP_TEST_PLUGIN_TOKEN", "sensitive-plugin-value")
			f := newLocalPluginFixture(t, "secret-error", false)
			f.spec["secret_paths"] = []string{path}
			f.save(t)
			source, _ := os.ReadFile(f.workflow)
			field := "token: '${{ secrets.FLOWUP_TEST_PLUGIN_TOKEN }}'"
			if path == "headers.*" {
				field = "headers: {authorization: '${{ secrets.FLOWUP_TEST_PLUGIN_TOKEN }}'}"
			}
			if path == "tokens[0]" {
				field = "tokens: ['${{ secrets.FLOWUP_TEST_PLUGIN_TOKEN }}']"
			}
			require.NoError(t, os.WriteFile(f.workflow, []byte(strings.Replace(string(source), "with: {text: hello}", "with: {text: hello, "+field+"}", 1)), 0600))
			code, out, err := f.run()
			require.Equal(t, 1, code)
			require.NotContains(t, out+err, "sensitive-plugin-value")
			runID := outputField(t, out, "run_id")
			s, openErr := store.OpenSQLite(f.database)
			require.NoError(t, openErr)
			defer s.Close()
			step, loadErr := s.GetStep(context.Background(), runID, "echo")
			require.NoError(t, loadErr)
			require.NotContains(t, string(step.Input)+step.ErrorMessage, "sensitive-plugin-value")
			require.Contains(t, step.ErrorMessage, "[REDACTED]")
			run, loadErr := s.GetRun(context.Background(), runID)
			require.NoError(t, loadErr)
			require.NotContains(t, string(run.PluginBindings), "sensitive-plugin-value")
		})
	}
}

func TestLocalPluginInstalledUpgradeAndUninstallKeepWaitingRun(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", true)
	t.Chdir(f.dir)
	code, _, err := pluginCLI("plugin", "install", f.manifest)
	require.Equal(t, 0, code, err)
	code, out, err := pluginCLI("run", f.workflow, "--inputs", f.inputs, "--db", f.database)
	require.Equal(t, 0, code, err)
	approval := outputField(t, out, "approval_id")
	f.spec["version"] = "2.0.0"
	f.spec["command"].([]string)[3] = "exit"
	f.save(t)
	code, _, err = pluginCLI("plugin", "install", f.manifest)
	require.Equal(t, 0, code, err)
	code, _, err = pluginCLI("plugin", "uninstall", "local.echo")
	require.Equal(t, 0, code, err)
	t.Chdir(t.TempDir())
	code, out, err = pluginCLI("approve", approval, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `"HELLO"`)
}

func TestLocalPluginResumeRestoresSnapshot(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", true)
	code, out, err := f.run()
	require.Equal(t, 0, code, err)
	runID := outputField(t, out, "run_id")
	s, openErr := store.OpenSQLite(f.database)
	require.NoError(t, openErr)
	run, loadErr := s.GetRun(context.Background(), runID)
	require.NoError(t, loadErr)
	// Reproduce a crash after the approval state was persisted, before dispatch.
	run.Status = store.RunRunning
	run.CurrentStep = 1
	require.NoError(t, s.UpdateRun(context.Background(), run))
	require.NoError(t, s.PutStep(context.Background(), store.StepRecord{RunID: runID, StepID: "review", StepIndex: 0, ActionName: "approval", Status: store.StepApproved, Output: json.RawMessage(`{"approved":true}`)}))
	require.NoError(t, s.Close())
	require.NoError(t, os.Remove(f.manifest))
	t.Chdir(t.TempDir())
	code, out, err = pluginCLI("resume", runID, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `"HELLO"`)
}

func TestLocalPluginInvalidManifests(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing-version", func(s map[string]any) { delete(s, "version") }},
		{"missing-effect", func(s map[string]any) { delete(s, "effect") }},
		{"bad-timeout", func(s map[string]any) { s["timeout"] = "0s" }},
		{"bad-name", func(s map[string]any) { s["name"] = "echo" }},
		{"unknown-field", func(s map[string]any) { s["typo"] = "value" }},
		{"outside-file", func(s map[string]any) { s["files"] = []string{"../outside"} }},
		{"bad-schema", func(s map[string]any) { s["input_schema"] = map[string]any{"type": 123} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLocalPluginFixture(t, "echo", false)
			test.change(f.spec)
			f.save(t)
			code, _, err := pluginCLI("validate", f.workflow, "--plugins", f.manifest)
			require.Equal(t, 2, code)
			require.NotEmpty(t, err)
		})
	}
}

func TestLocalPluginInstallRewritesDeclaredAbsoluteFileArgument(t *testing.T) {
	f := newLocalPluginFixture(t, "file", true)
	f.spec["command"] = append(f.spec["command"].([]string), f.code)
	f.save(t)
	t.Chdir(f.dir)
	code, _, err := pluginCLI("plugin", "install", f.manifest)
	require.Equal(t, 0, code, err)
	code, out, err := pluginCLI("run", f.workflow, "--inputs", f.inputs, "--db", f.database)
	require.Equal(t, 0, code, err)
	approval := outputField(t, out, "approval_id")
	require.NoError(t, os.Remove(f.code))
	code, out, err = pluginCLI("approve", approval, "--db", f.database)
	require.Equal(t, 0, code, err)
	require.Contains(t, out, `"original"`)
}

func TestLocalPluginSchemaErrorDoesNotExposeSecret(t *testing.T) {
	t.Setenv("FLOWUP_TEST_PLUGIN_TOKEN", "schema-sensitive-value")
	f := newLocalPluginFixture(t, "echo", false)
	f.spec["secret_paths"] = []string{"token"}
	f.spec["input_schema"].(map[string]any)["properties"].(map[string]any)["token"] = map[string]any{"type": "string", "pattern": "^expected-prefix"}
	f.save(t)
	source, _ := os.ReadFile(f.workflow)
	require.NoError(t, os.WriteFile(f.workflow, []byte(strings.Replace(string(source), "with: {text: hello}", "with: {text: hello, token: '${{ secrets.FLOWUP_TEST_PLUGIN_TOKEN }}'}", 1)), 0600))
	code, out, err := f.run()
	require.Equal(t, 1, code)
	require.NotContains(t, out+err, "schema-sensitive-value")
	runID := outputField(t, out, "run_id")
	_, trace, _ := pluginCLI("trace", runID, "--db", f.database)
	require.NotContains(t, trace, "schema-sensitive-value")
}

func TestLocalPluginInstallRejectsReservedPayload(t *testing.T) {
	f := newLocalPluginFixture(t, "echo", false)
	t.Chdir(f.dir)
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, ".flowup-manifest.json"), []byte("payload"), 0600))
	f.spec["files"] = []string{".flowup-manifest.json"}
	f.save(t)
	code, _, err := pluginCLI("plugin", "install", f.manifest)
	require.Equal(t, 1, code)
	require.Contains(t, err, "reserved")
}
