package workflow

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/moyoez/flowup/internal/action"
)

type Warning struct {
	Code    string
	StepID  string
	Message string
}

type ActionCatalog interface {
	Get(string) (action.Action, bool)
}

func Validate(wf Workflow, actions ActionCatalog) ([]Warning, error) {
	positions := make(map[string]int, len(wf.Steps))
	var warnings []Warning
	seenApproval := false
	for index, step := range wf.Steps {
		positions[step.ID] = index
	}

	for index, step := range wf.Steps {
		candidate, ok := actions.Get(step.Uses)
		if !ok {
			return nil, fmt.Errorf("step %q uses unknown action %q", step.ID, step.Uses)
		}
		if err := validateValueExpressions(step.With); err != nil {
			return nil, fmt.Errorf("step %q input: %w", step.ID, err)
		}
		refs, err := References(step.With)
		if err != nil {
			return nil, fmt.Errorf("step %q input references: %w", step.ID, err)
		}
		if err := validateReferences(refs, wf.Inputs, positions, index, step.ID); err != nil {
			return nil, err
		}

		if step.If != "" {
			conditionRefs, err := References(step.If)
			if err != nil {
				return nil, fmt.Errorf("step %q condition: %w", step.ID, err)
			}
			if err := validateReferences(conditionRefs, wf.Inputs, positions, index, step.ID); err != nil {
				return nil, err
			}
			for _, ref := range conditionRefs {
				if ref.Kind == ReferenceSecret {
					return nil, fmt.Errorf("step %q condition cannot reference secrets", step.ID)
				}
			}
			if _, err := EvaluateCondition(step.If, sampleContext(wf.Inputs, conditionRefs)); err != nil {
				return nil, fmt.Errorf("step %q condition: %w", step.ID, err)
			}
		}

		definition := candidate.Definition()
		if err := validateSecretPlacement(step.With, definition.SecretPaths, definition.Name); err != nil {
			return nil, fmt.Errorf("step %q input: %w", step.ID, err)
		}
		if len(refs) == 0 {
			if err := action.ValidateSchema(definition.InputSchema, step.With); err != nil {
				return nil, fmt.Errorf("step %q input: %w", step.ID, err)
			}
		} else if err := validateObjectStructure(definition.InputSchema, step.With); err != nil {
			return nil, fmt.Errorf("step %q input: %w", step.ID, err)
		}
		effect := candidate.Effect(step.With)
		if definition.Name == "http.request" {
			method, _ := step.With["method"].(string)
			if method == "GET" || method == "HEAD" {
				effect = action.EffectReadOnly
			} else {
				effect = action.EffectExternal
			}
		}
		if effect == action.EffectExternal && !seenApproval {
			warnings = append(warnings, Warning{
				Code:   "write_without_approval",
				StepID: step.ID,
				Message: fmt.Sprintf(
					"external-effect step %q has no earlier approval step",
					step.ID,
				),
			})
		}
		if definition.Name == "approval" {
			seenApproval = true
		}
	}

	outputRefs, err := References(wf.Outputs)
	if err != nil {
		return nil, fmt.Errorf("workflow outputs: %w", err)
	}
	for _, ref := range outputRefs {
		switch ref.Kind {
		case ReferenceInput:
			if _, ok := wf.Inputs[ref.Name]; !ok {
				return nil, fmt.Errorf("workflow output references unknown input %q", ref.Name)
			}
		case ReferenceStep:
			if _, ok := positions[ref.Name]; !ok {
				return nil, fmt.Errorf("workflow output references unknown step %q", ref.Name)
			}
		case ReferenceSecret:
			return nil, fmt.Errorf("workflow output cannot reference secrets")
		}
	}
	return warnings, nil
}

func validateSecretPlacement(value any, allowedPaths []string, actionName string) error {
	var visit func(any, string) error
	visit = func(current any, path string) error {
		switch typed := current.(type) {
		case string:
			_, ok, err := ParseSecretReference(typed)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if actionName == "ai.generate" && (path == "input" || strings.HasPrefix(path, "input.")) {
				return fmt.Errorf("AI input cannot contain secret references")
			}
			if !secretPathAllowed(path, allowedPaths) {
				return fmt.Errorf("secret reference is not allowed at %q", path)
			}
		case map[string]any:
			for key, item := range typed {
				child := key
				if path != "" {
					child = path + "." + key
				}
				if err := visit(item, child); err != nil {
					return err
				}
			}
		case []any:
			for index, item := range typed {
				if err := visit(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value, "")
}

func secretPathAllowed(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == path {
			return true
		}
		if strings.HasSuffix(pattern, ".*") {
			prefix := strings.TrimSuffix(pattern, "*")
			remainder := strings.TrimPrefix(path, prefix)
			if strings.HasPrefix(path, prefix) && remainder != "" && !strings.Contains(remainder, ".") {
				return true
			}
		}
	}
	return false
}

func ValidateInputs(specs map[string]InputSpec, values map[string]any) error {
	for name, spec := range specs {
		value, ok := values[name]
		if !ok {
			if spec.Required {
				return fmt.Errorf("required input %q is missing", name)
			}
			continue
		}
		if !matchesInputType(value, spec.Type) {
			return fmt.Errorf("input %q must be %s", name, spec.Type)
		}
	}
	for name := range values {
		if _, ok := specs[name]; !ok {
			return fmt.Errorf("unknown input %q", name)
		}
	}
	return nil
}

func matchesInputType(value any, expected string) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return true
		default:
			return false
		}
	case "integer":
		switch typed := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			return typed == math.Trunc(typed)
		default:
			return false
		}
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func validateReferences(
	refs []Reference,
	inputs map[string]InputSpec,
	positions map[string]int,
	currentIndex int,
	currentStep string,
) error {
	for _, ref := range refs {
		switch ref.Kind {
		case ReferenceInput:
			if _, ok := inputs[ref.Name]; !ok {
				return fmt.Errorf("step %q references unknown input %q", currentStep, ref.Name)
			}
		case ReferenceStep:
			position, ok := positions[ref.Name]
			if !ok {
				return fmt.Errorf("step %q references unknown step %q", currentStep, ref.Name)
			}
			if position >= currentIndex {
				return fmt.Errorf("step %q references later step %q", currentStep, ref.Name)
			}
		}
	}
	return nil
}

func validateValueExpressions(value any) error {
	switch typed := value.(type) {
	case string:
		matches := embeddedExpressionPattern.FindAllStringSubmatch(typed, -1)
		for _, match := range matches {
			if _, err := parseReference(strings.TrimSpace(match[1])); err != nil {
				return err
			}
		}
		if strings.Contains(typed, "${{") && len(matches) == 0 {
			return fmt.Errorf("malformed expression %q", typed)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := validateValueExpressions(typed[key]); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case []any:
		for index, item := range typed {
			if err := validateValueExpressions(item); err != nil {
				return fmt.Errorf("[%d]: %w", index, err)
			}
		}
	}
	return nil
}

func validateObjectStructure(schema, value map[string]any) error {
	required, _ := schema["required"].([]any)
	for _, item := range required {
		name, _ := item.(string)
		if _, ok := value[name]; !ok {
			return fmt.Errorf("required property %q is missing", name)
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		properties, _ := schema["properties"].(map[string]any)
		for name := range value {
			if _, ok := properties[name]; !ok {
				return fmt.Errorf("property %q is not allowed", name)
			}
		}
	}
	return nil
}

func sampleContext(inputs map[string]InputSpec, refs []Reference) Context {
	context := Context{
		Inputs: make(map[string]any, len(inputs)),
		Steps:  map[string]StepValue{},
	}
	for name, spec := range inputs {
		context.Inputs[name] = sampleInput(spec.Type)
	}
	for _, ref := range refs {
		if ref.Kind != ReferenceStep {
			continue
		}
		step := context.Steps[ref.Name]
		step.Status = "succeeded"
		if strings.HasPrefix(ref.Path, "output") {
			step.Output = addSampleOutputPath(step.Output, strings.TrimPrefix(ref.Path, "output"))
		}
		context.Steps[ref.Name] = step
	}
	return context
}

func sampleInput(inputType string) any {
	switch inputType {
	case "boolean":
		return true
	case "number", "integer":
		return float64(1)
	case "object":
		return map[string]any{}
	case "array":
		return []any{}
	default:
		return "sample"
	}
}

func addSampleOutputPath(current any, path string) any {
	if path == "" {
		if current == nil {
			return "sample"
		}
		return current
	}
	segments := strings.Split(strings.TrimPrefix(path, "."), ".")
	root, ok := current.(map[string]any)
	if !ok {
		root = map[string]any{}
	}
	cursor := root
	for index, segment := range segments {
		if index == len(segments)-1 {
			cursor[segment] = "sample"
			break
		}
		next, ok := cursor[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[segment] = next
		}
		cursor = next
	}
	return root
}
