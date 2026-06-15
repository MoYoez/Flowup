package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSecretReferenceRequiresCompleteValue(t *testing.T) {
	ref, ok, err := ParseSecretReference("${{ secrets.GITHUB_TOKEN }}")

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "GITHUB_TOKEN", ref.Name)

	_, _, err = ParseSecretReference("Bearer ${{ secrets.GITHUB_TOKEN }}")
	require.ErrorContains(t, err, "must occupy the complete value")
}

func TestResolveLeavesSecretReferenceOpaque(t *testing.T) {
	got, err := Resolve("${{ secrets.GITHUB_TOKEN }}", Context{})

	require.NoError(t, err)
	require.Equal(t, SecretRef{Name: "GITHUB_TOKEN"}, got)
	require.True(t, ContainsSecret(map[string]any{"token": got}))
}

func TestReferencesReportsSecret(t *testing.T) {
	refs, err := References(map[string]any{"token": "${{ secrets.GITHUB_TOKEN }}"})

	require.NoError(t, err)
	require.Equal(t, []Reference{{Kind: ReferenceSecret, Name: "GITHUB_TOKEN"}}, refs)
}
