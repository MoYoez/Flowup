// Package agent runs semantic/agent nodes: the model→tool→model loop. It is a
// dispatch.Runner, so it slots behind the same seam as the sandbox runner — the
// engine is unaware. The model is pluggable (model.Client), tools
// are whitelisted per node, and max_steps + budget_usd are enforced. The final
// model answer is validated against the node's output_schema before returning.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/tools"
)

const (
	defaultMaxSteps  = 8
	defaultMaxTokens = 2048 // reasoning models need room for thinking + the answer
)

// Runner drives the agent loop for semantic/agent nodes.
type Runner struct {
	client model.Client
	tools  *tools.Registry

	// Observer, if set, receives a line per model step / tool call (for tests
	// and lightweight tracing).
	Observer func(event, detail string)

	// TokenSink, if set, receives batched answer-token chunks as they stream
	// (run_id, node_id, chunk) — the worker wires this to the event log so the
	// engine's SSE relays live tokens to the dashboard cross-process.
	TokenSink func(runID, nodeID, chunk string)
}

// NewRunner builds an agent runner over a model client and a tool registry.
func NewRunner(client model.Client, reg *tools.Registry) *Runner {
	return &Runner{client: client, tools: reg}
}

func (r *Runner) observe(event, detail string) {
	if r.Observer != nil {
		r.Observer(event, detail)
	}
}

// Dispatch runs one semantic/agent node.
func (r *Runner) Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	res := contracts.NodeResult{RunID: task.RunID, NodeID: task.NodeID, LeaseID: task.LeaseID}

	if task.Kind != contracts.KindSemantic && task.Kind != contracts.KindAgent {
		res.Status = contracts.StatusFailed
		res.Reason = "wrong_runner_for_kind" // native goes to the sandbox runner
		return res, nil
	}

	whitelist := task.Profile.Tools
	specs := r.tools.Specs(whitelist)
	system := task.Profile.Role
	modelName := task.Profile.ModelForAttempt(task.Attempt)

	msgs := []model.Message{{Role: model.RoleUser, Content: buildPrompt(task)}}

	maxSteps := task.Limits.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	var spent float64

	var tokBuf strings.Builder
	tokN := 0
	onToken := func(delta string) {
		if r.TokenSink == nil {
			return
		}
		tokBuf.WriteString(delta)
		if tokN++; tokN%10 == 0 {
			r.emitTokens(task, &tokBuf)
		}
	}

	for step := 1; step <= maxSteps; step++ {
		resp, err := r.complete(ctx, model.Request{
			Model:       modelName,
			System:      system,
			Messages:    msgs,
			Tools:       specs,
			Temperature: task.Profile.Temp,
			MaxTokens:   defaultMaxTokens,
		}, onToken)
		r.emitTokens(task, &tokBuf) // flush any tokens streamed this turn
		if err != nil {
			res.Status = contracts.StatusFailed
			res.Reason = "model_error"
			res.Retriable = true // transient (timeout/hiccup) → node retry can self-heal
			return res, nil
		}
		spent += resp.CostUSD
		if task.Limits.BudgetUSD > 0 && spent > task.Limits.BudgetUSD {
			res.Status = contracts.StatusFailed
			res.Reason = "budget_exceeded"
			return res, nil
		}
		r.observe("model_step", fmt.Sprintf("step=%d tool_calls=%d spent=%.4f", step, len(resp.ToolCalls), spent))

		if len(resp.ToolCalls) == 0 {
			// Final answer: must satisfy the output_schema (if any). Real models
			// (esp. reasoning ones) may wrap JSON in prose/markdown — extract it.
			out := json.RawMessage(extractJSON(resp.Text))
			if schema := decodeSchema(task.OutputSchema); len(schema) > 0 {
				if err := pipelines.ValidateJSONAgainstSchema(schema, out); err != nil {
					res.Status = contracts.StatusFailed
					res.Reason = contracts.ReasonSchemaViolation
					return res, nil
				}
			}
			res.Status = contracts.StatusOK
			res.Output = out
			return res, nil
		}

		// Run each tool call (whitelist-gated) and feed results back. Keep the
		// assistant's tool_calls on the message so a provider adapter can replay
		// them as native tool_use blocks.
		msgs = append(msgs, model.Message{Role: model.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			result := r.runTool(ctx, whitelist, tc)
			r.observe("tool_call", tc.Name+" → "+oneLine(result))
			msgs = append(msgs, model.Message{Role: model.RoleTool, ToolCallID: tc.ID, Content: result})
		}
	}

	res.Status = contracts.StatusFailed
	res.Reason = "max_steps_exceeded"
	return res, nil
}

// complete runs one model turn. Tool-less turns stream token-by-token when the
// client supports it (each token goes to the Observer + onToken); turns that
// offer tools use Complete, which captures tool_calls.
func (r *Runner) complete(ctx context.Context, req model.Request, onToken func(string)) (model.Response, error) {
	if len(req.Tools) == 0 {
		if sc, ok := r.client.(model.StreamingClient); ok {
			return sc.Stream(ctx, req, func(delta string) {
				r.observe("token", delta)
				if onToken != nil {
					onToken(delta)
				}
			})
		}
	}
	return r.client.Complete(ctx, req)
}

// emitTokens flushes a buffered token chunk to the sink (worker → event log → SSE).
func (r *Runner) emitTokens(task contracts.NodeTask, buf *strings.Builder) {
	if r.TokenSink != nil && buf.Len() > 0 {
		r.TokenSink(task.RunID, task.NodeID, buf.String())
	}
	buf.Reset()
}

func (r *Runner) runTool(ctx context.Context, whitelist []string, tc model.ToolCall) string {
	if !r.tools.Allowed(tc.Name, whitelist) {
		return "error: tool not allowed: " + tc.Name
	}
	t, ok := r.tools.Get(tc.Name)
	if !ok {
		return "error: unknown tool: " + tc.Name
	}
	out, err := t.Invoke(ctx, tc.Args)
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}

func buildPrompt(task contracts.NodeTask) string {
	var b strings.Builder
	b.WriteString("Inputs (JSON):\n")
	b.Write(task.Inputs)
	if len(task.OutputSchema) > 0 && string(task.OutputSchema) != "null" {
		b.WriteString("\n\nUse the available tools as needed, then reply with ONLY a JSON object matching this schema (no prose):\n")
		b.Write(task.OutputSchema)
	} else {
		b.WriteString("\n\nUse the available tools as needed, then reply with the result.")
	}
	return b.String()
}

func decodeSchema(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	_ = sonic.Unmarshal(raw, &m)
	return m
}

// extractJSON pulls a JSON object/array out of a model reply that may be wrapped
// in ```json fences or surrounding prose (common with reasoning models).
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := strings.TrimSpace(s[i+3:])
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = strings.TrimSpace(rest)
	}
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		start := strings.IndexAny(s, "{[")
		if start >= 0 {
			closer := byte('}')
			if s[start] == '[' {
				closer = ']'
			}
			if end := strings.LastIndexByte(s, closer); end > start {
				s = s[start : end+1]
			}
		}
	}
	return strings.TrimSpace(s)
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
