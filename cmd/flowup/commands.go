package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moyoez/flowup/internal/action"
	jsonaction "github.com/moyoez/flowup/internal/action/json"
	switchaction "github.com/moyoez/flowup/internal/action/switch"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/workflow"
)

const defaultDatabasePath = ".flowup/flowup.db"

func runCLI(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		return validateCommand(stdout, stderr, args[1:])
	case "run":
		return runCommand(ctx, stdout, stderr, args[1:])
	case "status":
		return statusCommand(ctx, stdout, stderr, args[1:])
	case "trace":
		return traceCommand(ctx, stdout, stderr, args[1:])
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func validateCommand(stdout, stderr io.Writer, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: flowup validate <workflow.yaml>")
		return 2
	}
	source, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "invalid_workflow: read workflow: %v\n", err)
		return 2
	}
	warnings, err := validateSource(source)
	if err != nil {
		fmt.Fprintf(stderr, "invalid_workflow: %v\n", err)
		return 2
	}
	for _, warning := range warnings {
		fmt.Fprintf(stdout, "warning[%s] %s: %s\n", warning.Code, warning.StepID, warning.Message)
	}
	fmt.Fprintln(stdout, "workflow valid")
	return 0
}

func runCommand(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]")
		return 2
	}
	workflowPath := args[0]
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputsPath := flags.String("inputs", "", "path to workflow inputs JSON")
	databasePath := flags.String("db", defaultDatabasePath, "path to SQLite database")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *inputsPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]")
		return 2
	}
	source, err := os.ReadFile(workflowPath)
	if err != nil {
		fmt.Fprintf(stderr, "invalid_workflow: read workflow: %v\n", err)
		return 2
	}
	rawInputs, err := os.ReadFile(*inputsPath)
	if err != nil {
		fmt.Fprintf(stderr, "invalid_inputs: read inputs: %v\n", err)
		return 2
	}
	var inputs map[string]any
	if err := json.Unmarshal(rawInputs, &inputs); err != nil {
		fmt.Fprintf(stderr, "invalid_inputs: decode inputs: %v\n", err)
		return 2
	}
	state, err := openStore(*databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: %v\n", err)
		return 1
	}
	defer state.Close()
	registry, err := coreRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "internal: %v\n", err)
		return 1
	}
	run, err := engine.New(state, registry).Start(ctx, source, inputs)
	if err != nil {
		var coded *engine.Error
		if engine.AsError(err, &coded) && (coded.Code == engine.CodeInvalidWorkflow || coded.Code == engine.CodeInvalidInputs) {
			fmt.Fprintln(stderr, coded.Error())
			return 2
		}
		fmt.Fprintln(stderr, err)
		if run.ID != "" {
			printRun(stdout, run)
		}
		return 1
	}
	printRun(stdout, run)
	return 0
}

func statusCommand(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	runID, databasePath, ok := parseIDAndDatabase(stderr, "status", args)
	if !ok {
		return 2
	}
	state, err := openStore(databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: %v\n", err)
		return 1
	}
	defer state.Close()
	run, err := state.GetRun(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: load run: %v\n", err)
		return 1
	}
	printRun(stdout, run)
	steps, err := state.ListSteps(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: list steps: %v\n", err)
		return 1
	}
	for _, step := range steps {
		fmt.Fprintf(stdout, "%s: %s\n", step.StepID, step.Status)
	}
	return 0
}

func traceCommand(ctx context.Context, stdout, stderr io.Writer, args []string) int {
	runID, databasePath, ok := parseIDAndDatabase(stderr, "trace", args)
	if !ok {
		return 2
	}
	state, err := openStore(databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: %v\n", err)
		return 1
	}
	defer state.Close()
	events, err := state.ListEvents(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "persistence: list events: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		line := struct {
			ID        int64           `json:"id"`
			RunID     string          `json:"run_id"`
			StepID    string          `json:"step_id,omitempty"`
			Type      string          `json:"type"`
			Data      json.RawMessage `json:"data"`
			CreatedAt string          `json:"created_at"`
		}{
			ID: event.ID, RunID: event.RunID, StepID: event.StepID, Type: event.Type,
			Data: event.Data, CreatedAt: event.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if err := encoder.Encode(line); err != nil {
			fmt.Fprintf(stderr, "persistence: encode event: %v\n", err)
			return 1
		}
	}
	return 0
}

func parseIDAndDatabase(stderr io.Writer, command string, args []string) (string, string, bool) {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "usage: flowup %s <run-id> [--db <path>]\n", command)
		return "", "", false
	}
	id := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("db", defaultDatabasePath, "path to SQLite database")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return "", "", false
	}
	return id, *databasePath, true
}

func validateSource(source []byte) ([]workflow.Warning, error) {
	wf, err := workflow.Parse(source)
	if err != nil {
		return nil, err
	}
	registry, err := coreRegistry()
	if err != nil {
		return nil, err
	}
	return workflow.Validate(wf, registry)
}

func coreRegistry() (*action.Registry, error) {
	return action.NewRegistry(
		jsonaction.NewSelect(),
		jsonaction.NewValidate(),
		switchaction.New(),
	)
}

func openStore(path string) (*store.SQLiteStore, error) {
	if path != ":memory:" {
		path = filepath.Clean(path)
	}
	return store.OpenSQLite(path)
}

func printRun(output io.Writer, run store.RunRecord) {
	fmt.Fprintf(output, "run_id: %s\n", run.ID)
	fmt.Fprintf(output, "status: %s\n", run.Status)
	if len(run.Output) > 0 {
		fmt.Fprintf(output, "output: %s\n", run.Output)
	}
	if run.ErrorCode != "" {
		fmt.Fprintf(output, "error: %s: %s\n", run.ErrorCode, run.ErrorMessage)
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  flowup validate <workflow.yaml>")
	fmt.Fprintln(output, "  flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]")
	fmt.Fprintln(output, "  flowup status <run-id> [--db <path>]")
	fmt.Fprintln(output, "  flowup trace <run-id> [--db <path>]")
}
