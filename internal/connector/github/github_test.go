package githubconnector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moyoez/flowup/internal/action"
	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	issue, err := ParseURL("https://github.com/acme/widgets/issues/42")
	require.NoError(t, err)
	require.Equal(t, Ref{Owner: "acme", Repo: "widgets", Number: 42, Kind: "issue"}, issue)
	pull, err := ParseURL("https://github.com/acme/widgets/pull/7")
	require.NoError(t, err)
	require.Equal(t, "pull_request", pull.Kind)
}

func TestIssueGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/repos/acme/widgets/issues/42", request.URL.Path)
		require.Equal(t, "Bearer token-value", request.Header.Get("Authorization"))
		_, _ = io.WriteString(writer, `{"number":42,"title":"Broken","body":"Details","state":"open","html_url":"https://github.com/acme/widgets/issues/42"}`)
	}))
	defer server.Close()
	candidate := NewIssueGet(Client{BaseURL: server.URL, HTTP: server.Client()})

	result, err := candidate.Execute(context.Background(), invocation(map[string]any{
		"url": "https://github.com/acme/widgets/issues/42", "token": "token-value",
	}))

	require.NoError(t, err)
	require.JSONEq(t, `{"number":42,"title":"Broken","body":"Details","state":"open","url":"https://github.com/acme/widgets/issues/42"}`, string(result.Output))
}

func TestPullRequestGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/repos/acme/widgets/pulls/7", request.URL.Path)
		_, _ = io.WriteString(writer, `{"number":7,"title":"Change","body":"Body","state":"open","merged":false,"draft":true,"html_url":"https://github.com/acme/widgets/pull/7","head":{"ref":"feature"},"base":{"ref":"main"}}`)
	}))
	defer server.Close()
	candidate := NewPullRequestGet(Client{BaseURL: server.URL, HTTP: server.Client()})

	result, err := candidate.Execute(context.Background(), invocation(map[string]any{
		"url": "https://github.com/acme/widgets/pull/7", "token": "token-value",
	}))

	require.NoError(t, err)
	require.Contains(t, string(result.Output), `"head":"feature"`)
	require.Contains(t, string(result.Output), `"draft":true`)
}

func TestCommentActionsPostBody(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, "Investigating", body["body"])
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"id":99,"html_url":"https://github.com/comment/99","body":"Investigating"}`)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTP: server.Client()}
	for _, test := range []struct {
		candidate action.Action
		url       string
	}{
		{NewIssueComment(client), "https://github.com/acme/widgets/issues/42"},
		{NewPullRequestComment(client), "https://github.com/acme/widgets/pull/7"},
	} {
		result, err := test.candidate.Execute(context.Background(), invocation(map[string]any{
			"url": test.url, "body": "Investigating", "token": "token-value",
		}))
		require.NoError(t, err)
		require.JSONEq(t, `{"id":99,"body":"Investigating","url":"https://github.com/comment/99"}`, string(result.Output))
		require.Equal(t, action.EffectExternal, test.candidate.Effect(nil))
	}
	require.Equal(t, []string{
		"/repos/acme/widgets/issues/42/comments",
		"/repos/acme/widgets/issues/7/comments",
	}, paths)
}

func invocation(input map[string]any) action.Invocation {
	return action.Invocation{RunID: "run", StepID: "step", Input: input, IdempotencyKey: "run:step"}
}
