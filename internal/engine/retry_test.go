package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

type flakyAction struct {
	name       string
	failures   int
	transient  bool
	attempts   *int
	effect     action.EffectClass
	prepareErr error
	executed   *bool
}

func (a flakyAction) Definition() action.Definition {
	return action.Definition{
		Name:         a.name,
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{},
		Timeout:      time.Second,
	}
}

func (a flakyAction) Effect(map[string]any) action.EffectClass {
	if a.effect == "" {
		return action.EffectReadOnly
	}
	return a.effect
}

func (a flakyAction) Prepare(map[string]any) error {
	return a.prepareErr
}

func (a flakyAction) Execute(_ context.Context, _ action.Invocation) (action.Result, error) {
	if a.executed != nil {
		*a.executed = true
	}
	if a.attempts != nil {
		*a.attempts++
		if *a.attempts <= a.failures {
			err := fmt.Errorf("temporary failure %d", *a.attempts)
			if a.transient {
				return action.Result{}, action.Transient(err)
			}
			return action.Result{}, err
		}
	}
	return action.Result{Output: json.RawMessage(`null`)}, nil
}

func noBackoffEngine(s store.Store, registry *action.Registry, maxAttempts int) *Engine {
	return New(
		s, registry,
		withClock(func() time.Time { return time.Unix(100, 0).UTC() }),
		withIDGenerator(func(prefix string) string { return prefix + "_fixed" }),
		WithRetry(maxAttempts, func(int) time.Duration { return 0 }),
	)
}

func TestEngineRetriesTransientReadOnlyFailures(t *testing.T) {
	var attempts int
	s := testStore(t)
	registry := testRegistry(t, flakyAction{name: "flaky", failures: 2, transient: true, attempts: &attempts})
	engine := noBackoffEngine(s, registry, 3)
	source := []byte(`
name: retry
version: 1
steps:
  - id: read
    uses: flaky
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, 3, attempts)
}

func TestEngineExhaustsRetriesThenFails(t *testing.T) {
	var attempts int
	s := testStore(t)
	registry := testRegistry(t, flakyAction{name: "flaky", failures: 5, transient: true, attempts: &attempts})
	engine := noBackoffEngine(s, registry, 3)
	source := []byte(`
name: retry
version: 1
steps:
  - id: read
    uses: flaky
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.Error(t, err)
	require.Equal(t, store.RunFailed, run.Status)
	require.Equal(t, 3, attempts)
}

func TestEngineDoesNotRetryNonTransientFailures(t *testing.T) {
	var attempts int
	s := testStore(t)
	registry := testRegistry(t, flakyAction{name: "flaky", failures: 5, transient: false, attempts: &attempts})
	engine := noBackoffEngine(s, registry, 3)
	source := []byte(`
name: retry
version: 1
steps:
  - id: read
    uses: flaky
`)

	_, err := engine.Start(context.Background(), source, nil)

	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

func TestEngineFailedPrepareLeavesNoEffectRecord(t *testing.T) {
	var executed bool
	s := testStore(t)
	registry := testRegistry(t, flakyAction{
		name:       "write",
		effect:     action.EffectExternal,
		prepareErr: fmt.Errorf("unsendable request"),
		executed:   &executed,
	})
	engine := fixedEngine(s, registry)
	source := []byte(`
name: prepare
version: 1
steps:
  - id: send
    uses: write
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.Error(t, err)
	require.Equal(t, CodeActionInput, errorCode(err))
	require.Equal(t, store.RunFailed, run.Status)
	require.False(t, executed, "action must not run when Prepare fails")
	_, getErr := s.GetEffect(context.Background(), run.ID+":send")
	require.ErrorIs(t, getErr, store.ErrNotFound)
}
