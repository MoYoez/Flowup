package approvalaction

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type Approval struct{}

func New() Approval {
	return Approval{}
}

func (Approval) Definition() action.Definition {
	return action.Definition{
		Name: "approval",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"message"},
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"preview": map[string]any{},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []any{"approved"},
			"properties": map[string]any{
				"approved": map[string]any{"type": "boolean"},
				"reason":   map[string]any{"type": "string"},
			},
		},
		Timeout: 5 * time.Second,
	}
}

func (Approval) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (Approval) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	message, _ := invocation.Input["message"].(string)
	preview, err := json.Marshal(invocation.Input["preview"])
	if err != nil {
		return action.Result{}, fmt.Errorf("encode approval preview: %w", err)
	}
	if _, ok := invocation.Input["preview"]; !ok {
		preview = json.RawMessage(`null`)
	}
	return action.Result{Pause: &action.Pause{
		Kind: "approval", Message: message, Preview: preview,
	}}, nil
}
