// Package tools is the node tool layer: a registry of callable tools, gated by
// each node's profile.tools whitelist. The agent loop hands the model only the
// whitelisted specs, and routes the model's tool calls here. Tools that touch
// the outside (shell, fs) run inside the sandbox Backend; http_get is a bounded
// in-process fetch.
package tools

import (
	"context"
	"encoding/json"

	"github.com/moyoez/flowup/internal/model"
)

// Tool is one callable capability.
type Tool interface {
	Name() string
	Spec() model.ToolSpec
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds tools by name and exposes only whitelisted ones to a node.
type Registry struct {
	byName map[string]Tool
}

// NewRegistry indexes the given tools by name.
func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(ts))}
	for _, t := range ts {
		r.byName[t.Name()] = t
	}
	return r
}

// Get returns a tool if it exists.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Allowed reports whether name is in the whitelist AND registered.
func (r *Registry) Allowed(name string, whitelist []string) bool {
	if _, ok := r.byName[name]; !ok {
		return false
	}
	for _, w := range whitelist {
		if w == name {
			return true
		}
	}
	return false
}

// Specs returns the ToolSpecs for the whitelisted, registered tools (what the
// model is told it may use).
func (r *Registry) Specs(whitelist []string) []model.ToolSpec {
	var out []model.ToolSpec
	for _, w := range whitelist {
		if t, ok := r.byName[w]; ok {
			out = append(out, t.Spec())
		}
	}
	return out
}

func objSchema(required string, props string) json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["` + required + `"],"properties":` + props + `}`)
}
