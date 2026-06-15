package durable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/store"
)

// selfBuilt is the minimal event-sourcing Executor. It rides step checkpoints on
// the Store: a step's output is written once and atomically, so a process kill
// after a commit leaves a recoverable, replay-not-rerun state.
type selfBuilt struct {
	st store.Store
}

// NewSelfBuilt returns the self-built event-sourcing durable executor.
func NewSelfBuilt(st store.Store) Executor { return &selfBuilt{st: st} }

func (e *selfBuilt) Run(ctx context.Context, spec RunSpec, body WorkflowFunc) (Outcome, error) {
	rec := store.RunRecord{
		RunID:          genID("run"),
		PipelineID:     spec.PipelineID,
		IdempotencyKey: spec.IdempotencyKey,
		Status:         store.RunPending,
		Input:          spec.Input,
	}
	created, existing, err := e.st.CreateRun(ctx, rec)
	if err != nil {
		return Outcome{}, err
	}
	if !created {
		// Same idempotency key. If the prior run is terminal or suspended, return
		// its recorded result — no re-run, no duplicate side effects. If it is
		// still pending (e.g. crashed mid-run), re-drive it.
		if existing.Status != store.RunPending {
			return outcomeFromRecord(existing), nil
		}
		rec = existing
	}
	return e.drive(ctx, rec, body)
}

func (e *selfBuilt) Resume(ctx context.Context, token string, humanInput json.RawMessage, bodyFor BodyForRun) (Outcome, error) {
	rec, ok, err := e.st.GetRunByResumeToken(ctx, token)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("resume: unknown resume_token")
	}
	if rec.Status == store.RunOK || rec.Status == store.RunFailed {
		return outcomeFromRecord(rec), nil // already resolved; idempotent resume
	}
	if rec.SuspendedNode != "" && humanInput != nil {
		if err := e.st.PutHumanInput(ctx, rec.RunID, rec.SuspendedNode, humanInput); err != nil {
			return Outcome{}, err
		}
	}
	body, err := bodyFor(rec.PipelineID, rec.Input)
	if err != nil {
		return Outcome{}, fmt.Errorf("resume: rebuild body: %w", err)
	}
	rec.Status = store.RunPending
	if err := e.st.UpdateRun(ctx, rec); err != nil {
		return Outcome{}, err
	}
	return e.drive(ctx, rec, body)
}

func (e *selfBuilt) Recover(ctx context.Context, bodyFor BodyForRun) (int, error) {
	pending, err := e.st.ListPendingRuns(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range pending {
		body, err := bodyFor(rec.PipelineID, rec.Input)
		if err != nil {
			// Can't rebuild this run's body (unknown pipeline); leave it pending.
			continue
		}
		if _, err := e.drive(ctx, rec, body); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (e *selfBuilt) Close() error { return e.st.Close() }

// drive runs the body once with a fresh handle and maps the result to the
// three-state contract, persisting the terminal/suspended record.
func (e *selfBuilt) drive(ctx context.Context, rec store.RunRecord, body WorkflowFunc) (Outcome, error) {
	h := &handle{st: e.st, runID: rec.RunID}
	out, err := body(ctx, h)

	switch {
	case err == nil:
		rec.Status = store.RunOK
		rec.Output = out
		rec.Reason = ""
		rec.SuspendedNode = ""
		rec.ResumeToken = ""
		rec.HandoffWhat = ""
		if err := e.st.UpdateRun(ctx, rec); err != nil {
			return Outcome{}, err
		}
		return Outcome{RunID: rec.RunID, Status: contracts.StatusOK, Output: out}, nil

	case errors.Is(err, ErrSuspended):
		// handle.Suspend already persisted status/token/handoff/suspended_node.
		latest, _, gerr := e.st.GetRun(ctx, rec.RunID)
		if gerr != nil {
			return Outcome{}, gerr
		}
		return Outcome{
			RunID:   rec.RunID,
			Status:  contracts.StatusNeedsHuman,
			Handoff: &contracts.Handoff{What: latest.HandoffWhat, ResumeToken: latest.ResumeToken},
		}, nil

	default:
		reason := "internal_error"
		var f *Failure
		if errors.As(err, &f) {
			reason = f.Reason
		}
		rec.Status = store.RunFailed
		rec.Reason = reason
		if uerr := e.st.UpdateRun(ctx, rec); uerr != nil {
			return Outcome{}, uerr
		}
		return Outcome{RunID: rec.RunID, Status: contracts.StatusFailed, Reason: reason}, nil
	}
}

func outcomeFromRecord(r store.RunRecord) Outcome {
	o := Outcome{RunID: r.RunID}
	switch r.Status {
	case store.RunOK:
		o.Status = contracts.StatusOK
		o.Output = r.Output
	case store.RunFailed:
		o.Status = contracts.StatusFailed
		o.Reason = r.Reason
	case store.RunNeedsHuman:
		o.Status = contracts.StatusNeedsHuman
		o.Handoff = &contracts.Handoff{What: r.HandoffWhat, ResumeToken: r.ResumeToken}
	default:
		o.Status = contracts.Status(r.Status)
	}
	return o
}

// handle

type handle struct {
	st    store.Store
	runID string
}

func (h *handle) RunID() string { return h.runID }

func (h *handle) Step(ctx context.Context, stepKey string, fn StepFunc) (json.RawMessage, error) {
	if rec, ok, err := h.st.GetStep(ctx, h.runID, stepKey); err != nil {
		return nil, err
	} else if ok {
		return rec.Output, nil // REPLAY: recorded output, fn is NOT called
	}
	out, err := fn(ctx)
	if err != nil {
		return nil, err // suspends/failures are not memoized → they re-run
	}
	if err := h.st.PutStep(ctx, store.StepRecord{RunID: h.runID, StepKey: stepKey, Output: out}); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handle) HumanInput(ctx context.Context, nodeID string) (json.RawMessage, bool, error) {
	return h.st.GetHumanInput(ctx, h.runID, nodeID)
}

func (h *handle) Suspend(ctx context.Context, nodeID, what string) error {
	rec, ok, err := h.st.GetRun(ctx, h.runID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("suspend: run %s not found", h.runID)
	}
	rec.Status = store.RunNeedsHuman
	rec.SuspendedNode = nodeID
	rec.HandoffWhat = what
	rec.ResumeToken = genID("rt")
	if err := h.st.UpdateRun(ctx, rec); err != nil {
		return err
	}
	return ErrSuspended
}

func (h *handle) Emit(ctx context.Context, nodeID string, attempt int, typ string, data json.RawMessage) {
	_ = h.st.AppendEvent(ctx, store.EventRecord{
		RunID:   h.runID,
		NodeID:  nodeID,
		Attempt: attempt,
		Type:    typ,
		Data:    data,
	})
}

func (h *handle) ClaimSideEffect(ctx context.Context, key string) (bool, json.RawMessage, error) {
	return h.st.ClaimSideEffect(ctx, key)
}

func (h *handle) FinishSideEffect(ctx context.Context, key string, result json.RawMessage) error {
	return h.st.FinishSideEffect(ctx, key, result)
}

// genID returns a short random id. crypto/rand is appropriate here (this is the
// engine process, not a replayable workflow sandbox).
func genID(prefix string) string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
