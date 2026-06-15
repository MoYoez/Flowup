package aiaction

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/workflow"
)

type Generate struct {
	client model.Client
}

func New(client model.Client) *Generate {
	return &Generate{client: client}
}

func (*Generate) Definition() action.Definition {
	return action.Definition{
		Name: "ai.generate",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"prompt", "input", "output_schema"},
			"properties": map[string]any{
				"model":           map[string]any{"type": "string"},
				"models":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"prompt":          map[string]any{"type": "string"},
				"input":           map[string]any{},
				"output_schema":   map[string]any{"type": "object"},
				"max_attempts":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 120},
				"max_tokens":      map[string]any{"type": "integer", "minimum": 1, "maximum": 32768},
				"temperature":     map[string]any{"type": "number", "minimum": 0, "maximum": 2},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{},
		Timeout:      120 * time.Second,
	}
}

func (*Generate) Effect(map[string]any) action.EffectClass {
	return action.EffectReadOnly
}

func (a *Generate) Execute(ctx context.Context, invocation action.Invocation) (action.Result, error) {
	if a.client == nil {
		return action.Result{}, fmt.Errorf("model client is not configured")
	}
	projected := invocation.Input["input"]
	if workflow.ContainsSecret(projected) {
		return action.Result{}, fmt.Errorf("projected input cannot contain secret references")
	}
	models, err := modelList(invocation.Input)
	if err != nil {
		return action.Result{}, err
	}
	prompt, _ := invocation.Input["prompt"].(string)
	schema, ok := invocation.Input["output_schema"].(map[string]any)
	if !ok {
		return action.Result{}, fmt.Errorf("output_schema must be an object")
	}
	projectedJSON, err := json.Marshal(projected)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode projected input: %w", err)
	}
	maxAttempts := integerOption(invocation.Input, "max_attempts", 1)
	maxTokens := integerOption(invocation.Input, "max_tokens", 2048)
	timeout := time.Duration(integerOption(invocation.Input, "timeout_seconds", 30)) * time.Second
	temperature := numberOption(invocation.Input, "temperature", 0)
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		modelName := models[min(attempt-1, len(models)-1)]
		started := time.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		response, generateErr := a.client.Generate(attemptCtx, model.Request{
			Model: modelName, Prompt: prompt, Input: projectedJSON,
			OutputSchema: schema, MaxTokens: maxTokens, Temperature: temperature,
		})
		cancel()
		valid := false
		validationMessage := ""
		if generateErr == nil {
			var value any
			if err := json.Unmarshal(response.JSON, &value); err != nil {
				lastErr = fmt.Errorf("model output is not valid JSON")
				validationMessage = lastErr.Error()
			} else if err := action.ValidateSchema(schema, value); err != nil {
				lastErr = err
				validationMessage = err.Error()
			} else {
				valid = true
			}
		} else {
			lastErr = generateErr
			validationMessage = "model request failed"
		}
		if invocation.Events != nil {
			if err := invocation.Events.Emit(ctx, action.Event{
				Type: "ai.attempt",
				Data: map[string]any{
					"model":         modelName,
					"attempt":       attempt,
					"duration_ms":   time.Since(started).Milliseconds(),
					"input_tokens":  response.Usage.InputTokens,
					"output_tokens": response.Usage.OutputTokens,
					"valid":         valid,
					"validation":    validationMessage,
				},
			}); err != nil {
				return action.Result{}, fmt.Errorf("emit AI event: %w", err)
			}
		}
		if valid {
			return action.Result{Output: append(json.RawMessage(nil), response.JSON...)}, nil
		}
		if generateErr != nil && !model.IsTransient(generateErr) {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("AI generation failed")
	}
	return action.Result{}, fmt.Errorf("AI output validation failed after %d attempts: %w", maxAttempts, lastErr)
}

func modelList(input map[string]any) ([]string, error) {
	single, hasSingle := input["model"].(string)
	rawModels, hasModels := input["models"]
	if hasSingle && hasModels {
		return nil, fmt.Errorf("cannot specify both model and models")
	}
	if hasSingle {
		if single == "" {
			return nil, fmt.Errorf("model cannot be empty")
		}
		return []string{single}, nil
	}
	if hasModels {
		items, ok := rawModels.([]any)
		if !ok || len(items) == 0 {
			return nil, fmt.Errorf("models must be a non-empty array")
		}
		models := make([]string, len(items))
		for index, item := range items {
			name, ok := item.(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("models[%d] must be a non-empty string", index)
			}
			models[index] = name
		}
		return models, nil
	}
	return []string{"default"}, nil
}

func integerOption(input map[string]any, name string, fallback int) int {
	switch value := input[name].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

func numberOption(input map[string]any, name string, fallback float64) float64 {
	switch value := input[name].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return fallback
	}
}
