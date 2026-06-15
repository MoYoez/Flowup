package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/sandbox"
)

// Shell runs a command inside the sandbox Backend (subprocess/container). This is
// the build-style tool: the model's chosen argv is executed in isolation.
type Shell struct {
	Backend    sandbox.Backend
	TimeoutSec int
}

// NewShell builds a shell tool over a sandbox backend.
func NewShell(b sandbox.Backend) *Shell { return &Shell{Backend: b, TimeoutSec: 60} }

func (*Shell) Name() string { return "shell" }

func (*Shell) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "shell",
		Description: "Run a command (argv array) inside the sandbox; returns exit code, stdout, stderr.",
		InputSchema: objSchema("argv", `{"argv":{"type":"array","items":{"type":"string"},"description":"command and args"}}`),
	}
}

func (s *Shell) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Argv []string `json:"argv"`
	}
	if err := sonic.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	if len(a.Argv) == 0 {
		return "", fmt.Errorf("shell: empty argv")
	}
	res, err := s.Backend.Exec(ctx, sandbox.ExecSpec{Argv: a.Argv, TimeoutSec: s.TimeoutSec})
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("exit=%d\nstdout:\n%s", res.ExitCode, string(res.Stdout))
	if len(res.Stderr) > 0 {
		out += "\nstderr:\n" + string(res.Stderr)
	}
	return out, nil
}
