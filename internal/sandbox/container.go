package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ContainerBackend runs each command in a fresh container (one container per
// task) via a daemonless engine — podman (default) or docker. Self-hosted, no
// SaaS. For stronger isolation, point Runtime at gVisor (runsc) or Kata; to go
// full microVM, swap this whole Backend for a microsandbox-backed one.
type ContainerBackend struct {
	Engine  string // "podman" (default) | "docker"
	Image   string // base image commands run in (must be present/pullable)
	Network string // "none" (default, no egress) | "host" | "" (engine default)
	Runtime string // "" (default) | "runsc" (gVisor) | "kata" — OCI runtime for hardening
}

// NewContainer returns a container backend over the given engine + image.
func NewContainer(engine, image string) *ContainerBackend {
	if engine == "" {
		engine = "podman"
	}
	return &ContainerBackend{Engine: engine, Image: image, Network: "none"}
}

func (b *ContainerBackend) Name() string { return "container:" + b.Engine }

func (b *ContainerBackend) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	if len(spec.Argv) == 0 {
		return ExecResult{}, errors.New("sandbox: empty argv")
	}
	if b.Image == "" {
		return ExecResult{}, errors.New("sandbox: container backend requires an Image")
	}
	if spec.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSec)*time.Second)
		defer cancel()
	}

	args := []string{"run", "--rm", "-i"}
	if b.Network != "" {
		args = append(args, "--network", b.Network)
	}
	if b.Runtime != "" {
		args = append(args, "--runtime", b.Runtime)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, b.Image)
	args = append(args, spec.Argv...)

	cmd := exec.CommandContext(ctx, b.Engine, args...)
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
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
			return res, nil
		}
		return res, fmt.Errorf("%s run: %w", b.Engine, err)
	}
	return res, nil
}
