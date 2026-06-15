package policy

import (
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNetworkPolicyBlocksLiteralPrivateAddresses(t *testing.T) {
	policy := DefaultNetworkPolicy()
	require.True(t, policy.BlockPrivateNetworks)

	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata endpoint
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://[::1]/",
		"http://0.0.0.0/",
	} {
		require.ErrorContains(t, policy.ValidateURL(raw), "blocked private address", raw)
	}

	require.NoError(t, policy.ValidateURL("https://example.com/path"))
}

func TestNetworkPolicyAllowsPrivateWhenDisabled(t *testing.T) {
	policy := NetworkPolicy{AllowedSchemes: []string{"http"}, BlockPrivateNetworks: false}

	require.NoError(t, policy.ValidateURL("http://127.0.0.1/"))
}

func TestGuardedHTTPClientBlocksPrivateConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	defer server.Close()

	blocked := GuardedHTTPClient(DefaultNetworkPolicy())
	_, err := blocked.Get(server.URL) // resolves to 127.0.0.1
	require.Error(t, err)
	require.ErrorContains(t, err, "blocked")

	allowed := GuardedHTTPClient(NetworkPolicy{BlockPrivateNetworks: false})
	response, err := allowed.Get(server.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
}
