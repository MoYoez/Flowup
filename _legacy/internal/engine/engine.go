// Package engine is the deterministic pusher. It walks a predefined DAG,
// dispatches nodes to a Worker, and drives the durable substrate with no AI in
// the loop. Retry/fallback, needs_human suspension, crash recovery and
// exactly-once side effects all live here, expressed against the durable.Handle.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/trace"
)

// Engine orchestrates runs. It is deterministic: given the same definition and
// the same recorded step outputs, re-running the body reconstructs the same plan.
type Engine struct {
	exec      durable.Executor
	disp      Dispatcher
	st        store.Store // read-only here: used by ReplayNode to rebuild recorded state
	reg       map[string]*pipelines.Pipeline
	log       *slog.Logger
	authorize func(contracts.Caller, string) error // optional policy gate; nil = allow all
}

// New builds an Engine over a durable executor, a node dispatcher, and the store
// (the same one the executor uses; the engine reads it for replay/inspection).
func New(exec durable.Executor, disp Dispatcher, st store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Engine{exec: exec, disp: disp, st: st, reg: map[string]*pipelines.Pipeline{}, log: log}
}

// Register makes a pipeline definition invocable by its id.
func (e *Engine) Register(p *pipelines.Pipeline) { e.reg[p.Pipeline] = p }

// SetAuthorizer installs an optional policy gate. If it returns an error for a
// (caller, pipeline) pair, Invoke fails with `unauthorized` and never runs it.
func (e *Engine) SetAuthorizer(fn func(contracts.Caller, string) error) { e.authorize = fn }

// Registered returns the ids of all registered pipelines (sorted).
func (e *Engine) Registered() []string {
	out := make([]string, 0, len(e.reg))
	for id := range e.reg {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Invoke starts (or replays) a run and returns the three-state result. An
// infra-level error is returned separately; the result itself is always a valid
// three-state value the host can act on.
func (e *Engine) Invoke(ctx context.Context, inv contracts.Invocation) (result contracts.InvocationResult, err error) {
	ctx, endRun := trace.StartRun(ctx, inv.PipelineID)
	defer func() { endRun(string(result.Status), result.Reason) }()

	p, ok := e.reg[inv.PipelineID]
	if !ok {
		return failedResult(contracts.ReasonUnknownPipeline + ":" + inv.PipelineID), nil
	}
	if e.authorize != nil {
		if err := e.authorize(inv.Caller, inv.PipelineID); err != nil {
			return failedResult(contracts.ReasonUnauthorized), nil
		}
	}
	if err := pipelines.ValidateJSONAgainstSchema(p.InputSchema, inv.Params); err != nil {
		// Caller's own params; a concise classified reason is fine (not raw).
		return failedResult(contracts.ReasonInvalidParams), nil
	}
	body, err := e.buildBody(p, inv.Params)
	if err != nil {
		e.log.Error("build body failed", "pipeline", inv.PipelineID, "err", err)
		return failedResult(contracts.ReasonInternal), err
	}
	out, err := e.exec.Run(ctx, durable.RunSpec{
		PipelineID:     inv.PipelineID,
		IdempotencyKey: idempotencyOrDefault(inv),
		Input:          inv.Params,
	}, body)
	if err != nil {
		e.log.Error("run failed", "pipeline", inv.PipelineID, "err", err)
		return failedResult(contracts.ReasonInternal), err
	}
	return toInvocationResult(out), nil
}

// Resume continues a suspended (needs_human) run.
func (e *Engine) Resume(ctx context.Context, resumeToken string, humanInput json.RawMessage) (contracts.InvocationResult, error) {
	out, err := e.exec.Resume(ctx, resumeToken, humanInput, e.bodyFor)
	if err != nil {
		e.log.Error("resume failed", "err", err)
		return failedResult(contracts.ReasonInternal), err
	}
	return toInvocationResult(out), nil
}

// Recover re-drives runs left pending by a crash. Call this on startup.
func (e *Engine) Recover(ctx context.Context) (int, error) {
	return e.exec.Recover(ctx, e.bodyFor)
}

// bodyFor rebuilds a workflow body from a persisted run (used by Resume/Recover).
func (e *Engine) bodyFor(pipelineID string, input json.RawMessage) (durable.WorkflowFunc, error) {
	p, ok := e.reg[pipelineID]
	if !ok {
		return nil, fmt.Errorf("unknown pipeline %q", pipelineID)
	}
	return e.buildBody(p, input)
}

// buildBody compiles a pipeline definition + params into the deterministic
// workflow body. Each node is one memoized Step; on replay/recovery completed
// nodes short-circuit and the run context is rebuilt from recorded outputs.
func (e *Engine) buildBody(p *pipelines.Pipeline, params json.RawMessage) (durable.WorkflowFunc, error) {
	order, err := p.TopoOrder()
	if err != nil {
		return nil, err
	}
	paramsMap := map[string]any{}
	if len(params) > 0 {
		if err := sonic.Unmarshal(params, &paramsMap); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
	}

	return func(ctx context.Context, h durable.Handle) (json.RawMessage, error) {
		runCtx := map[string]any{"params": paramsMap}
		for _, node := range order {
			node := node
			out, err := h.Step(ctx, node.ID, func(ctx context.Context) (json.RawMessage, error) {
				return e.runNode(ctx, h, node, runCtx)
			})
			if err != nil {
				return nil, err // ErrSuspended or *Failure — propagate to the executor
			}
			var m map[string]any
			if len(out) > 0 {
				if err := sonic.Unmarshal(out, &m); err != nil {
					return nil, &durable.Failure{Reason: node.ID + ":bad_output_json"}
				}
			}
			runCtx[node.ID] = m
		}
		outMap, err := pipelines.ResolveMap(p.Output, runCtx)
		if err != nil {
			return nil, &durable.Failure{Reason: "output_resolve_failed"}
		}
		return sonic.Marshal(outMap)
	}, nil
}

// runNode executes a single node: resolve inputs, enforce exactly-once for side
// effects, then run the attempt loop where `attempt` both retries and slides
// down the primary→backup model chain.
func (e *Engine) runNode(ctx context.Context, h durable.Handle, node pipelines.Node, runCtx map[string]any) (out json.RawMessage, rerr error) {
	ctx, endNode := trace.StartNode(ctx, node.ID, string(node.Kind))
	defer func() {
		status, reason := "ok", ""
		switch {
		case errors.Is(rerr, durable.ErrSuspended):
			status = "needs_human"
		case rerr != nil:
			status = "failed"
			var f *durable.Failure
			if errors.As(rerr, &f) {
				reason = f.Reason
			}
		}
		endNode(status, reason)
	}()

	inputs, err := pipelines.ResolveMap(node.Inputs, runCtx)
	if err != nil {
		return nil, &durable.Failure{Reason: node.ID + ":" + contracts.ReasonInputResolve}
	}

	// Exactly-once side effects: claim the idempotency key before doing the work;
	// if already done, replay the recorded result instead of re-dispatching.
	var sideKey string
	if node.SideEffect {
		sk, err := pipelines.ResolveValue(node.IdempotencyKey, runCtx)
		if err != nil {
			return nil, &durable.Failure{Reason: node.ID + ":idempotency_key_failed"}
		}
		sideKey = fmt.Sprint(sk)
		claimed, existing, err := h.ClaimSideEffect(ctx, sideKey)
		if err != nil {
			return nil, &durable.Failure{Reason: node.ID + ":side_effect_claim_failed"}
		}
		if !claimed && len(existing) > 0 {
			h.Emit(ctx, node.ID, 0, "side_effect_deduped", mustJSON(map[string]any{"idempotency_key": sideKey}))
			return existing, nil
		}
	}

	humanRaw, hasHuman, _ := h.HumanInput(ctx, node.ID)

	maxAtt := node.MaxAttempts()
	for attempt := 1; attempt <= maxAtt; attempt++ {
		model := node.ExecProfile().ModelForAttempt(attempt)

		taskInputs := cloneMap(inputs)
		if hasHuman {
			taskInputs["_human"] = json.RawMessage(humanRaw)
		}
		inputsJSON, _ := sonic.Marshal(taskInputs)
		schemaJSON, _ := sonic.Marshal(node.OutputSchema)

		task := contracts.NodeTask{
			RunID:        h.RunID(),
			NodeID:       node.ID,
			Kind:         node.Kind,
			Profile:      node.ExecProfile(),
			Inputs:       inputsJSON,
			OutputSchema: schemaJSON,
			RequiresCaps: node.RequiresCaps,
			Limits:       node.ContractLimits(),
			Attempt:      attempt,
			LeaseID:      newLease(),
		}

		e.log.Info("node start", "run", h.RunID(), "node", node.ID, "attempt", attempt, "model", model)
		h.Emit(ctx, node.ID, attempt, "node_started", mustJSON(map[string]any{"model": model, "kind": string(node.Kind)}))

		// Enforce the node's timeout (limits.timeout_sec): the Worker returns a
		// retriable timeout if it overruns, so timeout folds into retry/fallback.
		res, derr := e.dispatchWithTimeout(ctx, node, task)
		if derr != nil {
			// Transport/worker infra failure → treat as a retriable failure.
			h.Emit(ctx, node.ID, attempt, "node_dispatch_error", mustJSON(map[string]any{"error": contracts.ReasonDispatchFailed}))
			if attempt < maxAtt && node.RetriesOn("retriable") {
				continue
			}
			return nil, &durable.Failure{Reason: node.ID + ":" + contracts.ReasonDispatchFailed}
		}

		switch res.Status {
		case contracts.StatusOK:
			e.log.Info("node ok", "run", h.RunID(), "node", node.ID, "attempt", attempt, "model", model)
			// Redact the observability copy only; the output committed by Step
			// (operation_outputs) stays raw for downstream resolution and replay.
			h.Emit(ctx, node.ID, attempt, "node_ok", trace.Redact(res.Output))
			if node.SideEffect && sideKey != "" {
				_ = h.FinishSideEffect(ctx, sideKey, res.Output)
			}
			return res.Output, nil

		case contracts.StatusNeedsHuman:
			what := ""
			if res.Handoff != nil {
				what = res.Handoff.What
			}
			if what == "" && node.OnNeedsHuman != nil {
				what = node.OnNeedsHuman.Handoff
			}
			e.log.Info("node needs human", "run", h.RunID(), "node", node.ID)
			h.Emit(ctx, node.ID, attempt, "node_needs_human", mustJSON(map[string]any{"what": what}))
			return nil, h.Suspend(ctx, node.ID, what)

		case contracts.StatusFailed:
			e.log.Info("node failed", "run", h.RunID(), "node", node.ID, "attempt", attempt, "reason", res.Reason, "retriable", res.Retriable)
			h.Emit(ctx, node.ID, attempt, "node_failed", mustJSON(map[string]any{"reason": res.Reason, "retriable": res.Retriable}))
			if res.Retriable && attempt < maxAtt && node.RetriesOn("retriable") {
				continue // next attempt slides to the backup model
			}
			return nil, &durable.Failure{Reason: classifyFailure(node, res.Reason)}

		default:
			return nil, &durable.Failure{Reason: node.ID + ":unknown_status"}
		}
	}
	return nil, &durable.Failure{Reason: node.ID + ":" + contracts.ReasonRetriesExhausted}
}

// dispatchWithTimeout enforces a node's limits.timeout_sec around dispatch.
func (e *Engine) dispatchWithTimeout(ctx context.Context, node pipelines.Node, task contracts.NodeTask) (contracts.NodeResult, error) {
	if node.Limits.TimeoutSec <= 0 {
		return e.disp.Dispatch(ctx, task)
	}
	dctx, cancel := context.WithTimeout(ctx, time.Duration(node.Limits.TimeoutSec)*time.Second)
	defer cancel()
	return e.disp.Dispatch(dctx, task)
}

// helpers

func toInvocationResult(o durable.Outcome) contracts.InvocationResult {
	return contracts.InvocationResult{
		RunID:   o.RunID,
		Status:  o.Status,
		Output:  o.Output,
		Reason:  o.Reason,
		Handoff: o.Handoff,
	}
}

func failedResult(reason string) contracts.InvocationResult {
	return contracts.InvocationResult{Status: contracts.StatusFailed, Reason: reason}
}

// classifyFailure keeps reasons agent-actionable and human-diagnosable while
// never leaking a raw error or stack across the boundary. A node's reported
// reason is sanitized: only a short, single-token classification survives;
// anything stack-like is collapsed to node_error.
func classifyFailure(node pipelines.Node, reason string) string {
	return node.ID + ":" + sanitizeReason(reason)
}

// sanitizeReason scrubs anything that looks like a raw error/stack, so the
// three-state boundary only ever exposes a classified token.
func sanitizeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return contracts.ReasonNodeError
	}
	if len(reason) > 64 ||
		strings.ContainsAny(reason, "\n\t ") ||
		strings.Contains(reason, "panic") ||
		strings.Contains(reason, "goroutine") ||
		strings.Contains(reason, "0x") {
		return contracts.ReasonNodeError
	}
	return reason
}

func idempotencyOrDefault(inv contracts.Invocation) string {
	if inv.IdempotencyKey != "" {
		return inv.IdempotencyKey
	}
	return "auto_" + newLease()
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, _ := sonic.Marshal(v)
	return b
}

func newLease() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// discard is an io.Writer that drops everything (default no-op logger sink).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
