package model_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/moyoez/flowup/internal/model"
)

// TestOpenAIStreamLive verifies token streaming against a real OpenAI-compatible
// endpoint. Gated: set OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL to run it
// (e.g. `set -a; source model.env; set +a`). Skipped in CI.
func TestOpenAIStreamLive(t *testing.T) {
	if os.Getenv("OPENAI_BASE_URL") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("set OPENAI_BASE_URL/OPENAI_API_KEY/OPENAI_MODEL to run the live streaming test")
	}
	c := model.NewOpenAI()

	deltas := 0
	var sb strings.Builder
	resp, err := c.Stream(context.Background(), model.Request{
		System:    "Reply with exactly: hello from the streaming model",
		Messages:  []model.Message{{Role: model.RoleUser, Content: "Go."}},
		MaxTokens: 256,
	}, func(d string) {
		deltas++
		sb.WriteString(d)
	})
	if err != nil {
		t.Fatal(err)
	}
	if deltas < 2 {
		t.Fatalf("expected multiple streamed deltas (token-by-token), got %d", deltas)
	}
	if resp.Text == "" || resp.Text != sb.String() {
		t.Fatalf("assembled text mismatch: resp=%q accumulated=%q", resp.Text, sb.String())
	}
	t.Logf("streamed %d deltas; text=%q", deltas, resp.Text)
}
