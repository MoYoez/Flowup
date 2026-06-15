package dispatch

import (
	"context"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/store"
)

// Runner executes a claimed NodeTask (e.g. a model run inside a sandbox). The
// Router (sandbox for native, model+tools for semantic/agent) satisfies it.
type Runner interface {
	Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error)
}

// Worker pulls matching tasks from the queue, runs them via its Runner, and
// writes results back — holding a lease for the duration. One container ≈ one
// Worker; capability routing keeps privileged work on the right machines.
type Worker struct {
	ID      string
	Caps    []string
	store   store.Store
	runner  Runner
	LeaseMS int64
	Poll    time.Duration
}

// NewWorker builds a puller worker advertising the given capabilities.
func NewWorker(id string, caps []string, st store.Store, runner Runner) *Worker {
	return &Worker{ID: id, Caps: caps, store: st, runner: runner, LeaseMS: 2000, Poll: 5 * time.Millisecond}
}

// Run is the pull loop; it blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		w.Tick(ctx)
	}
}

// Tick claims and runs at most one matching task. Returns true if it ran one.
// Exposed so tests can step a worker deterministically.
func (w *Worker) Tick(ctx context.Context) bool {
	rec, ok, err := w.store.ClaimTask(ctx, w.ID, w.Caps, w.LeaseMS)
	if err != nil || !ok {
		return false // nothing matched (backpressure) or a transient error
	}
	var task contracts.NodeTask
	if err := sonic.Unmarshal(rec.Payload, &task); err != nil {
		_, _ = w.store.CompleteTask(ctx, rec.TaskID, w.ID, store.TaskFailed, mustResult(contracts.NodeResult{
			RunID: rec.RunID, NodeID: rec.NodeID, Status: contracts.StatusFailed, Reason: "bad_task_payload",
		}))
		return true
	}
	res, _ := w.runner.Dispatch(ctx, task)
	// The NodeResult carries the three-state; transport-wise the task is done.
	// CompleteTask is a no-op if this worker's lease was reclaimed (late result).
	_, _ = w.store.CompleteTask(ctx, rec.TaskID, w.ID, store.TaskDone, mustResult(res))
	return true
}

func mustResult(r contracts.NodeResult) []byte {
	b, _ := sonic.Marshal(r)
	return b
}

// Reap periodically requeues tasks whose lease expired (dead workers). It blocks
// until ctx is cancelled. Run it once per deployment.
func Reap(ctx context.Context, st store.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = st.ReclaimExpiredTasks(ctx)
		}
	}
}
