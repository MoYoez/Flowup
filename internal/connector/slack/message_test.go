package slackconnector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestMessageSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/chat.postMessage", request.URL.Path)
		require.Equal(t, "Bearer slack-token", request.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, "engineering", body["channel"])
		require.Equal(t, "Build failed", body["text"])
		_, _ = writer.Write([]byte(`{"ok":true,"channel":"C123","ts":"1710000000.000100","message":{"text":"Build failed"}}`))
	}))
	defer server.Close()
	candidate := NewMessageSend(Client{BaseURL: server.URL, HTTP: server.Client()})

	result, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"channel": "engineering", "text": "Build failed", "token": "slack-token",
	}})

	require.NoError(t, err)
	require.JSONEq(t, `{"channel":"C123","timestamp":"1710000000.000100","text":"Build failed"}`, string(result.Output))
	require.Equal(t, action.EffectExternal, candidate.Effect(nil))
}

func TestMessageGetSelectsExactTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/conversations.replies", request.URL.Path)
		_, _ = writer.Write([]byte(`{"ok":true,"messages":[
			{"ts":"1.0","user":"U1","text":"first"},
			{"ts":"2.0","user":"U2","text":"target","thread_ts":"1.0"}
		]}`))
	}))
	defer server.Close()
	candidate := NewMessageGet(Client{BaseURL: server.URL, HTTP: server.Client()})

	result, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"channel": "C123", "timestamp": "2.0", "token": "slack-token",
	}})

	require.NoError(t, err)
	require.JSONEq(t, `{"channel":"C123","timestamp":"2.0","user":"U2","text":"target","thread_timestamp":"1.0"}`, string(result.Output))
	require.Equal(t, action.EffectReadOnly, candidate.Effect(nil))
}

func TestSlackErrorIsSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":false,"error":"channel_not_found","detail":"secret-body"}`))
	}))
	defer server.Close()
	candidate := NewMessageSend(Client{BaseURL: server.URL, HTTP: server.Client()})

	_, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"channel": "missing", "text": "x", "token": "slack-token",
	}})

	require.ErrorContains(t, err, "channel_not_found")
	require.NotContains(t, err.Error(), "secret-body")
}
