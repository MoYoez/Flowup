package jsonaction

import (
	"context"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestValidateReturnsSchemaValidValue(t *testing.T) {
	result, err := NewValidate().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value":  "ready",
			"schema": map[string]any{"type": "string"},
		},
	})

	require.NoError(t, err)
	require.JSONEq(t, `"ready"`, string(result.Output))
}

func TestValidateRejectsSchemaMismatch(t *testing.T) {
	_, err := NewValidate().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value":  float64(3),
			"schema": map[string]any{"type": "string"},
		},
	})

	require.ErrorContains(t, err, "does not validate")
}
