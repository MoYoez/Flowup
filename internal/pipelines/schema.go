package pipelines

import (
	"bytes"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateAgainstSchema validates a decoded JSON value (map[string]any, etc.)
// against a JSON-schema-shaped definition (as parsed from YAML). It returns a
// concise error — the caller is responsible for never leaking it raw across the
// three-state boundary.
func ValidateAgainstSchema(schema map[string]any, instance any) error {
	if len(schema) == 0 {
		return nil // no schema = nothing to enforce
	}
	sch, err := compileSchema(schema)
	if err != nil {
		return err
	}
	if err := sch.Validate(instance); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}

// ValidateJSONAgainstSchema is a convenience wrapper that decodes raw JSON first.
func ValidateJSONAgainstSchema(schema map[string]any, raw []byte) error {
	if len(schema) == 0 {
		return nil
	}
	var v any
	if err := sonic.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("decode instance: %w", err)
	}
	return ValidateAgainstSchema(schema, v)
}

func compileSchema(schema map[string]any) (*jsonschema.Schema, error) {
	b, err := sonic.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	const ref = "mem://schema.json"
	if err := c.AddResource(ref, bytes.NewReader(b)); err != nil {
		return nil, fmt.Errorf("add schema: %w", err)
	}
	sch, err := c.Compile(ref)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return sch, nil
}
