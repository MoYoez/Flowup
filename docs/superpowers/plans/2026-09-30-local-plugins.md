# Local Plugins Implementation Plan

> Execute in this session. The user has selected local plugins; the scope and protocol are recorded in the accompanying design. Tasks share CLI/store/engine contracts and are implemented sequentially, followed by independent review.

**Goal:** Run trusted local action plugins through existing Flowup workflows without rebuilding the CLI.

**Architecture:** A new `internal/plugin` package parses explicit manifests, resolves and fingerprints local executable bindings, and adapts JSON subprocess calls to `action.Action`. The CLI persists the binding snapshot through an engine option and a backward-compatible SQLite column, then restores it for continuation.

**Tech Stack:** Go standard library processes/JSON/hashing, existing YAML and JSON Schema packages, SQLite, Python example.

## Tasks

- [x] Add black-box CLI subprocess tests in `cmd/flowup/plugins_test.go`; run `go test ./cmd/flowup -run LocalPlugin -count=1` and confirm failure on unsupported `--plugins`.
- [x] Add `internal/plugin/manifest.go` for strict manifest validation, command resolution, declared-file fingerprints and snapshot restoration; add `process.go` for bounded JSON subprocess execution. Adapt through the existing `Definition`, `Effect`, `Prepare` and `Execute` contracts.
- [x] Add immutable `PluginBindings` to `store.RunRecord`; migrate existing SQLite databases with a nullable `plugin_bindings_json` column; write it in CreateRun and load in GetRun. Preserve it through existing transitions. Add `engine.WithPluginBindings` to copy the snapshot before CreateRun.
- [x] Add CLI loading in `cmd/flowup/plugins.go`; extend validate/run flags; resolve saved bindings before approve/resume, leaving reject/status/trace independent of plugin availability. Reject built-in name collisions.
- [x] Incorporate the user's install/uninstall requirement: copy packages into project-local version directories, atomically update discovery, retain saved-run versions, and test uninstall/update followed by approval.
- [x] Run focused subprocess tests, then extend coverage for malformed responses, secret inheritance/redaction, schema mismatch, timeout, write retry and pinned-file drift. Verify an old database can still be opened and queried.
- [x] Add `examples/plugins/text-stats` with Python action, manifest, workflow and inputs; add `docs/plugins.md` and update README/usage/decisions to describe actual plugin behavior and limits.
- [x] Run `go test ./...`, `go vet ./...`, `git diff --check`, build CLI and run the example through validate/run/approve/status/trace. Independently review requirements and correctness. Fix wildcard/array error redaction, secret-bearing schema diagnostics, absolute copied-file arguments, reserved payload names and Unix environment-name handling. Verify installation, uninstall while paused, and successful continuation with a real Python interpreter on Windows.

## Acceptance commands

```sh
go test ./cmd/flowup -run LocalPlugin -count=1
go test ./internal/store -count=1
go test ./...
go vet ./...
go build -o bin/flowup ./cmd/flowup
```

Only generic examples and documentation belong in the change. Existing ignored private workflows, generated reports and credentials remain outside it.
