package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

type declaredAction struct {
	definition action.Definition
}

func (a declaredAction) Definition() action.Definition {
	return a.definition
}

func (declaredAction) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (declaredAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{Output: json.RawMessage(`null`)}, nil
}

func secretRegistry(t *testing.T) *action.Registry {
	t.Helper()
	registry, err := action.NewRegistry(
		declaredAction{definition: action.Definition{
			Name:         "http.request",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{},
			SecretPaths:  []string{"headers.*"},
			Timeout:      time.Second,
		}},
		declaredAction{definition: action.Definition{
			Name:         "ai.generate",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{},
			Timeout:      time.Second,
		}},
	)
	require.NoError(t, err)
	return registry
}

func TestValidateAllowsSecretAtDeclaredPath(t *testing.T) {
	wf := mustParse(t, `
name: secret
version: 1
steps:
  - id: fetch
    uses: http.request
    with:
      url: https://example.test
      headers:
        Authorization: ${{ secrets.TOKEN }}
`)

	_, err := Validate(wf, secretRegistry(t))

	require.NoError(t, err)
}

func TestValidateRejectsSecretAtNonSecretPath(t *testing.T) {
	wf := mustParse(t, `
name: secret
version: 1
steps:
  - id: fetch
    uses: http.request
    with:
      url: ${{ secrets.TOKEN }}
`)

	_, err := Validate(wf, secretRegistry(t))

	require.ErrorContains(t, err, `secret reference is not allowed at "url"`)
}

func TestValidateRejectsSecretInAIInput(t *testing.T) {
	wf := mustParse(t, `
name: ai-secret
version: 1
steps:
  - id: classify
    uses: ai.generate
    with:
      prompt: classify
      input:
        token: ${{ secrets.TOKEN }}
`)

	_, err := Validate(wf, secretRegistry(t))

	require.ErrorContains(t, err, "AI input cannot contain secret references")
}
