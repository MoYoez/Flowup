// Package faketest is a controllable in-process dispatcher for tests only. It
// lets a test inject deterministic node behavior — failures on chosen attempts,
// needs_human, side-effect counters — which is how the engine's invariants
// (retry/fallback, suspend/resume, crash-replay, idempotency) are exercised
// without a real, non-deterministic model. Real end-to-end behavior is covered
// by internal/integration. Imported only by _test.go files.
package faketest

import (
	"context"
	"sync"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/pipelines"
)

// NodeHandler simulates one node attempt. It receives the full NodeTask (so it
// can branch on Attempt, Inputs, Profile, etc.).
type NodeHandler func(task contracts.NodeTask) contracts.NodeResult

// Fake is an in-process Dispatcher driven by per-node handlers.
type Fake struct {
	mu       sync.Mutex
	handlers map[string]NodeHandler
	calls    map[string]int // node id → times actually dispatched (NOT replayed)
}

// New returns an empty fake dispatcher; register node behavior with SetHandler.
func New() *Fake {
	return &Fake{handlers: map[string]NodeHandler{}, calls: map[string]int{}}
}

// SetHandler registers behavior for a node id.
func (w *Fake) SetHandler(nodeID string, h NodeHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[nodeID] = h
}

// Calls reports how many times a node was actually dispatched to the worker.
// A replayed (memoized) node is NOT dispatched, so this is how a test proves a
// recovered AI node was not re-run.
func (w *Fake) Calls(nodeID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[nodeID]
}

// Dispatch implements engine.Dispatcher.
func (w *Fake) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	w.mu.Lock()
	w.calls[task.NodeID]++
	h := w.handlers[task.NodeID]
	w.mu.Unlock()

	if h == nil {
		h = func(contracts.NodeTask) contracts.NodeResult { return OK(map[string]any{}) }
	}

	// Run the handler off-thread so we can honor the engine's per-node timeout
	// (the NodeTask visibility timeout): if ctx fires first, return a retriable
	// timeout — the engine retries and may slide to the backup model. The handler
	// goroutine drains into the buffered channel and exits, so it never leaks.
	resCh := make(chan contracts.NodeResult, 1)
	go func() { resCh <- h(task) }()

	var res contracts.NodeResult
	select {
	case res = <-resCh:
	case <-ctx.Done():
		return contracts.NodeResult{
			RunID:     task.RunID,
			NodeID:    task.NodeID,
			LeaseID:   task.LeaseID,
			Status:    contracts.StatusFailed,
			Reason:    contracts.ReasonTimeout,
			Retriable: true,
		}, nil
	}
	res.RunID = task.RunID
	res.NodeID = task.NodeID
	res.LeaseID = task.LeaseID

	// Contract: validate output against output_schema on the Worker side, before
	// returning. A schema violation becomes a (non-retriable) failed result —
	// never a raw error across the boundary.
	if res.Status == contracts.StatusOK && len(task.OutputSchema) > 0 {
		var schema map[string]any
		if err := sonic.Unmarshal(task.OutputSchema, &schema); err == nil {
			if err := pipelines.ValidateJSONAgainstSchema(schema, res.Output); err != nil {
				return contracts.NodeResult{
					RunID:   task.RunID,
					NodeID:  task.NodeID,
					LeaseID: task.LeaseID,
					Status:  contracts.StatusFailed,
					Reason:  "output_schema_violation",
				}, nil
			}
		}
	}
	return res, nil
}

// NodeResult constructors

// OK builds an ok result with a JSON-object output.
func OK(output map[string]any) contracts.NodeResult {
	b, _ := sonic.Marshal(output)
	return contracts.NodeResult{Status: contracts.StatusOK, Output: b}
}

// Failed builds a failed result with a classified reason.
func Failed(reason string, retriable bool) contracts.NodeResult {
	return contracts.NodeResult{Status: contracts.StatusFailed, Reason: reason, Retriable: retriable}
}

// NeedsHuman builds a needs_human result with a handoff explanation.
func NeedsHuman(what string) contracts.NodeResult {
	return contracts.NodeResult{Status: contracts.StatusNeedsHuman, Handoff: &contracts.Handoff{What: what}}
}
