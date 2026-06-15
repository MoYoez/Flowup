package workflow

import (
	"testing"

	"github.com/moyoez/flowup/internal/action"
	switchaction "github.com/moyoez/flowup/internal/action/switch"
	"github.com/stretchr/testify/require"
)

func testRegistry(t *testing.T) *action.Registry {
	t.Helper()
	registry, err := action.NewRegistry(switchaction.New())
	require.NoError(t, err)
	return registry
}

func mustParse(t *testing.T, source string) Workflow {
	t.Helper()
	wf, err := Parse([]byte(source))
	require.NoError(t, err)
	return wf
}

func TestValidateRejectsLaterStepReference(t *testing.T) {
	wf := mustParse(t, `
name: bad-order
version: 1
steps:
  - id: first
    uses: switch
    with: {value: "${{ steps.second.output }}", cases: {default: x}}
  - id: second
    uses: switch
    with: {value: x, cases: {default: x}}
`)

	_, err := Validate(wf, testRegistry(t))

	require.ErrorContains(t, err, `step "first" references later step "second"`)
}

func TestValidateRejectsUnknownAction(t *testing.T) {
	wf := mustParse(t, `
name: unknown
version: 1
steps:
  - id: nope
    uses: missing.action
`)

	_, err := Validate(wf, testRegistry(t))

	require.ErrorContains(t, err, `unknown action "missing.action"`)
}

func TestValidateRejectsMissingActionInput(t *testing.T) {
	wf := mustParse(t, `
name: missing-input
version: 1
steps:
  - id: route
    uses: switch
    with:
      value: high
`)

	_, err := Validate(wf, testRegistry(t))

	require.ErrorContains(t, err, `step "route" input`)
	require.ErrorContains(t, err, "cases")
}

func TestValidateRejectsInvalidCondition(t *testing.T) {
	wf := mustParse(t, `
name: bad-condition
version: 1
inputs:
  enabled: {type: boolean}
steps:
  - id: route
    uses: switch
    if: ${{ contains(inputs.enabled, true) }}
    with: {value: high, cases: {default: notify}}
`)

	_, err := Validate(wf, testRegistry(t))

	require.ErrorContains(t, err, `step "route" condition`)
}

func TestValidateRejectsUnknownWorkflowOutputStep(t *testing.T) {
	wf := mustParse(t, `
name: bad-output
version: 1
steps:
  - id: route
    uses: switch
    with: {value: high, cases: {default: notify}}
outputs:
  result: ${{ steps.missing.output }}
`)

	_, err := Validate(wf, testRegistry(t))

	require.ErrorContains(t, err, `workflow output references unknown step "missing"`)
}

func TestValidateAcceptsReferencesToEarlierSteps(t *testing.T) {
	wf := mustParse(t, `
name: valid
version: 1
steps:
  - id: first
    uses: switch
    with: {value: high, cases: {default: notify}}
  - id: second
    uses: switch
    with: {value: "${{ steps.first.output }}", cases: {notify: done, default: stop}}
`)

	warnings, err := Validate(wf, testRegistry(t))

	require.NoError(t, err)
	require.Empty(t, warnings)
}

func TestValidateInputsChecksRequiredAndTypes(t *testing.T) {
	specs := map[string]InputSpec{
		"name":    {Type: "string", Required: true},
		"enabled": {Type: "boolean"},
	}

	require.NoError(t, ValidateInputs(specs, map[string]any{"name": "flowup", "enabled": true}))
	require.ErrorContains(t, ValidateInputs(specs, map[string]any{"enabled": true}), `required input "name"`)
	require.ErrorContains(t, ValidateInputs(specs, map[string]any{"name": 3}), `input "name" must be string`)
	require.ErrorContains(t, ValidateInputs(specs, map[string]any{"name": "flowup", "extra": true}), `unknown input "extra"`)
}
