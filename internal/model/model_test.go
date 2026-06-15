package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScriptedClientReturnsResponsesInOrder(t *testing.T) {
	client := NewScripted(
		Script{Response: Response{Model: "first", JSON: json.RawMessage(`{"value":1}`)}},
		Script{Response: Response{Model: "second", JSON: json.RawMessage(`{"value":2}`)}},
	)

	first, err := client.Generate(context.Background(), Request{Model: "first"})
	require.NoError(t, err)
	require.JSONEq(t, `{"value":1}`, string(first.JSON))
	second, err := client.Generate(context.Background(), Request{Model: "second"})
	require.NoError(t, err)
	require.JSONEq(t, `{"value":2}`, string(second.JSON))
	require.Len(t, client.Requests(), 2)
}

func TestScriptedClientReturnsConfiguredError(t *testing.T) {
	expected := errors.New("unavailable")
	client := NewScripted(Script{Err: expected})

	_, err := client.Generate(context.Background(), Request{})

	require.ErrorIs(t, err, expected)
}

func TestOpenAISendsStructuredOutputRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/chat/completions", request.URL.Path)
		require.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, false, body["stream"])
		require.Equal(t, false, body["store"])
		require.Nil(t, body["tools"])
		responseFormat := body["response_format"].(map[string]any)
		require.Equal(t, "json_schema", responseFormat["type"])
		_, _ = io.WriteString(writer, `{
			"model":"test-model",
			"choices":[{"message":{"content":"{\"priority\":\"high\"}"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":4}
		}`)
	}))
	defer server.Close()
	client := NewOpenAI(server.URL, "test-key", server.Client())

	response, err := client.Generate(context.Background(), Request{
		Model: "test-model", Prompt: "Classify",
		Input: json.RawMessage(`{"title":"Build failure"}`),
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"priority": map[string]any{"type": "string"}},
		},
		MaxTokens: 100,
	})

	require.NoError(t, err)
	require.JSONEq(t, `{"priority":"high"}`, string(response.JSON))
	require.Equal(t, Usage{InputTokens: 10, OutputTokens: 4}, response.Usage)
}

func TestOpenAIErrorDoesNotExposeProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":"provider-secret-body"}`)
	}))
	defer server.Close()
	client := NewOpenAI(server.URL, "test-key", server.Client())

	_, err := client.Generate(context.Background(), Request{Model: "test"})

	require.ErrorContains(t, err, "status 401")
	require.NotContains(t, err.Error(), "provider-secret-body")
}
