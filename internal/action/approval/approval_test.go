package approvalaction

import (
	"context"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestApprovalActionReturnsPause(t *testing.T) {
	result, err := New().Execute(context.Background(), action.Invocation{Input: map[string]any{
		"message": "Send alert",
		"preview": map[string]any{"priority": "high"},
	}})

	require.NoError(t, err)
	require.Equal(t, "approval", result.Pause.Kind)
	require.Equal(t, "Send alert", result.Pause.Message)
	require.JSONEq(t, `{"priority":"high"}`, string(result.Pause.Preview))
}
