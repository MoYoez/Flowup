package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNetworkPolicyRejectsDisallowedSchemeAndHost(t *testing.T) {
	policy := NetworkPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"api.example.com"},
	}

	require.Error(t, policy.ValidateURL("file:///etc/passwd"))
	require.Error(t, policy.ValidateURL("https://other.example.com/data"))
	require.NoError(t, policy.ValidateURL("https://api.example.com/data"))
}

func TestNetworkPolicyRejectsCredentialsAndFragments(t *testing.T) {
	policy := NetworkPolicy{AllowedSchemes: []string{"https"}}

	require.ErrorContains(t, policy.ValidateURL("https://user:pass@example.com"), "credentials")
	require.ErrorContains(t, policy.ValidateURL("https://example.com/path#fragment"), "fragment")
}
