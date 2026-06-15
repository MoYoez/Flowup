package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
)

// ReplayNode re-executes a single node using the inputs recorded for an existing
// run, in a shadow context that never touches the original run's state, event
// log, or side-effect dedup table. It supports time-travel debugging: inspect
// what a (possibly non-deterministic) node does with the same inputs, without
// polluting the run.
//
// Side-effect nodes are replayed dry: the shadow handle never records into the
// dedup table. A side-effecting node should expose a dry-run mode so replay does
// not re-trigger the external effect.
func (e *Engine) ReplayNode(ctx context.Context, runID, nodeID string) (contracts.NodeResult, error) {
	var zero contracts.NodeResult
	if e.st == nil {
		return zero, errors.New("replay: engine has no store")
	}
	run, ok, err := e.st.GetRun(ctx, runID)
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, fmt.Errorf("replay: run %s not found", runID)
	}
	p, ok := e.reg[run.PipelineID]
	if !ok {
		return zero, fmt.Errorf("replay: unknown pipeline %q", run.PipelineID)
	}
	node, ok := p.NodeByID(nodeID)
	if !ok {
		return zero, fmt.Errorf("replay: node %q not in pipeline %q", nodeID, run.PipelineID)
	}

	// Rebuild the run context from recorded state: params + every recorded node
	// output. The target node's templates resolve against this exactly as they
	// did originally.
	runCtx := map[string]any{}
	params := map[string]any{}
	if len(run.Input) > 0 {
		_ = sonic.Unmarshal(run.Input, &params)
	}
	runCtx["params"] = params
	for _, n := range p.Nodes {
		rec, ok, err := e.st.GetStep(ctx, runID, n.ID)
		if err != nil {
			return zero, err
		}
		if ok && len(rec.Output) > 0 {
			var m map[string]any
			if sonic.Unmarshal(rec.Output, &m) == nil {
				runCtx[n.ID] = m
			}
		}
	}

	out, rerr := e.runNode(ctx, shadowHandle{runID: "replay_" + runID}, node, runCtx)
	return nodeResultFrom(nodeID, out, rerr), nil
}

func nodeResultFrom(nodeID string, out json.RawMessage, err error) contracts.NodeResult {
	switch {
	case err == nil:
		return contracts.NodeResult{NodeID: nodeID, Status: contracts.StatusOK, Output: out}
	case errors.Is(err, durable.ErrSuspended):
		return contracts.NodeResult{NodeID: nodeID, Status: contracts.StatusNeedsHuman}
	default:
		reason := contracts.ReasonInternal
		var f *durable.Failure
		if errors.As(err, &f) {
			reason = f.Reason
		}
		return contracts.NodeResult{NodeID: nodeID, Status: contracts.StatusFailed, Reason: reason}
	}
}

// shadowHandle is a durable.Handle whose writes are all no-ops, so replaying a
// node leaves the original run untouched.
type shadowHandle struct{ runID string }

func (s shadowHandle) RunID() string { return s.runID }

func (shadowHandle) Step(ctx context.Context, _ string, fn durable.StepFunc) (json.RawMessage, error) {
	return fn(ctx) // no memoization during replay
}

func (shadowHandle) HumanInput(context.Context, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

func (shadowHandle) Suspend(context.Context, string, string) error { return durable.ErrSuspended }

func (shadowHandle) Emit(context.Context, string, int, string, json.RawMessage) {}

func (shadowHandle) ClaimSideEffect(context.Context, string) (bool, json.RawMessage, error) {
	return true, nil, nil // allow the node to run; never touch the real dedup table
}

func (shadowHandle) FinishSideEffect(context.Context, string, json.RawMessage) error { return nil }
