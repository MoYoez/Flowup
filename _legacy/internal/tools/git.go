package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/sandbox"
)

// Git runs git subcommands inside the sandbox. It is narrower than `shell` (only
// git), so a node can be granted version-control access without arbitrary exec.
type Git struct {
	Backend    sandbox.Backend
	TimeoutSec int
}

// NewGit builds a git tool over a sandbox backend.
func NewGit(b sandbox.Backend) *Git { return &Git{Backend: b, TimeoutSec: 60} }

func (*Git) Name() string { return "git" }

func (*Git) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "git",
		Description: "Run a git command. args = git arguments (e.g. [\"status\",\"--short\"]); dir = optional repo path.",
		InputSchema: json.RawMessage(`{"type":"object","required":["args"],"properties":{"args":{"type":"array","items":{"type":"string"}},"dir":{"type":"string"}}}`),
	}
}

func (g *Git) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}
	if err := sonic.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	argv := []string{"git"}
	if a.Dir != "" {
		argv = append(argv, "-C", a.Dir)
	}
	argv = append(argv, a.Args...)

	res, err := g.Backend.Exec(ctx, sandbox.ExecSpec{Argv: argv, TimeoutSec: g.TimeoutSec})
	if err != nil {
		return "", err
	}
	out := string(res.Stdout)
	if len(res.Stderr) > 0 {
		out += "\n" + string(res.Stderr)
	}
	if res.ExitCode != 0 {
		return out, fmt.Errorf("git exited %d", res.ExitCode)
	}
	return out, nil
}
