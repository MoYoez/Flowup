package httpaction

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/policy"
	"github.com/stretchr/testify/require"
)

func TestRequestReturnsNormalizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "2", request.URL.Query().Get("page"))
		require.Equal(t, "Bearer value", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer server.Close()
	candidate := New(server.Client(), policy.NetworkPolicy{
		AllowedSchemes:   []string{"http"},
		AllowedHosts:     []string{hostOf(t, server.URL)},
		MaxResponseBytes: 1024,
	})

	result, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"method":  "GET",
		"url":     server.URL,
		"headers": map[string]any{"Authorization": "Bearer value"},
		"query":   map[string]any{"page": "2"},
	}})

	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(result.Output, &output))
	require.Equal(t, float64(200), output["status"])
	require.Equal(t, map[string]any{"ok": true}, output["body"])
	require.Equal(t, action.EffectReadOnly, candidate.Effect(map[string]any{"method": "GET"}))
}

func TestRequestClassifiesWritesAsExternalEffects(t *testing.T) {
	candidate := New(http.DefaultClient, policy.DefaultNetworkPolicy())

	require.Equal(t, action.EffectExternal, candidate.Effect(map[string]any{"method": "POST"}))
}

func TestRequestEnforcesResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", 33))
	}))
	defer server.Close()
	candidate := New(server.Client(), policy.NetworkPolicy{
		AllowedSchemes:   []string{"http"},
		AllowedHosts:     []string{hostOf(t, server.URL)},
		MaxResponseBytes: 32,
	})

	_, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"method": "GET",
		"url":    server.URL,
	}})

	require.ErrorContains(t, err, "response exceeds 32 bytes")
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed.Hostname()
}
