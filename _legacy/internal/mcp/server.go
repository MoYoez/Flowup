// Package mcp is a minimal Model Context Protocol (JSON-RPC 2.0 over stdio)
// server. It exposes Flowup's skill to any MCP-capable host without binding to a
// specific one. It implements initialize, tools/list and tools/call.
package mcp

import (
	"context"
	"encoding/json"
	"io"

	"github.com/bytedance/sonic"
)

const protocolVersion = "2024-11-05"

// Tool is an MCP tool advertised to the host.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Handler backs the exposed tools.
type Handler interface {
	Tools() []Tool
	// Call runs a tool and returns text content; isError marks a tool-level error.
	Call(ctx context.Context, name string, args json.RawMessage) (text string, isError bool)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the JSON-RPC loop until in is exhausted or ctx is done.
func Serve(ctx context.Context, in io.Reader, out io.Writer, name, version string, h Handler) error {
	dec := sonic.ConfigDefault.NewDecoder(in)
	enc := sonic.ConfigDefault.NewEncoder(out)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(req.ID) == 0 {
			continue // a notification (e.g. notifications/initialized): no reply
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			resp.Result = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": name, "version": version},
			}
		case "tools/list":
			resp.Result = map[string]any{"tools": h.Tools()}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = sonic.Unmarshal(req.Params, &p)
			text, isErr := h.Call(ctx, p.Name, p.Arguments)
			resp.Result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}
		case "ping":
			resp.Result = map[string]any{}
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}
