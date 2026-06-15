// Package durable is the durable-execution adapter: the single seam through
// which the deterministic engine talks to the durability substrate. The
// self-built event-sourcing executor here and a future DBOS-backed one implement
// the same interface, so the substrate is swappable without touching the engine.
//
// The mapping to a workflow engine is intentional: Run is a durable workflow,
// Step a checkpointed (memoized) step, Suspend/Resume a wait-for-event, and
// Recover the startup pass that re-drives pending workflows.
package durable

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/moyoez/flowup/internal/contracts"
)

// ErrSuspended signals a first-class needs_human pause (not a failure). A
// WorkflowFunc returns it via Handle.Suspend.
var ErrSuspended = errors.New("workflow suspended for human input")

// Failure is a classified failure reason. The engine wraps every internal error
// in a Failure so a raw error never crosses the three-state boundary.
type Failure struct{ Reason string }

func (f *Failure) Error() string { return f.Reason }

// WorkflowFunc is the deterministic workflow body supplied by the engine. On a
// fresh run it executes; on replay/recovery, completed Steps short-circuit so
// the same body re-run produces the same result without re-executing committed
// (possibly non-deterministic) work.
type WorkflowFunc func(ctx context.Context, h Handle) (json.RawMessage, error)

// StepFunc is the unit of memoized work inside a workflow.
type StepFunc func(ctx context.Context) (json.RawMessage, error)

// RunSpec describes a run to start.
type RunSpec struct {
	PipelineID     string
	IdempotencyKey string
	Input          json.RawMessage
}

// Outcome is the durable result of a run, mapped 1:1 onto the three-state contract.
type Outcome struct {
	RunID   string
	Status  contracts.Status
	Output  json.RawMessage
	Reason  string
	Handoff *contracts.Handoff
}

// BodyForRun rebuilds the workflow body for a recovered run from its persisted
// pipeline id + input. Recover uses it to re-drive crashed runs.
type BodyForRun func(pipelineID string, input json.RawMessage) (WorkflowFunc, error)

// Handle is the per-run durability surface available inside a WorkflowFunc.
type Handle interface {
	RunID() string

	// Step memoizes fn's successful output under stepKey. On replay it returns
	// the recorded output WITHOUT calling fn. This is precisely how a
	// non-deterministic AI node is replayed (not re-run) after a crash.
	Step(ctx context.Context, stepKey string, fn StepFunc) (json.RawMessage, error)

	// HumanInput returns input injected by Resume for a node, if present.
	HumanInput(ctx context.Context, nodeID string) (json.RawMessage, bool, error)

	// Suspend persists a needs_human pause (a generated resume_token + handoff
	// text) and returns ErrSuspended for the body to propagate.
	Suspend(ctx context.Context, nodeID, what string) error

	// Emit appends a NodeEvent to the durable event log (replay truth source),
	// keyed by the run_id/node_id/attempt correlation IDs.
	Emit(ctx context.Context, nodeID string, attempt int, typ string, data json.RawMessage)

	// ClaimSideEffect / FinishSideEffect are the exactly-once outbox primitive
	// for side_effect nodes.
	ClaimSideEffect(ctx context.Context, key string) (claimed bool, existing json.RawMessage, err error)
	FinishSideEffect(ctx context.Context, key string, result json.RawMessage) error
}

// Executor is the durable-execution substrate. Self-built and DBOS-backed
// implementations are interchangeable behind it.
type Executor interface {
	// Run starts a run, or returns the recorded result if the idempotency key
	// already maps to a terminal/suspended run (call-level exactly-once / replay).
	Run(ctx context.Context, spec RunSpec, body WorkflowFunc) (Outcome, error)
	// Resume continues a suspended run by resume token, injecting human input.
	// bodyFor rebuilds the workflow body from the run's persisted pipeline+input
	// (the executor resolves which run the token belongs to).
	Resume(ctx context.Context, resumeToken string, humanInput json.RawMessage, bodyFor BodyForRun) (Outcome, error)
	// Recover re-drives every PENDING run left by a crash; returns how many ran.
	Recover(ctx context.Context, bodyFor BodyForRun) (int, error)
	Close() error
}
