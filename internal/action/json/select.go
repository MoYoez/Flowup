package jsonaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type Select struct{}

func NewSelect() Select {
	return Select{}
}

func (Select) Definition() action.Definition {
	return action.Definition{
		Name: "json.select",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"value", "paths"},
			"properties": map[string]any{
				"value": map[string]any{},
				"paths": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{"type": "object"},
		Timeout:      5 * time.Second,
	}
}

func (Select) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (Select) Execute(_ context.Context, invocation action.Invocation) (action.Result, error) {
	paths, ok := invocation.Input["paths"].(map[string]any)
	if !ok {
		return action.Result{}, fmt.Errorf("paths must be an object")
	}
	output := make(map[string]any, len(paths))
	for name, rawPath := range paths {
		path, ok := rawPath.(string)
		if !ok || path == "" {
			return action.Result{}, fmt.Errorf("path %q must be a non-empty string", name)
		}
		value, err := selectPath(invocation.Input["value"], path)
		if err != nil {
			return action.Result{}, err
		}
		output[name] = value
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode selected value: %w", err)
	}
	return action.Result{Output: raw}, nil
}

func selectPath(value any, path string) (any, error) {
	current := value
	for _, segment := range strings.Split(path, ".") {
		if _, ok := current.([]any); ok {
			return nil, fmt.Errorf("path %q uses array indexing; array indexing is not supported", path)
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q traverses a non-object", path)
		}
		next, ok := object[segment]
		if !ok {
			return nil, fmt.Errorf("path %q is missing", path)
		}
		current = next
	}
	return current, nil
}
