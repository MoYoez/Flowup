package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/mcp"
)

type stubHandler struct{}

func (stubHandler) Tools() []mcp.Tool {
	return []mcp.Tool{{Name: "run_pipeline", Description: "run it", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (stubHandler) Call(_ context.Context, name string, _ json.RawMessage) (string, bool) {
	if name == "run_pipeline" {
		return `{"status":"ok"}`, false
	}
	return "unknown", true
}

func TestServeHandshakeAndCall(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, // notification: no reply
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_pipeline","arguments":{}}}`,
	}, "\n")

	var out bytes.Buffer
	if err := mcp.Serve(context.Background(), strings.NewReader(in), &out, "flowup", "0.1", stubHandler{}); err != nil {
		t.Fatal(err)
	}

	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := sonic.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	if len(resps) != 3 {
		t.Fatalf("want 3 responses (notification gets none), got %d", len(resps))
	}

	// initialize
	if r := resps[0]["result"].(map[string]any); r["protocolVersion"] == nil || r["serverInfo"] == nil {
		t.Fatalf("initialize result malformed: %v", r)
	}
	// tools/list
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "run_pipeline" {
		t.Fatalf("tools/list malformed: %v", tools)
	}
	// tools/call
	res := resps[2]["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatal("tools/call should not be an error")
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"status":"ok"`) {
		t.Fatalf("tools/call text missing result: %q", text)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":9,"method":"bogus"}`
	var out bytes.Buffer
	_ = mcp.Serve(context.Background(), strings.NewReader(in), &out, "flowup", "0.1", stubHandler{})
	var m map[string]any
	_ = sonic.Unmarshal([]byte(strings.TrimSpace(out.String())), &m)
	if m["error"] == nil {
		t.Fatalf("unknown method should return a JSON-RPC error, got %v", m)
	}
}
