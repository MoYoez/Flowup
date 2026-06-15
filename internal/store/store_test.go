package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestSQLitePersistsRunAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowup.db")
	store := openTestStore(t, path)
	run := RunRecord{
		ID:              "run-1",
		WorkflowName:    "local",
		WorkflowVersion: 1,
		WorkflowYAML:    []byte("name: local\nversion: 1\nsteps: []\n"),
		Inputs:          json.RawMessage(`{"payload":{"x":1}}`),
		Status:          RunRunning,
		CreatedAt:       time.Unix(100, 0).UTC(),
		UpdatedAt:       time.Unix(100, 0).UTC(),
	}
	require.NoError(t, store.CreateRun(context.Background(), run))
	require.NoError(t, store.Close())

	store = openTestStore(t, path)
	got, err := store.GetRun(context.Background(), "run-1")

	require.NoError(t, err)
	require.Equal(t, run.WorkflowName, got.WorkflowName)
	require.Equal(t, run.WorkflowVersion, got.WorkflowVersion)
	require.Equal(t, run.WorkflowYAML, got.WorkflowYAML)
	require.JSONEq(t, string(run.Inputs), string(got.Inputs))
	require.Equal(t, RunRunning, got.Status)
}

func TestSQLiteUpdatesRunAndOrdersSteps(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "flowup.db"))
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	require.NoError(t, store.CreateRun(ctx, RunRecord{
		ID: "run-1", WorkflowName: "local", WorkflowVersion: 1,
		WorkflowYAML: []byte("workflow"), Inputs: json.RawMessage(`{}`),
		Status: RunRunning, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, store.PutStep(ctx, StepRecord{
		RunID: "run-1", StepID: "second", StepIndex: 1, ActionName: "switch",
		Status: StepSucceeded, Attempt: 1, Output: json.RawMessage(`"done"`),
	}))
	require.NoError(t, store.PutStep(ctx, StepRecord{
		RunID: "run-1", StepID: "first", StepIndex: 0, ActionName: "switch",
		Status: StepSkipped,
	}))

	run, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	run.Status = RunSucceeded
	run.CurrentStep = 2
	run.Output = json.RawMessage(`{"result":"done"}`)
	run.UpdatedAt = time.Unix(200, 0).UTC()
	require.NoError(t, store.UpdateRun(ctx, run))

	steps, err := store.ListSteps(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, []string{steps[0].StepID, steps[1].StepID})
	got, err := store.GetRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, RunSucceeded, got.Status)
	require.JSONEq(t, `{"result":"done"}`, string(got.Output))
}

func TestSQLiteAppendsEventsInOrder(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "flowup.db"))
	ctx := context.Background()
	require.NoError(t, store.AppendEvent(ctx, EventRecord{
		RunID: "run-1", Type: "run.started", Data: json.RawMessage(`{"n":1}`),
		CreatedAt: time.Unix(100, 0).UTC(),
	}))
	require.NoError(t, store.AppendEvent(ctx, EventRecord{
		RunID: "run-1", StepID: "route", Type: "step.succeeded", Data: json.RawMessage(`{"n":2}`),
		CreatedAt: time.Unix(101, 0).UTC(),
	}))

	events, err := store.ListEvents(ctx, "run-1")

	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Less(t, events[0].ID, events[1].ID)
	require.Equal(t, "run.started", events[0].Type)
	require.Equal(t, "step.succeeded", events[1].Type)
}

func TestSQLiteApprovalDecisionIsImmutable(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "flowup.db"))
	ctx := context.Background()
	approval := ApprovalRecord{
		ID: "approval-1", RunID: "run-1", StepID: "approve",
		Message: "Send alert", Preview: json.RawMessage(`{"priority":"high"}`),
		Status: ApprovalPending, CreatedAt: time.Unix(100, 0).UTC(),
	}
	require.NoError(t, store.CreateApproval(ctx, approval))

	decided, err := store.DecideApproval(ctx, approval.ID, ApprovalApproved, "", time.Unix(200, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, ApprovalApproved, decided.Status)
	_, err = store.DecideApproval(ctx, approval.ID, ApprovalRejected, "changed mind", time.Unix(201, 0).UTC())
	require.ErrorIs(t, err, ErrConflict)
}

func TestSQLiteEffectLifecycle(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "flowup.db"))
	ctx := context.Background()
	record := EffectRecord{
		Key: "run-1:notify", RunID: "run-1", StepID: "notify",
		ActionName: "slack.message.send", Status: EffectStarted,
		CreatedAt: time.Unix(100, 0).UTC(),
	}

	first, created, err := store.BeginEffect(ctx, record)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, EffectStarted, first.Status)
	second, created, err := store.BeginEffect(ctx, record)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, EffectStarted, second.Status)
	require.NoError(t, store.CompleteEffect(ctx, record.Key, json.RawMessage(`{"ts":"1"}`), time.Unix(200, 0).UTC()))
	completed, err := store.GetEffect(ctx, record.Key)
	require.NoError(t, err)
	require.Equal(t, EffectCompleted, completed.Status)
	require.JSONEq(t, `{"ts":"1"}`, string(completed.Output))
}
