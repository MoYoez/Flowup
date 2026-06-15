package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

type recordingAction struct {
	name         string
	order        *[]string
	outputs      map[string]json.RawMessage
	outputSchema map[string]any
}

func (a recordingAction) Definition() action.Definition {
	outputSchema := a.outputSchema
	if outputSchema == nil {
		outputSchema = map[string]any{}
	}
	return action.Definition{
		Name:         a.name,
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: outputSchema,
		Timeout:      time.Second,
	}
}

func (recordingAction) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (a recordingAction) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	if a.order != nil {
		*a.order = append(*a.order, invocation.StepID)
	}
	output := json.RawMessage(`null`)
	if configured, ok := a.outputs[invocation.StepID]; ok {
		output = configured
	}
	return action.Result{Output: output}, nil
}

func testStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "flowup.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testRegistry(t *testing.T, actions ...action.Action) *action.Registry {
	t.Helper()
	registry, err := action.NewRegistry(actions...)
	require.NoError(t, err)
	return registry
}

func fixedEngine(s store.Store, registry *action.Registry) *Engine {
	return New(
		s,
		registry,
		withClock(func() time.Time { return time.Unix(100, 0).UTC() }),
		withIDGenerator(func(prefix string) string { return prefix + "_fixed" }),
	)
}

func TestEngineRunsStepsInDeclarationOrder(t *testing.T) {
	var order []string
	s := testStore(t)
	engine := fixedEngine(s, testRegistry(t, recordingAction{name: "record", order: &order}))
	source := []byte(`
name: ordered
version: 1
steps:
  - id: first
    uses: record
  - id: second
    uses: record
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, []string{"first", "second"}, order)
	steps, err := s.ListSteps(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, []store.StepStatus{store.StepSucceeded, store.StepSucceeded}, []store.StepStatus{steps[0].Status, steps[1].Status})
}

func TestEngineSkipsFalseCondition(t *testing.T) {
	var order []string
	s := testStore(t)
	engine := fixedEngine(s, testRegistry(t, recordingAction{name: "record", order: &order}))
	source := []byte(`
name: conditional
version: 1
inputs:
  enabled: {type: boolean, required: true}
steps:
  - id: first
    uses: record
  - id: second
    uses: record
    if: ${{ inputs.enabled == true }}
`)

	run, err := engine.Start(context.Background(), source, map[string]any{"enabled": false})

	require.NoError(t, err)
	require.Equal(t, []string{"first"}, order)
	steps, err := s.ListSteps(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, store.StepSkipped, steps[1].Status)
}

func TestEngineBuildsWorkflowOutput(t *testing.T) {
	s := testStore(t)
	engine := fixedEngine(s, testRegistry(t, recordingAction{
		name:    "record",
		outputs: map[string]json.RawMessage{"classify": json.RawMessage(`{"priority":"high"}`)},
	}))
	source := []byte(`
name: output
version: 1
steps:
  - id: classify
    uses: record
outputs:
  priority: ${{ steps.classify.output.priority }}
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.NoError(t, err)
	require.JSONEq(t, `{"priority":"high"}`, string(run.Output))
}

func TestEngineRejectsInvalidActionOutput(t *testing.T) {
	s := testStore(t)
	engine := fixedEngine(s, testRegistry(t, recordingAction{
		name:         "record",
		outputs:      map[string]json.RawMessage{"classify": json.RawMessage(`{"priority":3}`)},
		outputSchema: map[string]any{"type": "object", "properties": map[string]any{"priority": map[string]any{"type": "string"}}},
	}))
	source := []byte(`
name: invalid-output
version: 1
steps:
  - id: classify
    uses: record
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.Error(t, err)
	require.Equal(t, CodeActionOutput, errorCode(err))
	require.Equal(t, store.RunFailed, run.Status)
	step, getErr := s.GetStep(context.Background(), run.ID, "classify")
	require.NoError(t, getErr)
	require.Equal(t, store.StepFailed, step.Status)
}

func TestEngineResumeDoesNotRepeatSucceededSteps(t *testing.T) {
	var order []string
	s := testStore(t)
	registry := testRegistry(t, recordingAction{name: "record", order: &order})
	engine := fixedEngine(s, registry)
	source := []byte(`
name: resume
version: 1
steps:
  - id: first
    uses: record
  - id: second
    uses: record
`)
	now := time.Unix(100, 0).UTC()
	require.NoError(t, s.CreateRun(context.Background(), store.RunRecord{
		ID: "run-resume", WorkflowName: "resume", WorkflowVersion: 1,
		WorkflowYAML: source, Inputs: json.RawMessage(`{}`), CurrentStep: 1,
		Status: store.RunRunning, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.PutStep(context.Background(), store.StepRecord{
		RunID: "run-resume", StepID: "first", StepIndex: 0, ActionName: "record",
		Status: store.StepSucceeded, Attempt: 1, Output: json.RawMessage(`null`),
		StartedAt: now, FinishedAt: now,
	}))

	run, err := engine.Resume(context.Background(), "run-resume")

	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, []string{"second"}, order)
}

func errorCode(err error) string {
	var coded *Error
	if !AsError(err, &coded) {
		return ""
	}
	return coded.Code
}
