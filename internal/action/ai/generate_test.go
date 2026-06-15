package aiaction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/workflow"
	"github.com/stretchr/testify/require"
)

type capturedEvents struct {
	events []action.Event
}

func (sink *capturedEvents) Emit(_ context.Context, event action.Event) error {
	sink.events = append(sink.events, event)
	return nil
}

func TestGenerateReceivesOnlyProjectedInput(t *testing.T) {
	client := model.NewScripted(model.Script{Response: model.Response{
		Model: "small",
		JSON:  json.RawMessage(`{"priority":"high"}`),
	}})
	candidate := New(client)

	result, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"models": []any{"small"},
		"prompt": "Classify",
		"input":  map[string]any{"title": "Build failure"},
		"output_schema": map[string]any{
			"type":     "object",
			"required": []any{"priority"},
			"properties": map[string]any{
				"priority": map[string]any{"type": "string"},
			},
		},
		"max_attempts": float64(1),
	}})

	require.NoError(t, err)
	require.JSONEq(t, `{"priority":"high"}`, string(result.Output))
	require.JSONEq(t, `{"title":"Build failure"}`, string(client.Requests()[0].Input))
}

func TestGenerateRetriesInvalidOutputThenUsesFallback(t *testing.T) {
	client := model.NewScripted(
		model.Script{Response: model.Response{Model: "small", JSON: json.RawMessage(`{"wrong":true}`)}},
		model.Script{Response: model.Response{Model: "large", JSON: json.RawMessage(`{"priority":"low"}`)}},
	)
	events := &capturedEvents{}
	candidate := New(client)

	result, err := candidate.Execute(context.Background(), action.Invocation{
		Events: events,
		Input: map[string]any{
			"models": []any{"small", "large"},
			"prompt": "Classify",
			"input":  map[string]any{"title": "Build failure"},
			"output_schema": map[string]any{
				"type":     "object",
				"required": []any{"priority"},
				"properties": map[string]any{
					"priority": map[string]any{"type": "string"},
				},
			},
			"max_attempts": float64(2),
		},
	})

	require.NoError(t, err)
	require.JSONEq(t, `{"priority":"low"}`, string(result.Output))
	requests := client.Requests()
	require.Equal(t, []string{"small", "large"}, []string{requests[0].Model, requests[1].Model})
	require.Len(t, events.events, 2)
	require.Equal(t, false, events.events[0].Data["valid"])
	require.Equal(t, true, events.events[1].Data["valid"])
}

func TestGenerateRejectsSecretInProjectedInput(t *testing.T) {
	client := model.NewScripted()
	candidate := New(client)

	_, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"model":         "small",
		"prompt":        "Classify",
		"input":         map[string]any{"token": workflow.SecretRef{Name: "TOKEN"}},
		"output_schema": map[string]any{"type": "object"},
	}})

	require.ErrorContains(t, err, "projected input")
}

func TestGenerateRejectsBothModelForms(t *testing.T) {
	candidate := New(model.NewScripted())

	_, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"model":         "small",
		"models":        []any{"large"},
		"prompt":        "Classify",
		"input":         map[string]any{},
		"output_schema": map[string]any{"type": "object"},
	}})

	require.ErrorContains(t, err, "cannot specify both model and models")
}

type blockingClient struct{}

func (blockingClient) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	select {
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	case <-time.After(1500 * time.Millisecond):
		return model.Response{}, errors.New("client timeout")
	}
}

func TestGenerateHonorsTimeoutSeconds(t *testing.T) {
	candidate := New(blockingClient{})
	started := time.Now()

	_, err := candidate.Execute(context.Background(), action.Invocation{Input: map[string]any{
		"model":           "small",
		"prompt":          "Classify",
		"input":           map[string]any{},
		"output_schema":   map[string]any{"type": "object"},
		"timeout_seconds": 1,
	}})

	require.Error(t, err)
	require.Less(t, time.Since(started), 1300*time.Millisecond)
}
