# Flowup v1 Rewrite Roadmap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current agent and distributed-worker prototype with the approved manual, sequential, semi-trusted Go workflow engine.

**Architecture:** Execute the rewrite as three independently verifiable plans. Preserve a tracked pre-rewrite baseline first, move the prototype under Go-ignored `_legacy/`, then build the new engine, add trust and recovery behavior, and finally delete the quarantined code and obsolete dependencies.

**Tech Stack:** Go 1.26, YAML v3, JSON Schema, modernc SQLite, standard-library HTTP and `httptest`

---

## Required Reading

- Design: `docs/superpowers/specs/2026-06-15-flowup-v1-simplified-workflow-design.md`
- Plan 1: `docs/superpowers/plans/2026-06-15-flowup-v1-core-workflow-plan.md`
- Plan 2: `docs/superpowers/plans/2026-06-15-flowup-v1-trusted-execution-plan.md`
- Plan 3: `docs/superpowers/plans/2026-06-15-flowup-v1-connectors-cleanup-plan.md`

## Execution Order

1. Capture the currently untracked implementation as the historical baseline.
2. Execute Plan 1 completely; it quarantines the prototype and verifies a deterministic workflow can be validated, run, inspected, and traced.
3. Execute Plan 2 completely and verify secrets, AI projection, approval, idempotency, and crash recovery.
4. Execute Plan 3 completely and verify GitHub/Slack connectors, destructive cleanup, documentation, and final acceptance.

Do not run the three plans in parallel. They intentionally modify shared engine,
store, workflow, and CLI contracts.

### Task 1: Capture the Pre-v1 Baseline

**Files:**
- Track: `.github/`
- Track: `.gitignore`
- Track: `README.md`
- Track: `cmd/`
- Track: `deploy/`
- Track: `examples/`
- Track: `go.mod`
- Track: `go.sum`
- Track: `internal/`
- Track: `model.env.example`

- [ ] **Step 1: Confirm only the design and plan documents are currently tracked**

Run:

```powershell
git status --short
git ls-files
```

Expected: implementation paths appear as `??`; the design and plan documents
are the only project documents already tracked.

- [ ] **Step 2: Run the old baseline tests before recording it**

Run:

```powershell
go test ./...
go vet ./...
```

Expected: both commands exit `0`. Do not require `go test -race` because the
current Windows environment may have CGO disabled.

- [ ] **Step 3: Commit the untouched implementation baseline**

Run:

```powershell
git add .github .gitignore README.md cmd deploy examples go.mod go.sum internal model.env.example
git commit -m "chore: capture pre-v1 implementation baseline"
```

Expected: one commit containing the existing implementation without functional
changes.

### Task 2: Execute the Three Plans

- [ ] **Step 1: Execute Plan 1**

Open and complete every checkbox in:

```text
docs/superpowers/plans/2026-06-15-flowup-v1-core-workflow-plan.md
```

Exit gate:

```powershell
go test ./internal/workflow/... ./internal/action/... ./internal/store/... ./internal/engine/... ./cmd/flowup/...
go run ./cmd/flowup validate examples/v1/local-triage.yaml
```

Expected: tests pass and validation prints `workflow valid`.

- [ ] **Step 2: Execute Plan 2**

Open and complete every checkbox in:

```text
docs/superpowers/plans/2026-06-15-flowup-v1-trusted-execution-plan.md
```

Exit gate:

```powershell
go test ./internal/policy/... ./internal/model/... ./internal/action/http/... ./internal/action/ai/... ./internal/action/approval/... ./internal/engine/... ./cmd/flowup/...
```

Expected: tests pass without API keys or network access.

- [ ] **Step 3: Execute Plan 3**

Open and complete every checkbox in:

```text
docs/superpowers/plans/2026-06-15-flowup-v1-connectors-cleanup-plan.md
```

Exit gate:

```powershell
go test ./...
go vet ./...
go build ./cmd/flowup
git status --short
```

Expected: all commands pass; only intentional local artifacts, if any, remain
untracked.

### Task 3: Final Acceptance Review

- [ ] **Step 1: Check removed architecture is absent**

Run:

```powershell
rg -n "needs_human|cmd/worker|cmd/engine|internal/agent|internal/dispatch|internal/durable|internal/mcp|internal/runner|internal/sandbox|internal/tools|Anthropic|DBOS|Postgres" README.md cmd internal examples .github go.mod
```

Expected: no product or code references. Test fixture text explaining a rejected
legacy format is allowed only when explicitly named `legacy`.

- [ ] **Step 2: Run the offline acceptance suite**

Run:

```powershell
go test ./...
go vet ./...
go build ./cmd/flowup
```

Expected: all commands exit `0` without Docker, Postgres, external services, or
credentials.

- [ ] **Step 3: Review the final diff against the approved design**

Run:

```powershell
git diff ac33750..HEAD --stat
git log --oneline --decorate ac33750..HEAD
```

Expected: commits follow the three-plan sequence and every acceptance criterion
in the design maps to a passing test or documented CLI smoke check.
