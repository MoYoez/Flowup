package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/agent"
	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/tools"
)

func healthSchema() json.RawMessage {
	b, _ := sonic.Marshal(map[string]any{
		"type": "object", "required": []string{"healthy"},
		"properties": map[string]any{"healthy": map[string]any{"type": "boolean"}},
	})
	return b
}

func task(kind contracts.Kind, whitelist []string, schema json.RawMessage, limits contracts.Limits) contracts.NodeTask {
	return contracts.NodeTask{
		NodeID:       "n",
		Kind:         kind,
		Profile:      contracts.ExecProfile{Models: []string{"m"}, Tools: whitelist},
		Inputs:       json.RawMessage(`{"x":1}`),
		OutputSchema: schema,
		Attempt:      1,
		Limits:       limits,
	}
}

// The loop: model asks for a tool, the tool runs for real, model returns final
// JSON that satisfies the output_schema.
func TestAgentToolLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := tools.NewRegistry(tools.NewHTTPGet())
	sc := model.NewScripted(
		model.Response{ToolCalls: []model.ToolCall{{ID: "t1", Name: "http_get", Args: json.RawMessage(`{"url":"` + srv.URL + `"}`)}}},
		model.Response{Text: `{"healthy":true}`},
	)
	r := agent.NewRunner(sc, reg)
	var toolCalls []string
	r.Observer = func(ev, d string) {
		if ev == "tool_call" {
			toolCalls = append(toolCalls, d)
		}
	}

	res, err := r.Dispatch(context.Background(), task(contracts.KindSemantic, []string{"http_get"}, healthSchema(), contracts.Limits{MaxSteps: 5}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != contracts.StatusOK || !strings.Contains(string(res.Output), "healthy") {
		t.Fatalf("want ok+healthy, got %s/%s out=%s", res.Status, res.Reason, res.Output)
	}
	if sc.Calls() != 2 {
		t.Fatalf("want 2 model calls, got %d", sc.Calls())
	}
	if len(toolCalls) != 1 || !strings.Contains(toolCalls[0], "http_get") {
		t.Fatalf("want one http_get tool call, got %v", toolCalls)
	}
}

func TestAgentBudgetExceeded(t *testing.T) {
	sc := model.NewScripted(model.Response{Text: `{}`, CostUSD: 1.0})
	r := agent.NewRunner(sc, tools.NewRegistry())
	res, _ := r.Dispatch(context.Background(), task(contracts.KindAgent, nil, nil, contracts.Limits{MaxSteps: 5, BudgetUSD: 0.5}))
	if res.Status != contracts.StatusFailed || res.Reason != "budget_exceeded" {
		t.Fatalf("want failed/budget_exceeded, got %s/%s", res.Status, res.Reason)
	}
}

func TestAgentMaxSteps(t *testing.T) {
	reg := tools.NewRegistry(tools.NewHTTPGet())
	call := model.Response{ToolCalls: []model.ToolCall{{ID: "t", Name: "http_get", Args: json.RawMessage(`{"url":"http://127.0.0.1:0"}`)}}}
	sc := model.NewScripted(call, call, call) // never finishes
	r := agent.NewRunner(sc, reg)
	res, _ := r.Dispatch(context.Background(), task(contracts.KindAgent, []string{"http_get"}, nil, contracts.Limits{MaxSteps: 2}))
	if res.Status != contracts.StatusFailed || res.Reason != "max_steps_exceeded" {
		t.Fatalf("want failed/max_steps_exceeded, got %s/%s", res.Status, res.Reason)
	}
}

func TestAgentSchemaViolation(t *testing.T) {
	sc := model.NewScripted(model.Response{Text: "not json"})
	r := agent.NewRunner(sc, tools.NewRegistry())
	res, _ := r.Dispatch(context.Background(), task(contracts.KindSemantic, nil, healthSchema(), contracts.Limits{MaxSteps: 3}))
	if res.Status != contracts.StatusFailed || res.Reason != contracts.ReasonSchemaViolation {
		t.Fatalf("want failed/%s, got %s/%s", contracts.ReasonSchemaViolation, res.Status, res.Reason)
	}
}

func TestAgentRejectsNativeKind(t *testing.T) {
	r := agent.NewRunner(model.NewScripted(), tools.NewRegistry())
	res, _ := r.Dispatch(context.Background(), task(contracts.KindNative, nil, nil, contracts.Limits{}))
	if res.Status != contracts.StatusFailed || res.Reason != "wrong_runner_for_kind" {
		t.Fatalf("want failed/wrong_runner_for_kind, got %s/%s", res.Status, res.Reason)
	}
}

// A model that calls a non-whitelisted tool gets a gated error, not execution.
func TestAgentToolWhitelistEnforced(t *testing.T) {
	reg := tools.NewRegistry(tools.NewHTTPGet()) // http_get registered...
	sc := model.NewScripted(
		model.Response{ToolCalls: []model.ToolCall{{ID: "t", Name: "http_get", Args: json.RawMessage(`{"url":"x"}`)}}},
		model.Response{Text: `{"healthy":true}`},
	)
	r := agent.NewRunner(sc, reg)
	var gated bool
	r.Observer = func(ev, d string) {
		if ev == "tool_call" && strings.Contains(d, "not allowed") {
			gated = true
		}
	}
	// ...but the node's whitelist does NOT include http_get → the call is gated.
	res, _ := r.Dispatch(context.Background(), task(contracts.KindAgent, []string{"shell"}, healthSchema(), contracts.Limits{MaxSteps: 5}))
	if res.Status != contracts.StatusOK {
		t.Fatalf("want ok (loop continues after gated tool), got %s/%s", res.Status, res.Reason)
	}
	if !gated {
		t.Fatal("expected the non-whitelisted tool call to be gated")
	}
}
