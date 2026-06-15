package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	aiaction "github.com/moyoez/flowup/internal/action/ai"
	approvalaction "github.com/moyoez/flowup/internal/action/approval"
	switchaction "github.com/moyoez/flowup/internal/action/switch"
	githubconnector "github.com/moyoez/flowup/internal/connector/github"
	slackconnector "github.com/moyoez/flowup/internal/connector/slack"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/policy"
	"github.com/moyoez/flowup/internal/store"
	"github.com/stretchr/testify/require"
)

func TestTriageIssueAcceptance(t *testing.T) {
	var githubGets, slackSends int
	githubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		githubGets++
		_, _ = io.WriteString(writer, `{"number":42,"title":"Broken build","body":"CI is red","state":"open","html_url":"https://github.com/acme/widgets/issues/42"}`)
	}))
	defer githubServer.Close()
	slackServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		slackSends++
		_, _ = io.WriteString(writer, `{"ok":true,"channel":"C1","ts":"1.0","message":{"text":"High priority issue"}}`)
	}))
	defer slackServer.Close()
	ai := model.NewScripted(model.Script{Response: model.Response{
		Model: "test", JSON: json.RawMessage(`{"priority":"high"}`),
	}})
	registry, err := action.NewRegistry(
		githubconnector.NewIssueGet(githubconnector.Client{BaseURL: githubServer.URL, HTTP: githubServer.Client()}),
		aiaction.New(ai),
		switchaction.New(),
		approvalaction.New(),
		slackconnector.NewMessageSend(slackconnector.Client{BaseURL: slackServer.URL, HTTP: slackServer.Client()}),
	)
	require.NoError(t, err)
	state := testStore(t)
	engine := New(
		state, registry,
		withClock(func() time.Time { return time.Unix(100, 0).UTC() }),
		withIDGenerator(func(prefix string) string { return prefix + "_acceptance" }),
		WithSecrets(policy.MapSecrets{
			"GITHUB_TOKEN": "github-secret",
			"SLACK_TOKEN":  "slack-secret",
		}),
	)
	source := []byte(`
name: triage
version: 1
steps:
  - id: issue
    uses: github.issue.get
    with:
      url: https://github.com/acme/widgets/issues/42
      token: ${{ secrets.GITHUB_TOKEN }}
  - id: classify
    uses: ai.generate
    with:
      model: test
      prompt: Classify
      input:
        title: ${{ steps.issue.output.title }}
      output_schema:
        type: object
        required: [priority]
        properties:
          priority: {type: string}
  - id: route
    uses: switch
    with:
      value: ${{ steps.classify.output.priority }}
      cases: {high: approval, default: notify}
  - id: approve
    uses: approval
    if: ${{ steps.route.output == "approval" }}
    with: {message: Send high priority alert}
  - id: notify
    uses: slack.message.send
    if: ${{ steps.route.output == "notify" || steps.approve.status == "approved" }}
    with:
      channel: engineering
      text: High priority issue
      token: ${{ secrets.SLACK_TOKEN }}
`)

	run, err := engine.Start(context.Background(), source, nil)

	require.NoError(t, err)
	require.Equal(t, store.RunWaitingApproval, run.Status)
	require.Equal(t, 1, githubGets)
	require.Equal(t, 0, slackSends)
	run, err = engine.Approve(context.Background(), "approval_acceptance")
	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, 1, githubGets)
	require.Equal(t, 1, slackSends)
	require.Len(t, ai.Requests(), 1)
	events, err := state.ListEvents(context.Background(), run.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "github-secret")
	require.NotContains(t, string(raw), "slack-secret")
}
