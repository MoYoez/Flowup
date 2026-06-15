package jsonaction

import (
	"context"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestSelectProjectsNamedPaths(t *testing.T) {
	result, err := NewSelect().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": map[string]any{
				"issue": map[string]any{"title": "Broken", "priority": "high"},
			},
			"paths": map[string]any{"title": "issue.title"},
		},
	})

	require.NoError(t, err)
	require.JSONEq(t, `{"title":"Broken"}`, string(result.Output))
}

func TestSelectRejectsMissingPath(t *testing.T) {
	_, err := NewSelect().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": map[string]any{"issue": map[string]any{}},
			"paths": map[string]any{"title": "issue.title"},
		},
	})

	require.ErrorContains(t, err, `path "issue.title" is missing`)
}

func TestSelectRejectsArrayIndexing(t *testing.T) {
	_, err := NewSelect().Execute(context.Background(), action.Invocation{
		Input: map[string]any{
			"value": map[string]any{"items": []any{"first"}},
			"paths": map[string]any{"first": "items.0"},
		},
	})

	require.ErrorContains(t, err, "array indexing is not supported")
}
