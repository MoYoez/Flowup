package switchaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type Switch struct{}

func New() Switch {
	return Switch{}
}

func (Switch) Definition() action.Definition {
	return action.Definition{
		Name: "switch",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"value", "cases"},
			"properties": map[string]any{
				"value": map[string]any{
					"type": []any{"string", "number", "integer", "boolean", "null"},
				},
				"cases": map[string]any{"type": "object"},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{},
		Timeout:      5 * time.Second,
	}
}

func (Switch) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (Switch) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	cases, ok := invocation.Input["cases"].(map[string]any)
	if !ok {
		return action.Result{}, fmt.Errorf("cases must be an object")
	}
	key, err := caseKey(invocation.Input["value"])
	if err != nil {
		return action.Result{}, err
	}
	selected, ok := cases[key]
	if !ok {
		selected, ok = cases["default"]
	}
	if !ok {
		return action.Result{}, fmt.Errorf("no matching case and no default")
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode selected case: %w", err)
	}
	return action.Result{Output: raw}, nil
}

func caseKey(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case nil:
		return "null", nil
	default:
		return "", fmt.Errorf("switch value must be scalar, got %T", value)
	}
}
