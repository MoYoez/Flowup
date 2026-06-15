package githubconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type githubAction struct {
	client Client
	name   string
	kind   string
	write  bool
}

func NewIssueGet(client Client) action.Action {
	return githubAction{client: client, name: "github.issue.get", kind: "issue"}
}

func NewIssueComment(client Client) action.Action {
	return githubAction{client: client, name: "github.issue.comment", kind: "issue", write: true}
}

func NewPullRequestGet(client Client) action.Action {
	return githubAction{client: client, name: "github.pull_request.get", kind: "pull_request"}
}

func NewPullRequestComment(client Client) action.Action {
	return githubAction{client: client, name: "github.pull_request.comment", kind: "pull_request", write: true}
}

func (a githubAction) Definition() action.Definition {
	properties := map[string]any{
		"url":   map[string]any{"type": "string"},
		"token": map[string]any{"type": "string"},
	}
	required := []any{"url", "token"}
	if a.write {
		properties["body"] = map[string]any{"type": "string"}
		properties["idempotency_key"] = map[string]any{"type": "string"}
		required = append(required, "body")
	}
	return action.Definition{
		Name: a.name,
		InputSchema: map[string]any{
			"type": "object", "required": required, "properties": properties,
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{"type": "object"},
		SecretPaths:  []string{"token"},
		Timeout:      30 * time.Second,
	}
}

func (a githubAction) Effect(map[string]any) action.EffectClass {
	if a.write {
		return action.EffectExternal
	}
	return action.EffectReadOnly
}

func (a githubAction) Execute(ctx context.Context, invocation action.Invocation) (action.Result, error) {
	ref, err := ParseURL(invocation.Input["url"].(string))
	if err != nil {
		return action.Result{}, err
	}
	if ref.Kind != a.kind {
		return action.Result{}, fmt.Errorf("%s requires a %s URL", a.name, a.kind)
	}
	token := invocation.Input["token"].(string)
	if a.write {
		var response struct {
			ID      int64  `json:"id"`
			Body    string `json:"body"`
			HTMLURL string `json:"html_url"`
		}
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", ref.Owner, ref.Repo, ref.Number)
		if err := a.client.DoJSON(ctx, http.MethodPost, path, token, map[string]any{
			"body": invocation.Input["body"],
		}, &response); err != nil {
			return action.Result{}, err
		}
		return marshalResult(map[string]any{"id": response.ID, "body": response.Body, "url": response.HTMLURL})
	}
	if a.kind == "issue" {
		var response struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
		}
		path := fmt.Sprintf("/repos/%s/%s/issues/%d", ref.Owner, ref.Repo, ref.Number)
		if err := a.client.DoJSON(ctx, http.MethodGet, path, token, nil, &response); err != nil {
			return action.Result{}, err
		}
		return marshalResult(map[string]any{
			"number": response.Number, "title": response.Title, "body": response.Body,
			"state": response.State, "url": response.HTMLURL,
		})
	}
	var response struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		Draft   bool   `json:"draft"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number)
	if err := a.client.DoJSON(ctx, http.MethodGet, path, token, nil, &response); err != nil {
		return action.Result{}, err
	}
	return marshalResult(map[string]any{
		"number": response.Number, "title": response.Title, "body": response.Body,
		"state": response.State, "merged": response.Merged, "draft": response.Draft,
		"head": response.Head.Ref, "base": response.Base.Ref, "url": response.HTMLURL,
	})
}

func marshalResult(value any) (action.Result, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode GitHub action output: %w", err)
	}
	return action.Result{Output: raw}, nil
}
