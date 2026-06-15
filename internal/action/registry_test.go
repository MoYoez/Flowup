package action

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeAction struct {
	name         string
	inputSchema  map[string]any
	outputSchema map[string]any
	timeout      time.Duration
}

func (a fakeAction) Definition() Definition {
	return Definition{
		Name:         a.name,
		InputSchema:  a.inputSchema,
		OutputSchema: a.outputSchema,
		Timeout:      a.timeout,
	}
}

func (fakeAction) Effect(map[string]any) EffectClass {
	return EffectReadOnly
}

func (fakeAction) Execute(context.Context, Invocation) (Result, error) {
	return Result{Output: json.RawMessage(`null`)}, nil
}

func validFake(name string) fakeAction {
	return fakeAction{
		name:         name,
		inputSchema:  map[string]any{"type": "object"},
		outputSchema: map[string]any{},
		timeout:      time.Second,
	}
}

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	a := validFake("switch")

	_, err := NewRegistry(a, a)

	require.ErrorContains(t, err, `duplicate action "switch"`)
}

func TestRegistryRequiresSchemas(t *testing.T) {
	a := validFake("broken")
	a.inputSchema = nil

	_, err := NewRegistry(a)

	require.ErrorContains(t, err, "input schema")
}

func TestRegistryRequiresPositiveTimeout(t *testing.T) {
	a := validFake("broken")
	a.timeout = 0

	_, err := NewRegistry(a)

	require.ErrorContains(t, err, "positive timeout")
}

func TestRegistryReturnsSortedNames(t *testing.T) {
	registry, err := NewRegistry(validFake("switch"), validFake("json.select"))
	require.NoError(t, err)

	require.Equal(t, []string{"json.select", "switch"}, registry.Names())
	_, ok := registry.Get("switch")
	require.True(t, ok)
}
