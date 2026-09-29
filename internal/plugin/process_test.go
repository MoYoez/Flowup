package plugin

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvironmentPreservesCaseSensitiveAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows environment keys are case-insensitive")
	}
	t.Setenv("HTTP_PROXY", "upper")
	t.Setenv("http_proxy", "lower")
	p := Process{binding: Binding{Spec: Spec{Env: []string{"HTTP_PROXY", "http_proxy"}}}}
	env := p.environment()
	require.Contains(t, env, "HTTP_PROXY=upper")
	require.Contains(t, env, "http_proxy=lower")
}
