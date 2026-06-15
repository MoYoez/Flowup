package trace_test

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/trace"
)

func TestRedactMasksSecretKeys(t *testing.T) {
	in := []byte(`{"deployment_id":"d1","api_key":"sk-SECRET-1","nested":{"password":"PW-2","ok":1},"list":[{"token":"TK-3"}],"url":"u"}`)
	out := trace.Redact(in)
	s := string(out)
	for _, leak := range []string{"sk-SECRET-1", "PW-2", "TK-3"} {
		if strings.Contains(s, leak) {
			t.Fatalf("secret leaked: %s still contains %q", s, leak)
		}
	}
	var m map[string]any
	if err := sonic.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["deployment_id"] != "d1" || m["url"] != "u" {
		t.Fatalf("non-secret fields were altered: %v", m)
	}
	if m["api_key"] != trace.RedactMask {
		t.Fatalf("api_key not masked: %v", m["api_key"])
	}
	nested := m["nested"].(map[string]any)
	if nested["password"] != trace.RedactMask || nested["ok"].(float64) != 1 {
		t.Fatalf("nested redaction wrong: %v", nested)
	}
}

func TestRedactPassesThroughNonJSON(t *testing.T) {
	if string(trace.Redact([]byte("not json"))) != "not json" {
		t.Fatal("non-JSON data should pass through unchanged")
	}
	if trace.Redact(nil) != nil {
		t.Fatal("nil should pass through")
	}
}
