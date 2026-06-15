// Package runner composes the execution backends into one Dispatcher that routes
// a NodeTask by its kind: native → sandbox (real command), semantic/agent →
// the model+tool agent loop. It is the unified node executor a Worker runs;
// behind the same dispatch.Runner / engine.Dispatcher seam, so the engine never
// changes.
package runner

import (
	"context"

	"github.com/moyoez/flowup/internal/agent"
	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/tools"
)

// Router dispatches a node to the right backend by kind.
type Router struct {
	native *sandbox.Runner
	agent  *agent.Runner // nil when no model is configured
}

// NewRouter builds the unified executor. If model is nil, semantic/agent nodes
// return a classified model_runner_not_configured failure.
func NewRouter(backend sandbox.Backend, mc model.Client, reg *tools.Registry) *Router {
	r := &Router{native: sandbox.NewRunner(backend)}
	if mc != nil {
		r.agent = agent.NewRunner(mc, reg)
	}
	return r
}

// SetTokenSink wires live answer-token streaming (no-op if no model is configured).
func (r *Router) SetTokenSink(fn func(runID, nodeID, chunk string)) {
	if r.agent != nil {
		r.agent.TokenSink = fn
	}
}

// DefaultTools is the standard tool set over a sandbox backend. Nodes still
// only get the tools their profile.tools whitelists — this is the superset.
func DefaultTools(backend sandbox.Backend) *tools.Registry {
	return tools.NewRegistry(
		tools.NewHTTPGet(),
		tools.NewHTTPPost(),
		tools.NewShell(backend),
		tools.NewGit(backend),
		tools.NewFS(""),
	)
}

// Dispatch routes by node kind.
func (r *Router) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	switch task.Kind {
	case contracts.KindNative:
		return r.native.Dispatch(ctx, task)
	case contracts.KindSemantic, contracts.KindAgent:
		if r.agent == nil {
			return contracts.NodeResult{
				RunID: task.RunID, NodeID: task.NodeID, LeaseID: task.LeaseID,
				Status: contracts.StatusFailed, Reason: "model_runner_not_configured",
			}, nil
		}
		return r.agent.Dispatch(ctx, task)
	default:
		return contracts.NodeResult{
			RunID: task.RunID, NodeID: task.NodeID, LeaseID: task.LeaseID,
			Status: contracts.StatusFailed, Reason: "unknown_kind",
		}, nil
	}
}
