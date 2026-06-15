package switchaction

import (
	"context"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestSwitchUsesMatchingCase(t *testing.T) {
	result, err := New().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": "high",
			"cases": map[string]any{"high": "approval", "default": "notify"},
		},
	})

	require.NoError(t, err)
	require.JSONEq(t, `"approval"`, string(result.Output))
}

func TestSwitchUsesDefault(t *testing.T) {
	result, err := New().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": "unknown",
			"cases": map[string]any{"high": "approval", "default": "notify"},
		},
	})

	require.NoError(t, err)
	require.JSONEq(t, `"notify"`, string(result.Output))
}

func TestSwitchRejectsMissingDefault(t *testing.T) {
	_, err := New().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": "unknown",
			"cases": map[string]any{"high": "approval"},
		},
	})

	require.ErrorContains(t, err, "no matching case and no default")
}
