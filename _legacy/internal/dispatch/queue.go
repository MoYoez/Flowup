// Package dispatch is the transport layer: engine-to-Worker dispatch over a
// durable, pull-based task queue. It needs no broker — the queue rides on the
// same store (SQLite or Postgres). It sits behind the engine's Dispatcher seam,
// so the engine is unaware that nodes run on a Worker pool with leases instead
// of in-process.
//
// The pull model makes Worker capacity a first-class signal: busy workers don't
// claim, idle ones do, so backpressure and buffering come for free.
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/store"
)

// QueueDispatcher enqueues a NodeTask and waits for a Worker to produce its
// result. It implements engine.Dispatcher.
type QueueDispatcher struct {
	st   store.Store
	poll time.Duration
}

// NewQueueDispatcher builds a dispatcher over the durable store.
func NewQueueDispatcher(st store.Store) *QueueDispatcher {
	return &QueueDispatcher{st: st, poll: 5 * time.Millisecond}
}

// TaskID is the stable, idempotent key for a node attempt.
func TaskID(t contracts.NodeTask) string {
	return fmt.Sprintf("%s:%s:%d", t.RunID, t.NodeID, t.Attempt)
}

// retryBusy absorbs transient store contention (cross-process SQLite WAL can
// surface "database is locked"/"busy" past busy_timeout). Harmless for Postgres
// and the single-process path, which never hit it.
func retryBusy(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = fn(); err == nil || !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(10*(i+1)) * time.Millisecond):
		}
	}
	return err
}

func isBusy(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "locked") || strings.Contains(s, "busy")
}

// Dispatch enqueues the task and blocks until a Worker writes a result, the
// engine's per-node timeout fires (ctx), or an error occurs.
func (d *QueueDispatcher) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	id := TaskID(task)
	payload, _ := sonic.Marshal(task)
	if err := retryBusy(ctx, func() error {
		return d.st.EnqueueTask(ctx, store.TaskRecord{
			TaskID:  id,
			RunID:   task.RunID,
			NodeID:  task.NodeID,
			Attempt: task.Attempt,
			Caps:    strings.Join(task.RequiresCaps, ","),
			Payload: payload,
			Status:  store.TaskQueued,
		})
	}); err != nil {
		return contracts.NodeResult{}, err
	}

	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		var got store.TaskRecord
		var ok bool
		err := retryBusy(ctx, func() error {
			var e error
			got, ok, e = d.st.GetTask(ctx, id)
			return e
		})
		if err != nil {
			return contracts.NodeResult{}, err
		}
		if ok && (got.Status == store.TaskDone || got.Status == store.TaskFailed) && len(got.Result) > 0 {
			var res contracts.NodeResult
			if err := sonic.Unmarshal(got.Result, &res); err != nil {
				return contracts.NodeResult{}, fmt.Errorf("decode task result: %w", err)
			}
			return res, nil
		}
		select {
		case <-ctx.Done():
			// The engine's per-node timeout (visibility timeout) fired: report a
			// retriable timeout, mirroring the in-process Worker.
			return contracts.NodeResult{
				RunID:     task.RunID,
				NodeID:    task.NodeID,
				Status:    contracts.StatusFailed,
				Reason:    contracts.ReasonTimeout,
				Retriable: true,
			}, nil
		case <-t.C:
		}
	}
}
