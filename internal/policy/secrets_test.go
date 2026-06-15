package policy

import (
	"testing"

	"github.com/moyoez/flowup/internal/workflow"
	"github.com/stretchr/testify/require"
)

func TestResolveSecretsOnlyAtDeclaredPaths(t *testing.T) {
	input := map[string]any{
		"url": "https://api.example.test",
		"headers": map[string]any{
			"Authorization": workflow.SecretRef{Name: "TOKEN"},
		},
	}

	resolved, redacted, err := ResolveSecrets(input, []string{"headers.*"}, MapSecrets{"TOKEN": "secret-value"})

	require.NoError(t, err)
	require.Equal(t, "secret-value", resolved["headers"].(map[string]any)["Authorization"])
	require.Equal(t, "[REDACTED]", redacted["headers"].(map[string]any)["Authorization"])
}

func TestResolveSecretsRejectsNonSecretField(t *testing.T) {
	_, _, err := ResolveSecrets(
		map[string]any{"url": workflow.SecretRef{Name: "TOKEN"}},
		[]string{"headers.*"},
		MapSecrets{"TOKEN": "secret-value"},
	)

	require.ErrorContains(t, err, `secret reference is not allowed at "url"`)
}

func TestResolveSecretsRejectsMissingValue(t *testing.T) {
	_, _, err := ResolveSecrets(
		map[string]any{"token": workflow.SecretRef{Name: "TOKEN"}},
		[]string{"token"},
		MapSecrets{},
	)

	require.ErrorContains(t, err, `secret "TOKEN" is not available`)
}

func TestRedactMasksSensitiveKeysRecursively(t *testing.T) {
	got := Redact(map[string]any{
		"Authorization": "Bearer value",
		"nested": map[string]any{
			"api_key": "value",
			"safe":    "visible",
		},
	})

	require.Equal(t, map[string]any{
		"Authorization": "[REDACTED]",
		"nested": map[string]any{
			"api_key": "[REDACTED]",
			"safe":    "visible",
		},
	}, got)
}
