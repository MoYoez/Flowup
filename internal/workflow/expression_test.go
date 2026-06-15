package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePreservesCompleteReferenceType(t *testing.T) {
	got, err := Resolve("${{ steps.fetch.output }}", Context{
		Steps: map[string]StepValue{
			"fetch": {
				Status: "succeeded",
				Output: map[string]any{"count": float64(2)},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"count": float64(2)}, got)
}

func TestResolveInterpolatesScalarReferences(t *testing.T) {
	got, err := Resolve("issue-${{ inputs.number }}-${{ steps.fetch.status }}", Context{
		Inputs: map[string]any{"number": float64(42)},
		Steps:  map[string]StepValue{"fetch": {Status: "succeeded"}},
	})

	require.NoError(t, err)
	require.Equal(t, "issue-42-succeeded", got)
}

func TestResolveRecursesThroughObjectsAndLists(t *testing.T) {
	got, err := Resolve(map[string]any{
		"title": "${{ steps.fetch.output.title }}",
		"tags":  []any{"static", "${{ inputs.tag }}"},
	}, Context{
		Inputs: map[string]any{"tag": "bug"},
		Steps: map[string]StepValue{
			"fetch": {Output: map[string]any{"title": "Broken"}},
		},
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"title": "Broken",
		"tags":  []any{"static", "bug"},
	}, got)
}

func TestResolveRejectsMissingReference(t *testing.T) {
	_, err := Resolve("${{ steps.fetch.output.title }}", Context{})

	require.ErrorContains(t, err, `step "fetch" is not available`)
}

func TestEvaluateCondition(t *testing.T) {
	ok, err := EvaluateCondition(
		`${{ steps.route.output == "notify" || steps.approval.status == "approved" }}`,
		Context{Steps: map[string]StepValue{
			"route":    {Output: "notify"},
			"approval": {Status: "skipped"},
		}},
	)

	require.NoError(t, err)
	require.True(t, ok)
}

func TestEvaluateConditionSupportsParenthesesAndInequality(t *testing.T) {
	ok, err := EvaluateCondition(
		`${{ (inputs.priority != "low" && inputs.enabled == true) || inputs.override == true }}`,
		Context{Inputs: map[string]any{
			"priority": "high",
			"enabled":  true,
			"override": false,
		}},
	)

	require.NoError(t, err)
	require.True(t, ok)
}

func TestEvaluateConditionRejectsFunctions(t *testing.T) {
	_, err := EvaluateCondition(`${{ contains(inputs.title, "x") }}`, Context{
		Inputs: map[string]any{"title": "x"},
	})

	require.ErrorContains(t, err, "unexpected token")
}

func TestReferencesDiscoversNestedAndConditionReferences(t *testing.T) {
	refs, err := References(map[string]any{
		"value": "${{ steps.second.output.title }}",
		"if":    `${{ inputs.enabled && steps.first.status == "succeeded" }}`,
	})

	require.NoError(t, err)
	require.ElementsMatch(t, []Reference{
		{Kind: ReferenceStep, Name: "second", Path: "output.title"},
		{Kind: ReferenceInput, Name: "enabled"},
		{Kind: ReferenceStep, Name: "first", Path: "status"},
	}, refs)
}
