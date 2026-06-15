package jsonaction

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type Validate struct{}

func NewValidate() Validate {
	return Validate{}
}

func (Validate) Definition() action.Definition {
	return action.Definition{
		Name: "json.validate",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"value", "schema"},
			"properties": map[string]any{
				"value":  map[string]any{},
				"schema": map[string]any{"type": "object"},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{},
		Timeout:      5 * time.Second,
	}
}

func (Validate) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (Validate) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	schema, ok := invocation.Input["schema"].(map[string]any)
	if !ok {
		return action.Result{}, fmt.Errorf("schema must be an object")
	}
	value := invocation.Input["value"]
	if err := action.ValidateSchema(schema, value); err != nil {
		return action.Result{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode validated value: %w", err)
	}
	return action.Result{Output: raw}, nil
}
