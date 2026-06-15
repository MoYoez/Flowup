# Flowup v1 Connectors and Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add typed GitHub and Slack actions, warn about unapproved writes, delete the obsolete agent/distributed runtime, and finish the offline-tested v1 product surface.

**Architecture:** Connectors remain compiled-in actions using injected HTTP clients and base URLs for local tests. After connector and acceptance coverage passes, remove old commands, packages, examples, dependencies, and documentation in one controlled cleanup sequence.

**Tech Stack:** Go 1.26, standard-library HTTP and `httptest`, YAML, JSON Schema, modernc SQLite, GitHub REST API, Slack Web API

---

## File Map

| Path | Responsibility |
| --- | --- |
| `internal/connector/github/client.go` | authenticated GitHub REST transport |
| `internal/connector/github/issue.go` | issue read/comment actions |
| `internal/connector/github/pull_request.go` | pull request read/comment actions |
| `internal/connector/slack/client.go` | authenticated Slack Web API transport |
| `internal/connector/slack/message.go` | message read/send actions |
| `internal/workflow/validate.go` | write-without-approval warning |
| `internal/engine/acceptance_test.go` | complete offline user workflows |
| `README.md` | v1 product and CLI documentation |
| `docs/decisions.md` | approved v1 architecture decisions |
| `.github/workflows/ci.yml` | Go-only offline CI |
| `.gitignore` | track product documentation and ignore runtime artifacts |

### Task 1: Add the GitHub Client and Read Actions

**Files:**
- Create: `internal/connector/github/client.go`
- Create: `internal/connector/github/client_test.go`
- Create: `internal/connector/github/issue.go`
- Create: `internal/connector/github/pull_request.go`
- Create: `internal/connector/github/read_test.go`

- [ ] **Step 1: Write URL parsing and read-action tests**

```go
func TestParseIssueURL(t *testing.T) {
	ref, err := ParseURL("https://github.com/acme/widgets/issues/42")
	require.NoError(t, err)
	require.Equal(t, Ref{Owner: "acme", Repo: "widgets", Number: 42, Kind: "issue"}, ref)
}

func TestIssueGetUsesGitHubAPI(t *testing.T) {
	server := githubServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/acme/widgets/issues/42", r.URL.Path)
		require.Equal(t, "Bearer token-value", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"number":42,"title":"Broken","body":"Details","state":"open","html_url":"https://github.com/acme/widgets/issues/42"}`)
	})
	a := NewIssueGet(newTestClient(server.URL, "token-value"))
	result, err := a.Execute(context.Background(), invocation(map[string]any{
		"url": "https://github.com/acme/widgets/issues/42",
		"token": "token-value",
	}))
	require.NoError(t, err)
	require.JSONEq(t, `{"number":42,"title":"Broken","body":"Details","state":"open","url":"https://github.com/acme/widgets/issues/42"}`, string(result.Output))
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/connector/github -run 'Parse|Get' -v
```

Expected: FAIL because the connector does not exist.

- [ ] **Step 3: Implement the GitHub client**

```go
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Ref struct {
	Owner  string
	Repo   string
	Number int
	Kind   string
}

func ParseURL(raw string) (Ref, error)
func (c Client) DoJSON(ctx context.Context, method, path, token string, body any, output any) error
```

Accept only `https://github.com/<owner>/<repo>/issues/<number>` and
`https://github.com/<owner>/<repo>/pull/<number>` in production parsing.
Tests inject a separate API base URL. Send `Authorization: Bearer <token>`,
`Accept: application/vnd.github+json`, and
Limit responses to 1 MiB and sanitize non-2xx errors.

- [ ] **Step 4: Implement read actions**

`github.issue.get` calls `/repos/{owner}/{repo}/issues/{number}` and returns
number, title, body, state, and URL.

`github.pull_request.get` calls `/repos/{owner}/{repo}/pulls/{number}` and
returns number, title, body, state, merged, draft, head ref, base ref, and URL.

Both are read-only, use a 30-second timeout, and declare `token` as the only
secret path.

- [ ] **Step 5: Run GitHub read tests**

Run:

```powershell
go test ./internal/connector/github -v
```

Expected: PASS using only `httptest.Server`.

- [ ] **Step 6: Commit**

```powershell
git add internal/connector/github
git commit -m "feat: add github read actions"
```

### Task 2: Add GitHub Comment Actions

**Files:**
- Modify: `internal/connector/github/issue.go`
- Modify: `internal/connector/github/pull_request.go`
- Create: `internal/connector/github/write_test.go`

- [ ] **Step 1: Write comment tests**

```go
func TestIssueCommentPostsBody(t *testing.T) {
	server := githubServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/repos/acme/widgets/issues/42/comments", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "Investigating", body["body"])
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":99,"html_url":"https://github.com/acme/widgets/issues/42#issuecomment-99","body":"Investigating"}`)
	})
	// Execute and assert normalized output.
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/connector/github -run Comment -v
```

Expected: FAIL because comment actions are not registered.

- [ ] **Step 3: Implement comment actions**

`github.issue.comment` and `github.pull_request.comment` both accept URL, body,
and secret token. Both POST to GitHub's issue comments endpoint because pull
request conversation comments use `/issues/{number}/comments`. Return comment
ID, body, and URL. Classify both as external effects.

Do not retry inside the connector. The engine's effect record owns exactly-once
attempt semantics. If the process dies after GitHub accepted the comment but
before effect completion is persisted, resume must report
`effect_indeterminate`.

- [ ] **Step 4: Run GitHub tests**

Run:

```powershell
go test ./internal/connector/github -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/connector/github
git commit -m "feat: add github comment actions"
```

### Task 3: Add Slack Message Actions

**Files:**
- Create: `internal/connector/slack/client.go`
- Create: `internal/connector/slack/message.go`
- Create: `internal/connector/slack/message_test.go`

- [ ] **Step 1: Write Slack read/send tests**

```go
func TestMessageSend(t *testing.T) {
	server := slackServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/chat.postMessage", r.URL.Path)
		require.Equal(t, "Bearer slack-token", r.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "engineering", body["channel"])
		require.Equal(t, "Build failed", body["text"])
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1710000000.000100","message":{"text":"Build failed"}}`)
	})
	// Execute and assert normalized channel, timestamp, and text output.
}

func TestMessageGetSelectsExactTimestamp(t *testing.T) {
	// conversations.replies returns two messages; assert the requested ts is selected.
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/connector/slack -v
```

Expected: FAIL because the connector does not exist.

- [ ] **Step 3: Implement the Slack client**

```go
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c Client) Call(ctx context.Context, method, token string, input, output any) error
```

POST JSON to `/api/<method>` with bearer auth. Require HTTP 2xx and Slack
`{"ok":true}`. Return only Slack's short `error` code on API failure; do not
return response bodies or tokens. Limit responses to 1 MiB.

- [ ] **Step 4: Implement message actions**

`slack.message.get` accepts channel, timestamp, and token. Call
`conversations.replies` with channel and `ts`, then select the exact timestamp.
Return channel, timestamp, user, text, and thread timestamp. Mark read-only.

`slack.message.send` accepts channel, text, optional thread timestamp, and
token. Call `chat.postMessage`. Return channel, timestamp, and text. Mark as an
external effect.

Both use a 30-second timeout and declare only `token` as secret.

- [ ] **Step 5: Run Slack tests**

Run:

```powershell
go test ./internal/connector/slack -v
```

Expected: PASS using only `httptest.Server`.

- [ ] **Step 6: Commit**

```powershell
git add internal/connector/slack
git commit -m "feat: add slack message actions"
```

### Task 4: Warn About Write Actions Without Earlier Approval

**Files:**
- Modify: `internal/workflow/validate.go`
- Modify: `internal/workflow/validate_test.go`

- [ ] **Step 1: Write warning tests**

```go
func TestValidateWarnsWhenWriteHasNoEarlierApproval(t *testing.T) {
	warnings, err := Validate(mustParse(t, writeWorkflowWithoutApproval), connectorRegistry(t))
	require.NoError(t, err)
	require.Equal(t, []Warning{{
		Code: "write_without_approval",
		StepID: "notify",
		Message: `external-effect step "notify" has no earlier approval step`,
	}}, warnings)
}

func TestValidateDoesNotWarnWithEarlierApproval(t *testing.T) {
	warnings, err := Validate(mustParse(t, writeWorkflowWithApproval), connectorRegistry(t))
	require.NoError(t, err)
	require.Empty(t, warnings)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/workflow -run WriteWithoutApproval -v
```

Expected: FAIL because the warning is missing.

- [ ] **Step 3: Implement the v1 warning rule**

Walk steps in declaration order. Once an `approval` action has appeared, later
external-effect actions do not warn. Before that point, emit one deterministic
warning per external-effect step. Do not attempt conditional path proof in v1;
the warning means an approval is structurally earlier, not guaranteed to run.
Warnings remain non-fatal and are sorted by step index.

For dynamically classified actions such as `http.request`, treat an unresolved
method expression as external during static validation. Literal GET and HEAD
methods are read-only; other supported literal methods are external.

- [ ] **Step 4: Run workflow tests**

Run:

```powershell
go test ./internal/workflow -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/workflow
git commit -m "feat: warn about writes without approval"
```

### Task 5: Register Connectors and Add End-to-End Acceptance Tests

**Files:**
- Modify: `cmd/flowup/commands.go`
- Modify: `cmd/flowup/commands_test.go`
- Create: `internal/engine/acceptance_test.go`
- Create: `examples/v1/triage-issue.yaml`

- [ ] **Step 1: Write a complete offline acceptance test**

Build a registry with scripted AI and GitHub/Slack clients pointed at local
servers. Execute the approved triage shape:

```text
github.issue.get
ai.generate
switch
approval (high priority)
slack.message.send
```

Assert:

```go
require.Equal(t, store.RunWaitingApproval, initial.Status)
require.Equal(t, 0, slackSendCount)
resumed, err := engine.Approve(ctx, approvalID)
require.NoError(t, err)
require.Equal(t, store.RunSucceeded, resumed.Status)
require.Equal(t, 1, githubGetCount)
require.Equal(t, 1, aiCallCount)
require.Equal(t, 1, slackSendCount)
require.NotContains(t, allEventsJSON(t, runID), githubToken)
require.NotContains(t, allEventsJSON(t, runID), slackToken)
```

Close and reopen SQLite before approval to prove the pause survives process
exit.

- [ ] **Step 2: Run the acceptance test and confirm failure**

Run:

```powershell
go test ./internal/engine -run TestTriageIssueAcceptance -v
```

Expected: FAIL until connectors are wired into the registry and policy.

- [ ] **Step 3: Register all v1 actions**

The production registry contains exactly:

```text
http.request
json.select
json.validate
switch
ai.generate
approval
github.issue.get
github.issue.comment
github.pull_request.get
github.pull_request.comment
slack.message.get
slack.message.send
```

Keep constructors injected in tests. Production uses standard HTTP clients,
GitHub base `https://api.github.com`, and Slack base `https://slack.com`.

- [ ] **Step 4: Add the documented example**

Create `examples/v1/triage-issue.yaml` matching the approved design. It must use
explicit AI input projection and whole-field secret references. Do not include
real credentials.

- [ ] **Step 5: Run acceptance and CLI tests**

Run:

```powershell
go test ./internal/engine ./cmd/flowup -run 'Acceptance|Connector|Validate' -v
go run ./cmd/flowup validate examples/v1/triage-issue.yaml
```

Expected: tests pass. Validation prints `workflow valid`; a warning is allowed
only if the example intentionally contains a write before approval.

- [ ] **Step 6: Commit**

```powershell
git add cmd/flowup internal/engine/acceptance_test.go examples/v1/triage-issue.yaml
git commit -m "feat: wire official v1 connectors"
```

### Task 6: Delete the Quarantined Runtime

**Files:**
- Delete: `_legacy/`
- Delete: old files under `examples/`
- Delete: `deploy/`
- Delete: `model.env.example`

- [ ] **Step 1: Record the pre-deletion package list**

Run:

```powershell
go list ./...
```

Expected: only v1 packages are listed; `_legacy/` is ignored by the Go tool.

- [ ] **Step 2: Delete only the approved obsolete paths**

Use `git rm -r _legacy deploy model.env.example`. List `examples/` and remove
every old example except the `examples/v1/` directory created by Plans 1 and 3.

Do not remove:

```text
cmd/flowup
internal/action
internal/connector
internal/engine
internal/model
internal/policy
internal/store
internal/workflow
```

- [ ] **Step 3: Confirm the active tree has no old imports or build tags**

Run:

```powershell
rg -n "internal/(agent|contracts|dispatch|durable|mcp|pipelines|ratelimit|runner|sandbox|tools)|dbos|pgx|embedded-postgres|anthropic|opentelemetry" --glob '*.go' .
```

Expected: any matches identify v1 files that still import removed code. Replace
those imports with the v1 interfaces already defined; remove OTel build-tag
files rather than preserving an unused telemetry layer.

- [ ] **Step 4: Verify the reduced package graph**

Run:

```powershell
go list ./...
go test ./...
```

Expected: only `cmd/flowup` and the approved v1 internal packages are listed;
tests pass.

- [ ] **Step 5: Commit**

```powershell
git add -A
git commit -m "refactor: remove legacy agent runtime"
```

### Task 7: Remove Dependencies and Simplify CI

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Replace: `.github/workflows/ci.yml`
- Modify: `.gitignore`

- [ ] **Step 1: Remove obsolete documentation ignore rule**

Delete `/docs/` from `.gitignore`. Add only runtime artifacts:

```text
.flowup/
*.db
*.db-shm
*.db-wal
/flowup.exe
```

- [ ] **Step 2: Tidy dependencies**

Run:

```powershell
go mod tidy
go list -m all
```

Expected: DBOS, embedded Postgres, pgx, Fiber, Anthropic-specific packages,
OpenTelemetry, and old agent-loop-only dependencies are absent. Keep only
packages imported by v1.

- [ ] **Step 3: Replace CI**

CI runs on Windows and Ubuntu with:

```yaml
- run: go test ./...
- run: go vet ./...
- run: go build ./cmd/flowup
```

Do not start Docker/Postgres, set provider API keys, use build tags, or call
external services.

- [ ] **Step 4: Verify CI commands locally**

Run:

```powershell
go test ./...
go vet ./...
go build ./cmd/flowup
```

Expected: all commands exit `0`.

- [ ] **Step 5: Commit**

```powershell
git add .gitignore .github/workflows/ci.yml go.mod go.sum
git commit -m "build: simplify v1 dependencies and ci"
```

### Task 8: Rewrite Product Documentation

**Files:**
- Replace: `README.md`
- Replace: `docs/decisions.md`
- Delete: `docs/agent-pipeline-tech-research.md`
- Delete: `docs/agent-pipeline-infra-build-plan.md`

- [ ] **Step 1: Rewrite README around the actual v1 surface**

Include:

1. One-sentence product definition.
2. Trust model: workflow/runtime trusted; AI and service responses untrusted.
3. Installation with `go install`.
4. The v1 YAML example.
5. Exact CLI commands.
6. Secret environment-variable behavior.
7. Approval pause and resume example.
8. Supported core and connector actions.
9. Explicit non-goals: no agents, shell, plugins, triggers, web UI, distributed
   workers, or old format compatibility.
10. Offline test command.

Do not mention removed binaries, DBOS, Postgres, MCP, autonomous tools, or
automatic execution.

- [ ] **Step 2: Rewrite decisions**

Record dated decisions for:

```text
Manual CLI execution
Single ordered step list
Registered declarative actions only
Explicit AI input projection
Whole-field secret references
Explicit durable approval
SQLite-only persistence
No old-format compatibility
Indeterminate rather than repeated external effects
```

Each entry contains decision, reason, and consequence in three short
paragraphs.

- [ ] **Step 3: Delete obsolete research/build documents**

Delete the two named documents only after confirming their still-relevant
decisions appear in `docs/decisions.md`.

- [ ] **Step 4: Scan documentation**

Run:

```powershell
rg -n "agent loop|DBOS|Postgres|MCP|worker|automatic trigger|needs_human|pipeline/kind/profile/tools/needs" README.md docs examples
```

Expected: no stale product claims. A decision explaining that these features
were removed is allowed when clearly historical.

- [ ] **Step 5: Commit**

```powershell
git add README.md docs examples
git commit -m "docs: document simplified flowup v1"
```

### Task 9: Final Acceptance and Legacy Rejection

**Files:**
- Create: `internal/workflow/legacy_test.go`
- Modify: `internal/engine/acceptance_test.go`
- Modify: `cmd/flowup/commands_test.go`

- [ ] **Step 1: Add explicit legacy format rejection**

```go
func TestParseRejectsLegacyPipelineFormat(t *testing.T) {
	_, err := Parse([]byte(`
pipeline: old
kind: agent
profile: default
tools: [http]
nodes: []
`))
	require.Error(t, err)
}
```

- [ ] **Step 2: Add CLI exit-code acceptance tests**

Assert:

```text
invalid workflow -> exit 2, stable invalid_workflow message
missing input -> exit 2, stable invalid_inputs message
action failure -> exit 1, sanitized code and message
approval rejection -> exit 1, status rejected
indeterminate effect -> exit 1, effect_indeterminate
```

Fixture secrets and fake provider response bodies must not appear in stdout,
stderr, events, or stored redacted step inputs.

- [ ] **Step 3: Run complete verification**

Run:

```powershell
go test ./...
go test ./... -count=10
go vet ./...
go build ./cmd/flowup
```

Expected: every command exits `0` without Docker, Postgres, API keys, or
external network access.

- [ ] **Step 4: Scan for obsolete architecture and unsafe execution**

Run:

```powershell
rg -n "os/exec|exec\\.Command|tool_call|needs_human|DBOS|Postgres|Anthropic|cmd/worker|cmd/engine|internal/(agent|dispatch|durable|mcp|runner|sandbox|tools)" cmd internal README.md examples .github go.mod
```

Expected: no matches.

- [ ] **Step 5: Commit final acceptance coverage**

```powershell
git add internal/workflow/legacy_test.go internal/engine/acceptance_test.go cmd/flowup/commands_test.go
git commit -m "test: verify flowup v1 acceptance criteria"
```
