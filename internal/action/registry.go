package action

import (
	"fmt"
	"sort"
)

type Registry struct {
	actions map[string]Action
	names   []string
}

func NewRegistry(actions ...Action) (*Registry, error) {
	registry := &Registry{
		actions: make(map[string]Action, len(actions)),
		names:   make([]string, 0, len(actions)),
	}
	for _, candidate := range actions {
		if candidate == nil {
			return nil, fmt.Errorf("action is nil")
		}
		definition := candidate.Definition()
		if definition.Name == "" {
			return nil, fmt.Errorf("action name is required")
		}
		if definition.InputSchema == nil {
			return nil, fmt.Errorf("action %q input schema is required", definition.Name)
		}
		if definition.OutputSchema == nil {
			return nil, fmt.Errorf("action %q output schema is required", definition.Name)
		}
		if definition.Timeout <= 0 {
			return nil, fmt.Errorf("action %q requires a positive timeout", definition.Name)
		}
		if _, exists := registry.actions[definition.Name]; exists {
			return nil, fmt.Errorf("duplicate action %q", definition.Name)
		}
		registry.actions[definition.Name] = candidate
		registry.names = append(registry.names, definition.Name)
	}
	sort.Strings(registry.names)
	return registry, nil
}

func (r *Registry) Get(name string) (Action, bool) {
	if r == nil {
		return nil, false
	}
	candidate, ok := r.actions[name]
	return candidate, ok
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.names...)
}
