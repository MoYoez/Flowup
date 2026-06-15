package slackconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
)

type messageAction struct {
	client Client
	send   bool
}

func NewMessageGet(client Client) action.Action {
	return messageAction{client: client}
}

func NewMessageSend(client Client) action.Action {
	return messageAction{client: client, send: true}
}

func (a messageAction) Definition() action.Definition {
	properties := map[string]any{
		"channel": map[string]any{"type": "string"},
		"token":   map[string]any{"type": "string"},
	}
	required := []any{"channel", "token"}
	name := "slack.message.get"
	if a.send {
		name = "slack.message.send"
		properties["text"] = map[string]any{"type": "string"}
		properties["thread_timestamp"] = map[string]any{"type": "string"}
		properties["idempotency_key"] = map[string]any{"type": "string"}
		required = append(required, "text")
	} else {
		properties["timestamp"] = map[string]any{"type": "string"}
		required = append(required, "timestamp")
	}
	return action.Definition{
		Name: name,
		InputSchema: map[string]any{
			"type": "object", "required": required, "properties": properties,
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{"type": "object"},
		SecretPaths:  []string{"token"},
		Timeout:      30 * time.Second,
	}
}

func (a messageAction) Effect(map[string]any) action.EffectClass {
	if a.send {
		return action.EffectExternal
	}
	return action.EffectReadOnly
}

func (a messageAction) Execute(ctx context.Context, invocation action.Invocation) (action.Result, error) {
	channel := invocation.Input["channel"].(string)
	token := invocation.Input["token"].(string)
	if a.send {
		input := map[string]any{
			"channel": channel,
			"text":    invocation.Input["text"],
		}
		if thread, ok := invocation.Input["thread_timestamp"].(string); ok && thread != "" {
			input["thread_ts"] = thread
		}
		var response struct {
			Channel string `json:"channel"`
			TS      string `json:"ts"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
		}
		if err := a.client.Call(ctx, "chat.postMessage", token, input, &response); err != nil {
			return action.Result{}, err
		}
		return slackResult(map[string]any{
			"channel": response.Channel, "timestamp": response.TS, "text": response.Message.Text,
		})
	}
	timestamp := invocation.Input["timestamp"].(string)
	var response struct {
		Messages []struct {
			TS       string `json:"ts"`
			User     string `json:"user"`
			Text     string `json:"text"`
			ThreadTS string `json:"thread_ts"`
		} `json:"messages"`
	}
	if err := a.client.Call(ctx, "conversations.replies", token, map[string]any{
		"channel": channel, "ts": timestamp,
	}, &response); err != nil {
		return action.Result{}, err
	}
	for _, message := range response.Messages {
		if message.TS == timestamp {
			return slackResult(map[string]any{
				"channel": channel, "timestamp": message.TS, "user": message.User,
				"text": message.Text, "thread_timestamp": message.ThreadTS,
			})
		}
	}
	return action.Result{}, fmt.Errorf("Slack message %s was not found", timestamp)
}

func slackResult(value any) (action.Result, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode Slack action output: %w", err)
	}
	return action.Result{Output: raw}, nil
}
