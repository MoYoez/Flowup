package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	approvalaction "github.com/moyoez/flowup/internal/action/approval"
	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

func approvalRegistry(t *testing.T, order *[]string) *action.Registry {
	t.Helper()
	registry, err := action.NewRegistry(
		approvalaction.New(),
		recordingAction{name: "record", order: order},
	)
	require.NoError(t, err)
	return registry
}

func TestApprovalPauseSurvivesReopenAndApproveResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowup.db")
	state, err := store.OpenSQLite(path)
	require.NoError(t, err)
	var order []string
	engine := fixedEngine(state, approvalRegistry(t, &order))
	source := []byte(`
name: approval
version: 1
steps:
  - id: approve
    uses: approval
    with:
      message: Send alert
      preview: {priority: high}
  - id: notify
    uses: record
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.NoError(t, err)
	require.Equal(t, store.RunWaitingApproval, run.Status)
	require.Empty(t, order)
	approval, err := state.GetApproval(context.Background(), "approval_fixed")
	require.NoError(t, err)
	require.Equal(t, store.ApprovalPending, approval.Status)
	require.NoError(t, state.Close())

	state, err = store.OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = state.Close() })
	engine = fixedEngine(state, approvalRegistry(t, &order))
	run, err = engine.Approve(context.Background(), approval.ID)

	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, []string{"notify"}, order)
	step, err := state.GetStep(context.Background(), run.ID, "approve")
	require.NoError(t, err)
	require.Equal(t, store.StepApproved, step.Status)
	require.JSONEq(t, `{"approved":true}`, string(step.Output))
}

func TestApprovalRejectTerminatesRun(t *testing.T) {
	state := testStore(t)
	var order []string
	engine := fixedEngine(state, approvalRegistry(t, &order))
	run, err := engine.Start(context.Background(), []byte(`
name: rejection
version: 1
steps:
  - id: approve
    uses: approval
    with: {message: Send alert}
  - id: notify
    uses: record
`), nil)
	require.NoError(t, err)

	run, err = engine.Reject(context.Background(), "approval_fixed", "not now")

	require.NoError(t, err)
	require.Equal(t, store.RunRejected, run.Status)
	require.Empty(t, order)
	step, err := state.GetStep(context.Background(), run.ID, "approve")
	require.NoError(t, err)
	require.Equal(t, store.StepRejected, step.Status)
	var output map[string]any
	require.NoError(t, json.Unmarshal(step.Output, &output))
	require.Equal(t, false, output["approved"])
	require.Equal(t, "not now", output["reason"])
}

func TestApprovalCannotBeDecidedTwice(t *testing.T) {
	state := testStore(t)
	engine := fixedEngine(state, approvalRegistry(t, nil))
	_, err := engine.Start(context.Background(), []byte(`
name: approval
version: 1
steps:
  - id: approve
    uses: approval
    with: {message: Send alert}
`), nil)
	require.NoError(t, err)
	_, err = engine.Reject(context.Background(), "approval_fixed", "no")
	require.NoError(t, err)

	_, err = engine.Approve(context.Background(), "approval_fixed")

	require.Error(t, err)
}
