package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// SubprocessBackend runs commands as local OS processes. It provides no
// isolation (the command shares the host), so use it only for trusted nodes or
// local dev. Zero dependencies; runs anywhere, including Windows.
type SubprocessBackend struct{}

// NewSubprocess returns a subprocess (no-isolation) backend.
func NewSubprocess() *SubprocessBackend { return &SubprocessBackend{} }

func (*SubprocessBackend) Name() string { return "subprocess" }

func (b *SubprocessBackend) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, errors.New("sandbox: empty argv")
	}
	if spec.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSec)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	cmd.Env = envWith(spec.Env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil // ran, but exited non-zero — not a transport error
		}
		return res, err // failed to start
	}
	return res, nil
}

func envWith(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
