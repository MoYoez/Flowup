package sandbox

import (
	"bytes"
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/pipelines"
)

// Runner executes native-kind nodes by running their command in a Backend, and
// shapes the result into the three-state contract. It satisfies both
// dispatch.Runner (behind the queue) and engine.Dispatcher (in-process) — same
// signature.
//
// Model-backed kinds (semantic/agent) need a model runner, configured
// separately; here they return a classified failure so the boundary stays honest
// rather than pretending to run a model.
type Runner struct {
	backend Backend
}

// NewRunner wraps a Backend as a node Runner.
func NewRunner(b Backend) *Runner { return &Runner{backend: b} }

// nativeInputs is the exec spec a native node carries in its (resolved) inputs.
type nativeInputs struct {
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env"`
	Stdin string            `json:"stdin"`
}

// Dispatch runs one node.
func (r *Runner) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	res := contracts.NodeResult{RunID: task.RunID, NodeID: task.NodeID, LeaseID: task.LeaseID}

	if task.Kind != contracts.KindNative {
		// semantic/agent require a model runner — not this backend's job.
		res.Status = contracts.StatusFailed
		res.Reason = "model_runner_not_configured"
		return res, nil
	}

	var in nativeInputs
	if len(task.Inputs) > 0 {
		_ = sonic.Unmarshal(task.Inputs, &in)
	}
	if len(in.Argv) == 0 {
		res.Status = contracts.StatusFailed
		res.Reason = "missing_argv"
		return res, nil
	}

	out, err := r.backend.Exec(ctx, ExecSpec{
		Argv:       in.Argv,
		Env:        in.Env,
		Stdin:      []byte(in.Stdin),
		TimeoutSec: task.Limits.TimeoutSec,
	})
	switch {
	case err != nil:
		res.Status = contracts.StatusFailed
		res.Reason = "sandbox_exec_error"
		res.Retriable = true
		return res, nil
	case out.TimedOut:
		res.Status = contracts.StatusFailed
		res.Reason = contracts.ReasonTimeout
		res.Retriable = true
		return res, nil
	case out.ExitCode != 0:
		res.Status = contracts.StatusFailed
		res.Reason = fmt.Sprintf("exit_%d", out.ExitCode)
		return res, nil
	}

	stdout := bytes.TrimSpace(out.Stdout)
	// If the node declares a (non-empty) output_schema, treat stdout as JSON and
	// validate; otherwise wrap raw stdout. Note an absent schema marshals to the
	// 4-byte "null", so we check the decoded schema, not the byte length.
	var schema map[string]any
	if len(task.OutputSchema) > 0 {
		_ = sonic.Unmarshal(task.OutputSchema, &schema)
	}
	if len(schema) > 0 {
		if err := pipelines.ValidateJSONAgainstSchema(schema, stdout); err != nil {
			res.Status = contracts.StatusFailed
			res.Reason = contracts.ReasonSchemaViolation
			return res, nil
		}
		res.Output = stdout
	} else {
		res.Output, _ = sonic.Marshal(map[string]any{"stdout": string(stdout), "exit_code": 0})
	}
	res.Status = contracts.StatusOK
	return res, nil
}
