package model

import (
	"bufio"
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

// OpenAI is a model.Client for any OpenAI-compatible Chat Completions endpoint
// (OpenAI, vLLM, llama.cpp, Ollama, a proxy, …). Configured from env:
//
//	OPENAI_BASE_URL   e.g. https://host:8000   (the /v1/chat/completions is appended)
//	OPENAI_API_KEY    bearer token
//	OPENAI_MODEL      model name (overrides the pipeline's profile.models)
type OpenAI struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewOpenAI builds the client from OPENAI_* env vars.
func NewOpenAI() *OpenAI {
	return &OpenAI{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: strings.TrimRight(getenvDefault("OPENAI_BASE_URL", "https://api.openai.com"), "/"),
		Model:   os.Getenv("OPENAI_MODEL"),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

func (*OpenAI) Name() string { return "openai" }

func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	raw, _ := sonic.Marshal(o.requestBody(req, false))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"` // a JSON string in the OpenAI schema
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := sonic.ConfigDefault.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("openai: decode: %w", err)
	}
	if resp.StatusCode >= 300 {
		msg := "http " + resp.Status
		if out.Error != nil {
			msg = out.Error.Message
		}
		return Response{}, fmt.Errorf("openai: %s", msg)
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: empty choices")
	}

	var r Response
	r.Text = out.Choices[0].Message.Content
	for _, tc := range out.Choices[0].Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		r.ToolCalls = append(r.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: json.RawMessage(args)})
	}
	// Cost is unknown for self-hosted/proxy endpoints → 0 (budget guard inert).
	return r, nil
}

func (o *OpenAI) requestBody(req Request, stream bool) map[string]any {
	mdl := o.Model
	if mdl == "" {
		mdl = req.Model
	}
	body := map[string]any{
		"model":       mdl,
		"messages":    toOpenAIMessages(req.System, req.Messages),
		"temperature": req.Temperature,
		"max_tokens":  orDefault(req.MaxTokens, 2048),
	}
	if len(req.Tools) > 0 {
		body["tools"] = toOpenAITools(req.Tools)
	}
	if stream {
		body["stream"] = true
	}
	return body
}

// Stream issues a streaming Chat Completion: onDelta fires per content fragment
// as tokens arrive; the assembled answer is returned at the end. (Content only —
// tool-call streaming falls back to Complete.)
func (o *OpenAI) Stream(ctx context.Context, req Request, onDelta func(delta string)) (Response, error) {
	raw, _ := sonic.Marshal(o.requestBody(req, true))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	if o.APIKey != "" {
		httpReq.Header.Set("authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := bufio.NewReader(resp.Body).ReadString('\n')
		return Response{}, fmt.Errorf("openai stream: http %d: %s", resp.StatusCode, strings.TrimSpace(b))
	}

	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20) // tolerate long SSE lines
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if sonic.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		if d := chunk.Choices[0].Delta.Content; d != "" {
			text.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Response{}, err
	}
	return Response{Text: text.String()}, nil
}

func toOpenAIMessages(system string, msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	if system != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, map[string]any{"role": "user", "content": m.Content})
		case RoleAssistant:
			am := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.ToolCalls) > 0 {
				var tcs []map[string]any
				for _, tc := range m.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id": tc.ID, "type": "function",
						"function": map[string]any{"name": tc.Name, "arguments": string(tc.Args)},
					})
				}
				am["tool_calls"] = tcs
			}
			out = append(out, am)
		case RoleTool:
			out = append(out, map[string]any{"role": "tool", "tool_call_id": m.ToolCallID, "content": m.Content})
		}
	}
	return out
}

func toOpenAITools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		out = append(out, map[string]any{
			"type":     "function",
			"function": map[string]any{"name": s.Name, "description": s.Description, "parameters": json.RawMessage(s.InputSchema)},
		})
	}
	return out
}

func getenvDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
