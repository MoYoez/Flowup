package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestValidateCommand(t *testing.T) {
	workflowPath := writeFixture(t, "valid.yaml", `
name: valid
version: 1
steps:
  - id: route
    uses: switch
    with: {value: high, cases: {high: notify, default: stop}}
`)
	var stdout, stderr bytes.Buffer

	code := runCLI(context.Background(), &stdout, &stderr, []string{"validate", workflowPath})

	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "workflow valid")
	require.Empty(t, stderr.String())
}

func TestValidateCommandReturnsUsageExitForInvalidWorkflow(t *testing.T) {
	workflowPath := writeFixture(t, "invalid.yaml", `
name: invalid
version: 1
steps:
  - id: route
    uses: missing.action
`)
	var stdout, stderr bytes.Buffer

	code := runCLI(context.Background(), &stdout, &stderr, []string{"validate", workflowPath})

	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "invalid_workflow")
}

func TestRunAndStatusCommands(t *testing.T) {
	workflowPath := writeFixture(t, "valid.yaml", `
name: local
version: 1
inputs:
  priority: {type: string, required: true}
steps:
  - id: route
    uses: switch
    with:
      value: ${{ inputs.priority }}
      cases: {high: notify, default: ignore}
outputs:
  route: ${{ steps.route.output }}
`)
	inputsPath := writeFixture(t, "inputs.json", `{"priority":"high"}`)
	dbPath := filepath.Join(t.TempDir(), "flowup.db")
	var stdout, stderr bytes.Buffer

	code := runCLI(context.Background(), &stdout, &stderr, []string{
		"run", workflowPath, "--inputs", inputsPath, "--db", dbPath,
	})

	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "status: succeeded")
	require.Contains(t, stdout.String(), `output: {"route":"notify"}`)
	runID := outputField(t, stdout.String(), "run_id")

	stdout.Reset()
	stderr.Reset()
	code = runCLI(context.Background(), &stdout, &stderr, []string{
		"status", runID, "--db", dbPath,
	})

	require.Equal(t, 0, code, stderr.String())
	require.Contains(t, stdout.String(), "status: succeeded")
	require.Contains(t, stdout.String(), "route: succeeded")
}

func TestTraceCommandPrintsJSONEvents(t *testing.T) {
	workflowPath := writeFixture(t, "valid.yaml", `
name: trace
version: 1
steps:
  - id: route
    uses: switch
    with: {value: high, cases: {default: notify}}
`)
	inputsPath := writeFixture(t, "inputs.json", `{}`)
	dbPath := filepath.Join(t.TempDir(), "flowup.db")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, runCLI(context.Background(), &stdout, &stderr, []string{
		"run", workflowPath, "--inputs", inputsPath, "--db", dbPath,
	}), stderr.String())
	runID := outputField(t, stdout.String(), "run_id")

	stdout.Reset()
	stderr.Reset()
	code := runCLI(context.Background(), &stdout, &stderr, []string{
		"trace", runID, "--db", dbPath,
	})

	require.Equal(t, 0, code, stderr.String())
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	require.JSONEq(t, lines[0], lines[0])
	require.Contains(t, stdout.String(), `"type":"run.started"`)
	require.Contains(t, stdout.String(), `"type":"run.succeeded"`)
}

func outputField(t *testing.T, output, name string) string {
	t.Helper()
	prefix := name + ": "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("field %q not found in output %q", name, output)
	return ""
}
