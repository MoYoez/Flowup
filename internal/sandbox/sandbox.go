// Package sandbox runs a node's work inside an isolation boundary, behind the
// dispatch.Runner seam. It is local-first and pluggable: pick a Backend by how
// much isolation a deployment needs, with no engine changes.
//
//   - SubprocessBackend: zero-dependency, runs anywhere (including Windows), but
//     provides no isolation — use it only for trusted nodes or local dev.
//   - ContainerBackend: one container per task via a daemonless engine (podman
//     default, docker). Self-hosted. Point it at a gVisor/Kata runtime for
//     stronger isolation, or swap in a microVM backend behind this interface.
package sandbox

import "context"

// ExecSpec is a command to run in a sandbox.
type ExecSpec struct {
	Argv       []string          // command + args (run directly, no shell)
	Env        map[string]string // extra environment variables
	Stdin      []byte            // optional stdin
	TimeoutSec int               // 0 = no timeout
}

// ExecResult is the outcome of a sandboxed command. A non-zero ExitCode is NOT a
// Go error — the command ran; the caller decides how to classify it.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
}

// Backend runs a command inside some isolation boundary.
type Backend interface {
	Name() string
	Exec(ctx context.Context, spec ExecSpec) (ExecResult, error)
}
