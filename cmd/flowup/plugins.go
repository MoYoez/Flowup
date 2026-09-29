package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/plugin"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/workflow"
)

func projectPluginRoot() string { return filepath.Join(".flowup", "plugins") }

func pluginCommand(stdout, stderr io.Writer, args []string) int {
	if len(args) != 2 || (args[0] != "install" && args[0] != "uninstall") {
		fmt.Fprintln(stderr, "usage: flowup plugin install <local directory or manifest> | flowup plugin uninstall <name>")
		return 2
	}
	if args[0] == "uninstall" {
		if err := plugin.Uninstall(projectPluginRoot(), args[1]); err != nil {
			fmt.Fprintf(stderr, "plugin: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "uninstalled: %s (saved runs retain their pinned files)\n", args[1])
		return 0
	}
	registry, err := coreRegistry()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bindings, err := plugin.Install(projectPluginRoot(), args[1], registry.Names())
	if err != nil {
		fmt.Fprintf(stderr, "plugin: %v\n", err)
		return 1
	}
	for _, b := range bindings {
		fmt.Fprintf(stdout, "installed: %s@%s\n", b.Spec.Name, b.Spec.Version)
	}
	fmt.Fprintf(stdout, "scope: current project (%s)\n", projectPluginRoot())
	return 0
}

func workflowRegistry(source []byte, manifest string) (*action.Registry, json.RawMessage, error) {
	wf, err := workflow.Parse(source)
	if err != nil {
		return nil, nil, err
	}
	var extra []action.Action
	var snapshot json.RawMessage
	if manifest != "" {
		extra, snapshot, err = plugin.Load(manifest)
	} else {
		names := map[string]bool{}
		for _, step := range wf.Steps {
			names[step.Uses] = true
		}
		extra, snapshot, err = plugin.Installed(projectPluginRoot(), names)
	}
	if err != nil {
		return nil, nil, err
	}
	registry, err := coreRegistry(extra...)
	return registry, snapshot, err
}

func savedRegistry(ctx context.Context, state store.Store, runID string) (*action.Registry, error) {
	run, err := state.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	// Inspecting an ended run or refusing a pending approval does not execute code.
	if run.Status != store.RunRunning && run.Status != store.RunWaitingApproval {
		return coreRegistry()
	}
	extra, err := plugin.Restore(run.PluginBindings)
	if err != nil {
		return nil, err
	}
	return coreRegistry(extra...)
}

// Plugin failures are already stripped of stderr and declared secret values by
// the adapter. Show these useful diagnostics rather than only "execute action".
func printPluginFailure(ctx context.Context, output io.Writer, state store.Store, run store.RunRecord) {
	if len(run.PluginBindings) == 0 {
		return
	}
	var snapshot plugin.Snapshot
	if json.Unmarshal(run.PluginBindings, &snapshot) != nil {
		return
	}
	names := map[string]bool{}
	for _, binding := range snapshot.Bindings {
		names[binding.Spec.Name] = true
	}
	steps, err := state.ListSteps(ctx, run.ID)
	if err != nil {
		return
	}
	for _, step := range steps {
		if names[step.ActionName] && step.Status == store.StepFailed && step.ErrorMessage != "" {
			fmt.Fprintf(output, "plugin step %s: %s\n", step.StepID, step.ErrorMessage)
		}
	}
}
