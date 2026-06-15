package action

import (
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func ValidateSchema(schema map[string]any, value any) error {
	compiler := jsonschema.NewCompiler()
	const location = "mem://flowup/schema.json"
	if err := compiler.AddResource(location, schema); err != nil {
		return fmt.Errorf("add JSON schema: %w", err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return fmt.Errorf("compile JSON schema: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("value does not validate: %w", err)
	}
	return nil
}
