package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"

	"gopkg.in/yaml.v3"
)

var stepIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

var supportedInputTypes = map[string]struct{}{
	"string":  {},
	"number":  {},
	"integer": {},
	"boolean": {},
	"object":  {},
	"array":   {},
}

func Parse(source []byte) (Workflow, error) {
	var wf Workflow
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wf); err != nil {
		return Workflow{}, fmt.Errorf("decode workflow: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return Workflow{}, fmt.Errorf("decode trailing workflow document: %w", err)
	} else if err == nil {
		return Workflow{}, errors.New("workflow must contain exactly one YAML document")
	}

	if err := validateShape(wf); err != nil {
		return Workflow{}, err
	}
	return wf, nil
}

func validateShape(wf Workflow) error {
	if wf.Name == "" {
		return errors.New("workflow name is required")
	}
	if wf.Version != 1 {
		return fmt.Errorf("unsupported workflow version %d", wf.Version)
	}
	if len(wf.Steps) == 0 {
		return errors.New("workflow requires at least one step")
	}

	for name, spec := range wf.Inputs {
		if _, ok := supportedInputTypes[spec.Type]; !ok {
			return fmt.Errorf("input %q has unsupported type %q", name, spec.Type)
		}
	}

	seen := make(map[string]struct{}, len(wf.Steps))
	for index, step := range wf.Steps {
		if !stepIDPattern.MatchString(step.ID) {
			return fmt.Errorf("invalid step id %q", step.ID)
		}
		if step.Uses == "" {
			return fmt.Errorf("step %q action is required", step.ID)
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		seen[step.ID] = struct{}{}
		if step.With == nil {
			wf.Steps[index].With = map[string]any{}
		}
	}
	return nil
}
