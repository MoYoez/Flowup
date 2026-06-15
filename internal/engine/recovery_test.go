package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/policy"
	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

type countingEffectAction struct {
	calls int
}

func (*countingEffectAction) Definition() action.Definition {
	return action.Definition{
		Name:         "effect",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object"},
		Timeout:      time.Second,
	}
}

func (*countingEffectAction) Effect(map[string]any) action.EffectClass {
	return action.EffectExternal
}

func (a *countingEffectAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	a.calls++
	return action.Result{Output: json.RawMessage(`{"sent":true}`)}, nil
}

type secretCaptureAction struct {
	received string
}

func (a *secretCaptureAction) Definition() action.Definition {
	return action.Definition{
		Name:         "secret.capture",
		InputSchema:  map[string]any{"type": "object", "required": []any{"token"}, "properties": map[string]any{"token": map[string]any{"type": "string"}}},
		OutputSchema: map[string]any{},
		SecretPaths:  []string{"token"},
		Timeout:      time.Second,
	}
}

func (*secretCaptureAction) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (a *secretCaptureAction) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	a.received, _ = invocation.Input["token"].(string)
	return action.Result{Output: json.RawMessage(`null`)}, nil
}

func TestResumeDoesNotRepeatCompletedEffect(t *testing.T) {
	candidate := &countingEffectAction{}
	state := testStore(t)
	registry := testRegistry(t, candidate)
	engine := fixedEngine(state, registry)
	runID, source := prepareInterruptedEffectRun(t, state)
	_, _, err := state.BeginEffect(context.Background(), store.EffectRecord{
		Key: runID + ":send", RunID: runID, StepID: "send",
		ActionName: "effect", Status: store.EffectStarted, CreatedAt: time.Unix(100, 0).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, state.CompleteEffect(context.Background(), runID+":send", json.RawMessage(`{"sent":true}`), time.Unix(101, 0).UTC()))
	_ = source

	run, err := engine.Resume(context.Background(), runID)

	require.NoError(t, err)
	require.Equal(t, 0, candidate.calls)
	require.Equal(t, store.RunSucceeded, run.Status)
}

func TestResumeFailsIndeterminateStartedEffect(t *testing.T) {
	candidate := &countingEffectAction{}
	state := testStore(t)
	engine := fixedEngine(state, testRegistry(t, candidate))
	runID, _ := prepareInterruptedEffectRun(t, state)
	_, _, err := state.BeginEffect(context.Background(), store.EffectRecord{
		Key: runID + ":send", RunID: runID, StepID: "send",
		ActionName: "effect", Status: store.EffectStarted, CreatedAt: time.Unix(100, 0).UTC(),
	})
	require.NoError(t, err)

	run, err := engine.Resume(context.Background(), runID)

	require.Error(t, err)
	require.Equal(t, CodeEffectIndeterminate, errorCode(err))
	require.Equal(t, 0, candidate.calls)
	require.Equal(t, store.RunFailed, run.Status)
}

func TestEngineResolvesSecretOnlyForActionAndPersistsRedactedInput(t *testing.T) {
	candidate := &secretCaptureAction{}
	state := testStore(t)
	registry := testRegistry(t, candidate)
	engine := New(
		state,
		registry,
		withClock(func() time.Time { return time.Unix(100, 0).UTC() }),
		withIDGenerator(func(prefix string) string { return prefix + "_secret" }),
		WithSecrets(policy.MapSecrets{"TOKEN": "secret-value"}),
	)

	run, err := engine.Start(context.Background(), []byte(`
name: secret
version: 1
steps:
  - id: capture
    uses: secret.capture
    with:
      token: ${{ secrets.TOKEN }}
`), nil)

	require.NoError(t, err)
	require.Equal(t, "secret-value", candidate.received)
	step, err := state.GetStep(context.Background(), run.ID, "capture")
	require.NoError(t, err)
	require.NotContains(t, string(step.Input), "secret-value")
	require.Contains(t, string(step.Input), "[REDACTED]")
	events, err := state.ListEvents(context.Background(), run.ID)
	require.NoError(t, err)
	rawEvents, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(rawEvents), "secret-value")
}

func prepareInterruptedEffectRun(t *testing.T, state store.Store) (string, []byte) {
	t.Helper()
	runID := "run-effect"
	source := []byte(`
name: effect
version: 1
steps:
  - id: send
    uses: effect
`)
	now := time.Unix(100, 0).UTC()
	require.NoError(t, state.CreateRun(context.Background(), store.RunRecord{
		ID: runID, WorkflowName: "effect", WorkflowVersion: 1,
		WorkflowYAML: source, Inputs: json.RawMessage(`{}`), Status: store.RunRunning,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, state.PutStep(context.Background(), store.StepRecord{
		RunID: runID, StepID: "send", StepIndex: 0, ActionName: "effect",
		Status: store.StepRunning, Attempt: 1, Input: json.RawMessage(`{}`), StartedAt: now,
	}))
	return runID, source
}
