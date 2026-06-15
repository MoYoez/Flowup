package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Anthropic is a model.Client backed by the Anthropic Messages API (tool use).
//
// Written against the documented Messages API shape but not yet exercised
// against the live API; verify with ANTHROPIC_API_KEY before relying on it. The
// OpenAI-compatible client covers the same interface and is verified end to end.
type Anthropic struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	Version string
}

// NewAnthropic builds the client from ANTHROPIC_API_KEY (or a passed key).
func NewAnthropic(key string) *Anthropic {
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	return &Anthropic{
		APIKey:  key,
		BaseURL: "https://api.anthropic.com",
		HTTP:    &http.Client{Timeout: 120 * time.Second},
		Version: "2023-06-01",
	}
}

func (*Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	if a.APIKey == "" {
		return Response{}, fmt.Errorf("anthropic: no API key (set ANTHROPIC_API_KEY)")
	}
	body := map[string]any{
		"model":       req.Model,
		"max_tokens":  orDefault(req.MaxTokens, 1024),
		"messages":    toAnthropicMessages(req.Messages),
		"temperature": req.Temperature,
	}
	if req.System != "" {
		body["system"] = req.System
	}
	if len(req.Tools) > 0 {
		body["tools"] = toAnthropicTools(req.Tools)
	}
	raw, _ := sonic.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", a.Version)

	resp, err := a.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := sonic.ConfigDefault.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("anthropic: decode: %w", err)
	}
	if resp.StatusCode >= 300 {
		msg := "http " + resp.Status
		if out.Error != nil {
			msg = out.Error.Message
		}
		return Response{}, fmt.Errorf("anthropic: %s", msg)
	}

	var r Response
	var text strings.Builder
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "tool_use":
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Args: c.Input})
		}
	}
	r.Text = text.String()
	r.CostUSD = estimateCostUSD(req.Model, out.Usage.InputTokens, out.Usage.OutputTokens)
	return r, nil
}

func toAnthropicMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": m.Content}}})
		case RoleAssistant:
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": json.RawMessage(tc.Args)})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case RoleTool:
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content},
			}})
		}
	}
	return out
}

func toAnthropicTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		out = append(out, map[string]any{
			"name":         s.Name,
			"description":  s.Description,
			"input_schema": json.RawMessage(s.InputSchema),
		})
	}
	return out
}

// estimateCostUSD is an APPROXIMATE per-token cost for the budget guard (tune to
// current pricing). Approximate is fine: the guard trips on accumulated spend.
func estimateCostUSD(modelName string, in, out int) float64 {
	var inPer, outPer float64 // USD per 1M tokens
	switch {
	case strings.Contains(modelName, "opus"):
		inPer, outPer = 15, 75
	case strings.Contains(modelName, "haiku"):
		inPer, outPer = 0.8, 4
	default: // sonnet / unknown
		inPer, outPer = 3, 15
	}
	return float64(in)/1e6*inPer + float64(out)/1e6*outPer
}

func orDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}
