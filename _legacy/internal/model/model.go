// Package model is the casting layer: a pluggable model client behind one
// interface, so a node's semantic/agent execution can run a real LLM (Anthropic)
// or a deterministic scripted model (tests/CI). The agent tool-loop lives in
// internal/agent and drives this interface; swapping the client never touches
// the loop, the engine, or the contract.
package model

import (
	"context"
	"encoding/json"
)

// Role is a message author.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // a tool result fed back to the model
)

// Message is one turn in the conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // set on RoleTool results
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // set on RoleAssistant turns that called tools
}

// ToolSpec describes a tool the model may call (name + JSON-schema args).
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Request is one model completion request.
type Request struct {
	Model       string
	System      string // node role
	Messages    []Message
	Tools       []ToolSpec
	Temperature float64
	MaxTokens   int
}

// Response is one model completion. Either it asks for tools (ToolCalls non-empty)
// or it returns a final answer (Text). CostUSD feeds the budget guard.
type Response struct {
	Text      string
	ToolCalls []ToolCall
	CostUSD   float64
}

// Client is the swappable model backend.
type Client interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamingClient is a Client that can also stream the answer token-by-token.
// onDelta is called for each content fragment as it arrives; the full Response
// is returned at the end (same shape as Complete). Clients that can't stream
// simply don't implement this — callers type-assert and fall back to Complete.
type StreamingClient interface {
	Client
	Stream(ctx context.Context, req Request, onDelta func(delta string)) (Response, error)
}
