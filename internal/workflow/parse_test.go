package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseValidWorkflow(t *testing.T) {
	raw := []byte(`
name: local-triage
version: 1
inputs:
  payload:
    type: object
    required: true
steps:
  - id: select
    uses: json.select
    with:
      value: ${{ inputs.payload }}
      paths:
        title: title
outputs:
  title: ${{ steps.select.output.title }}
`)

	wf, err := Parse(raw)

	require.NoError(t, err)
	require.Equal(t, "local-triage", wf.Name)
	require.Equal(t, "json.select", wf.Steps[0].Uses)
}

func TestParseRejectsDuplicateStepIDs(t *testing.T) {
	_, err := Parse([]byte(`
name: duplicate
version: 1
steps:
  - id: same
    uses: switch
    with: {value: a, cases: {default: a}}
  - id: same
    uses: switch
    with: {value: b, cases: {default: b}}
`))

	require.ErrorContains(t, err, `duplicate step id "same"`)
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
name: unknown-field
version: 1
unexpected: true
steps:
  - id: route
    uses: switch
`))

	require.ErrorContains(t, err, "field unexpected not found")
}

func TestParseRejectsUnsupportedVersion(t *testing.T) {
	_, err := Parse([]byte(`
name: future
version: 2
steps:
  - id: route
    uses: switch
`))

	require.ErrorContains(t, err, "unsupported workflow version 2")
}

func TestParseRejectsMalformedInputDeclaration(t *testing.T) {
	_, err := Parse([]byte(`
name: bad-input
version: 1
inputs:
  payload:
    type: map
steps:
  - id: route
    uses: switch
`))

	require.ErrorContains(t, err, `input "payload" has unsupported type "map"`)
}

func TestParseRejectsInvalidStepID(t *testing.T) {
	_, err := Parse([]byte(`
name: bad-step
version: 1
steps:
  - id: 1bad
    uses: switch
`))

	require.ErrorContains(t, err, `invalid step id "1bad"`)
}
