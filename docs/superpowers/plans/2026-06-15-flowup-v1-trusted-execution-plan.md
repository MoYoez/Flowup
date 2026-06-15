# Flowup v1 Trusted Execution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce the semi-trusted execution boundary with secret isolation, bounded HTTP, structured AI generation, durable approval, effect idempotency, and crash-safe resume behavior.

**Architecture:** Keep workflow resolution separate from policy enforcement. The engine resolves public references, policy validates and resolves secret-bearing fields only at the action boundary, and the store records redacted inputs plus durable approval/effect state.

**Tech Stack:** Go 1.26, standard-library HTTP, JSON Schema, modernc SQLite, OpenAI-compatible non-streaming HTTP API

---

## File Map

| Path | Responsibility |
| --- | --- |
| `internal/workflow/secret.go` | parse secret references without resolving values |
| `internal/policy/secrets.go` | secret placement, lookup, and redaction |
| `internal/policy/network.go` | scheme, host, timeout, and response-size policy |
| `internal/policy/action.go` | action-boundary input/output enforcement |
| `internal/action/http/request.go` | bounded HTTP action |
| `internal/model/model.go` | structured completion contract |
| `internal/model/openai.go` | OpenAI-compatible JSON completion |
| `internal/model/scripted.go` | deterministic test client |
| `internal/action/ai/generate.go` | projected schema-constrained generation |
| `internal/action/approval/approval.go` | durable pause request |
| `internal/store/approvals.go` | approval creation and decisions |
| `internal/store/effects.go` | effect idempotency state |
| `internal/engine/recovery.go` | approval and interrupted-step recovery |
| `cmd/flowup/commands.go` | approve, reject, and resume commands |

### Task 1: Represent Secret References Without Resolving Them

**Files:**
- Create: `internal/workflow/secret.go`
- Create: `internal/workflow/secret_test.go`
- Modify: `internal/workflow/expression.go`

- [ ] **Step 1: Write secret parsing tests**

```go
func TestParseSecretReferenceRequiresCompleteValue(t *testing.T) {
	ref, ok, err := ParseSecretReference("${{ secrets.GITHUB_TOKEN }}")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "GITHUB_TOKEN", ref.Name)

	_, _, err = ParseSecretReference("Bearer ${{ secrets.GITHUB_TOKEN }}")
	require.ErrorContains(t, err, "must occupy the complete value")
}

func TestResolveLeavesSecretReferenceOpaque(t *testing.T) {
	got, err := Resolve("${{ secrets.GITHUB_TOKEN }}", Context{})
	require.NoError(t, err)
	require.Equal(t, SecretRef{Name: "GITHUB_TOKEN"}, got)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/workflow -run Secret -v
```

Expected: FAIL because secret references are not represented.

- [ ] **Step 3: Implement opaque secret references**

```go
type SecretRef struct {
	Name string
}

func ParseSecretReference(value string) (SecretRef, bool, error)
func ContainsSecret(value any) bool
```

Only `${{ secrets.NAME }}` is valid. `NAME` must match
`^[A-Za-z_][A-Za-z0-9_]*$`. Any string containing `secrets.` without being one
complete expression is an error. `Resolve` returns `SecretRef` instead of
looking up a value. `References` reports secret references with kind `secret`
so static validation can enforce placement.

- [ ] **Step 4: Run workflow tests**

Run:

```powershell
go test ./internal/workflow -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/workflow
git commit -m "feat: preserve opaque secret references"
```

### Task 2: Enforce Secret Placement and Redaction

**Files:**
- Create: `internal/policy/secrets.go`
- Create: `internal/policy/secrets_test.go`
- Create: `internal/policy/action.go`
- Modify: `internal/workflow/validate.go`
- Modify: `internal/workflow/validate_test.go`

- [ ] **Step 1: Write policy tests**

Use wildcard secret paths such as `headers.*`:

```go
func TestResolveSecretsOnlyAtDeclaredPaths(t *testing.T) {
	input := map[string]any{
		"url": "https://api.example.test",
		"headers": map[string]any{"Authorization": workflow.SecretRef{Name: "TOKEN"}},
	}
	got, redacted, err := ResolveSecrets(input, []string{"headers.*"}, MapSecrets{"TOKEN": "secret-value"})
	require.NoError(t, err)
	require.Equal(t, "secret-value", got["headers"].(map[string]any)["Authorization"])
	require.Equal(t, "[REDACTED]", redacted["headers"].(map[string]any)["Authorization"])
}

func TestResolveSecretsRejectsNonSecretField(t *testing.T) {
	_, _, err := ResolveSecrets(
		map[string]any{"url": workflow.SecretRef{Name: "TOKEN"}},
		[]string{"headers.*"},
		MapSecrets{"TOKEN": "secret-value"},
	)
	require.ErrorContains(t, err, `secret reference is not allowed at "url"`)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/policy ./internal/workflow -run 'Secret|Validate' -v
```

Expected: FAIL because policy APIs do not exist.

- [ ] **Step 3: Implement secret lookup and redaction**

```go
type SecretSource interface {
	Lookup(name string) (string, bool)
}

type MapSecrets map[string]string

func (m MapSecrets) Lookup(name string) (string, bool) {
	value, ok := m[name]
	return value, ok
}

type EnvSecrets struct{}

func (EnvSecrets) Lookup(name string) (string, bool) {
	return os.LookupEnv(name)
}

func ResolveSecrets(input map[string]any, allowedPaths []string, source SecretSource) (
	resolved map[string]any,
	redacted map[string]any,
	err error,
)
func Redact(value any) any
```

Path matching supports exact object paths and a terminal `.*` wildcard. Secret
values exist only in `resolved`; `redacted` substitutes `[REDACTED]`. `Redact`
also masks defense-in-depth key names matching token, secret, password,
authorization, api_key, or private_key, case-insensitively.

- [ ] **Step 4: Add static secret placement checks**

For each action, compare discovered secret references against
`Definition.SecretPaths`. Reject secrets in conditions, workflow outputs,
non-secret fields, and every nested path under `ai.generate.with.input`.
Missing secret values remain a runtime error because `validate` must not require
credentials.

- [ ] **Step 5: Run policy and workflow tests**

Run:

```powershell
go test ./internal/policy ./internal/workflow -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/policy internal/workflow
git commit -m "feat: enforce secret boundaries"
```

### Task 3: Add Network Policy and `http.request`

**Files:**
- Create: `internal/policy/network.go`
- Create: `internal/policy/network_test.go`
- Create: `internal/action/http/request.go`
- Create: `internal/action/http/request_test.go`

- [ ] **Step 1: Write network and HTTP tests**

```go
func TestNetworkPolicyRejectsDisallowedSchemeAndHost(t *testing.T) {
	p := NetworkPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}
	require.Error(t, p.ValidateURL("file:///etc/passwd"))
	require.Error(t, p.ValidateURL("https://other.example.com/data"))
	require.NoError(t, p.ValidateURL("https://api.example.com/data"))
}

func TestRequestEnforcesResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 33))
	}))
	defer server.Close()
	a := New(server.Client(), policy.NetworkPolicy{
		AllowedSchemes: []string{"http"}, AllowedHosts: []string{hostOf(t, server.URL)},
		MaxResponseBytes: 32,
	})
	_, err := a.Execute(context.Background(), invocation(map[string]any{"method": "GET", "url": server.URL}))
	require.ErrorContains(t, err, "response exceeds 32 bytes")
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/policy ./internal/action/http -run 'Network|Request' -v
```

Expected: FAIL because network policy and action do not exist.

- [ ] **Step 3: Implement network policy**

```go
type NetworkPolicy struct {
	AllowedSchemes   []string
	AllowedHosts     []string
	MaxResponseBytes int64
}

func (p NetworkPolicy) ValidateURL(raw string) error
```

Defaults are schemes `https` and `http`, no host restriction, and a 1 MiB
response limit. Reject missing hosts, URL credentials, fragments, and schemes
outside the allowlist. When `FLOWUP_ALLOWED_HOSTS` is set, split it by commas
and require an exact case-insensitive hostname match.

- [ ] **Step 4: Implement `http.request`**

Inputs:

```json
{
  "method": "GET",
  "url": "https://example.test/path",
  "headers": {"Authorization": "${{ secrets.API_TOKEN }}"},
  "query": {"page": "1"},
  "body": {"optional": true}
}
```

Output:

```json
{"status":200,"headers":{"Content-Type":["application/json"]},"body":{"ok":true}}
```

Allow methods GET, HEAD, POST, PUT, PATCH, and DELETE. Classify GET/HEAD as
read-only and all others as external effects. Encode object/array bodies as
JSON, limit request execution to 30 seconds, read through
`io.LimitReader(max+1)`, parse JSON responses when content type or body is JSON,
and otherwise return a UTF-8 string. Secret paths are `headers.*`.

- [ ] **Step 5: Run HTTP tests**

Run:

```powershell
go test ./internal/policy ./internal/action/http -v
```

Expected: PASS; all requests use `httptest.Server`.

- [ ] **Step 6: Commit**

```powershell
git add internal/policy/network.go internal/policy/network_test.go internal/action/http
git commit -m "feat: add bounded http action"
```

### Task 4: Replace Model Clients with Structured Generation

**Files:**
- Replace: `internal/model/model.go`
- Replace: `internal/model/scripted.go`
- Create: `internal/model/openai.go`
- Replace: `internal/model/model_test.go`
- Delete: `internal/model/anthropic.go`
- Delete: `internal/model/anthropic_test.go`

- [ ] **Step 1: Write model contract tests**

```go
func TestScriptedClientReturnsResponsesInOrder(t *testing.T) {
	client := NewScripted(
		Response{Model: "first", JSON: json.RawMessage(`{"value":1}`)},
		Response{Model: "second", JSON: json.RawMessage(`{"value":2}`)},
	)
	first, err := client.Generate(context.Background(), Request{Model: "first"})
	require.NoError(t, err)
	require.JSONEq(t, `{"value":1}`, string(first.JSON))
	second, err := client.Generate(context.Background(), Request{Model: "second"})
	require.NoError(t, err)
	require.JSONEq(t, `{"value":2}`, string(second.JSON))
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/model -v
```

Expected: FAIL because the old model interface streams tool-loop events.

- [ ] **Step 3: Define the non-streaming model contract**

```go
type Request struct {
	Model        string
	Prompt       string
	Input        json.RawMessage
	OutputSchema map[string]any
	MaxTokens    int
	Temperature  float64
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Model string
	JSON  json.RawMessage
	Usage Usage
}

type Error struct {
	Message   string
	Transient bool
}

type Client interface {
	Generate(context.Context, Request) (Response, error)
}
```

`Scripted` stores a queue of responses/errors and records received requests for
assertions. It must be safe for concurrent test access even though v1 executes
sequentially.

- [ ] **Step 4: Implement the OpenAI-compatible client**

`OpenAI` accepts base URL, API key, and `http.Client`. POST one non-streaming
request to `/v1/chat/completions`, provide the prompt plus serialized projected
input, request strict JSON Schema response formatting, define no tools, and
parse the first choice as JSON. On non-2xx responses, return status and a
sanitized message without returning the provider body. Read configuration from:

```text
OPENAI_API_KEY
OPENAI_BASE_URL (default https://api.openai.com)
FLOWUP_AI_MODEL
```

`FLOWUP_AI_MODEL` is required only when a workflow uses the alias `default`.
Do not hardcode a model identifier in the binary.

- [ ] **Step 5: Run model tests**

Run:

```powershell
go test ./internal/model -v
```

Expected: PASS using only scripted clients and local HTTP servers.

- [ ] **Step 6: Commit**

```powershell
git add internal/model
git commit -m "feat: constrain model clients to structured generation"
```

### Task 5: Implement `ai.generate` with Explicit Projection

**Files:**
- Create: `internal/action/ai/generate.go`
- Create: `internal/action/ai/generate_test.go`
- Modify: `internal/policy/action.go`

- [ ] **Step 1: Write AI boundary tests**

```go
func TestGenerateReceivesOnlyProjectedInput(t *testing.T) {
	client := model.NewScripted(model.Response{
		Model: "small",
		JSON: json.RawMessage(`{"priority":"high"}`),
	})
	a := New(client)
	result, err := a.Execute(context.Background(), invocation(map[string]any{
		"models": []any{"small"},
		"prompt": "Classify",
		"input": map[string]any{"title": "Build failure"},
		"output_schema": map[string]any{
			"type": "object",
			"required": []any{"priority"},
			"properties": map[string]any{"priority": map[string]any{"type": "string"}},
		},
		"max_attempts": float64(1),
	}))
	require.NoError(t, err)
	require.JSONEq(t, `{"priority":"high"}`, string(result.Output))
	require.JSONEq(t, `{"title":"Build failure"}`, string(client.Requests()[0].Input))
}

func TestGenerateRetriesInvalidJSONThenUsesFallback(t *testing.T) {
	client := model.NewScripted(
		model.Response{Model: "small", JSON: json.RawMessage(`{"wrong":true}`)},
		model.Response{Model: "large", JSON: json.RawMessage(`{"priority":"low"}`)},
	)
	// Assert two deterministic requests and schema-valid final output.
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/action/ai -v
```

Expected: FAIL because the AI action does not exist.

- [ ] **Step 3: Implement deterministic attempts**

Accepted inputs are:

```text
model: optional single model alias
models: optional ordered model aliases
prompt: required string
input: required JSON-compatible projected value
output_schema: required object
max_attempts: integer 1..5, default 1
timeout_seconds: integer 1..120, default 30
max_tokens: integer 1..32768, default 2048
temperature: number 0..2, default 0
```

Reject secret references recursively in `input`. Resolve `model` to a one-item
list; reject specifying both `model` and `models`. For attempt `n`, select
`models[min(n-1, len(models)-1)]`. Each attempt makes exactly one
`Client.Generate` call. Parse and validate the response against
`output_schema`; retry only model transport errors classified transient,
malformed JSON, or schema mismatch. Persist only the final validated JSON as
the action output.

- [ ] **Step 4: Emit sanitized AI events**

Use the optional `action.Invocation.Events` sink defined in Plan 1.

For each attempt emit model alias, attempt number, duration milliseconds,
input/output token counts, and validation result. Do not emit prompt, projected
input, provider body, reasoning content, or raw invalid output.

- [ ] **Step 5: Run AI tests**

Run:

```powershell
go test ./internal/action/ai ./internal/model ./internal/policy -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/action internal/model internal/policy
git commit -m "feat: add projected structured ai generation"
```

### Task 6: Implement Durable Approval

**Files:**
- Create: `internal/action/approval/approval.go`
- Create: `internal/action/approval/approval_test.go`
- Create: `internal/store/approvals.go`
- Create: `internal/store/approvals_test.go`
- Modify: `internal/engine/engine.go`
- Create: `internal/engine/approval_test.go`

- [ ] **Step 1: Write approval action and store tests**

```go
func TestApprovalActionReturnsPause(t *testing.T) {
	result, err := New().Execute(context.Background(), invocation(map[string]any{
		"message": "Send alert",
		"preview": map[string]any{"priority": "high"},
	}))
	require.NoError(t, err)
	require.Equal(t, "approval", result.Pause.Kind)
	require.JSONEq(t, `{"priority":"high"}`, string(result.Pause.Preview))
}

func TestApprovalDecisionIsSingleAssignment(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.CreateApproval(ctx, pendingApproval))
	_, err := s.DecideApproval(ctx, pendingApproval.ID, store.ApprovalApproved, "")
	require.NoError(t, err)
	_, err = s.DecideApproval(ctx, pendingApproval.ID, store.ApprovalRejected, "changed mind")
	require.ErrorIs(t, err, store.ErrConflict)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/action/approval ./internal/store -run Approval -v
```

Expected: FAIL because approval behavior is not implemented.

- [ ] **Step 3: Implement the approval action and persistence**

The action is read-only and accepts required `message` plus optional
JSON-compatible `preview`. It returns `Result.Pause` and no normal output.

Store approval statuses are `pending`, `approved`, and `rejected`.
`CreateApproval`, `GetApproval`, and `DecideApproval` execute in transactions.
A decision is immutable and records decision time and rejection reason.

Add a transactional store method:

```go
func (s *SQLiteStore) PauseForApproval(
	ctx context.Context,
	approval ApprovalRecord,
	step StepRecord,
	run RunRecord,
	event EventRecord,
) error
```

- [ ] **Step 4: Pause the engine durably**

When an action returns a pause:

1. Create approval ID `approval_<random>`.
2. Insert the approval record.
3. Update the step to `waiting_approval`.
4. Update the run to `waiting_approval`.
5. Append an `approval.requested` event.
6. Commit all changes in one store transaction.

Expose:

```go
func (e *Engine) Approve(ctx context.Context, approvalID string) (store.RunRecord, error)
func (e *Engine) Reject(ctx context.Context, approvalID, reason string) (store.RunRecord, error)
```

Approve changes the step to `approved`, stores output `{"approved":true}`, moves
the run index, appends `approval.approved`, and continues execution. Reject
changes step/run to `rejected`, stores `{"approved":false,"reason":"..."}`, and
does not execute later steps.

Use `DecideAndUpdateRun` to persist the decision, step, run, and event in one
SQLite transaction before any resumed action executes.

- [ ] **Step 5: Run approval tests**

Run:

```powershell
go test ./internal/action/approval ./internal/store ./internal/engine -run Approval -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/action/approval internal/store internal/engine
git commit -m "feat: persist approval pauses and decisions"
```

### Task 7: Make External Effects and Recovery Explicit

**Files:**
- Create: `internal/store/effects.go`
- Create: `internal/store/effects_test.go`
- Create: `internal/engine/recovery.go`
- Create: `internal/engine/recovery_test.go`
- Modify: `internal/engine/engine.go`

- [ ] **Step 1: Write effect and recovery tests**

```go
func TestResumeDoesNotRepeatCompletedEffect(t *testing.T) {
	a := &countingEffectAction{}
	engine, runID := prepareInterruptedRunWithCompletedEffect(t, a)
	run, err := engine.Resume(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, 0, a.Calls)
	require.Equal(t, store.RunSucceeded, run.Status)
}

func TestResumeFailsIndeterminateStartedEffect(t *testing.T) {
	a := &countingEffectAction{}
	engine, runID := prepareInterruptedRunWithStartedEffect(t, a)
	run, err := engine.Resume(context.Background(), runID)
	require.ErrorContains(t, err, "effect_indeterminate")
	require.Equal(t, 0, a.Calls)
	require.Equal(t, store.RunFailed, run.Status)
}

func TestResumeRetriesInterruptedReadOnlyAction(t *testing.T) {
	a := &countingReadAction{}
	engine, runID := prepareInterruptedRun(t, a)
	_, err := engine.Resume(context.Background(), runID)
	require.NoError(t, err)
	require.Equal(t, 1, a.Calls)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/store ./internal/engine -run 'Effect|Resume' -v
```

Expected: FAIL because effect recovery is not enforced.

- [ ] **Step 3: Implement effect state**

Effect statuses are `started` and `completed`.

```go
type BeginEffectResult struct {
	Record EffectRecord
	New    bool
}

func (s *SQLiteStore) BeginEffect(ctx context.Context, record EffectRecord) (BeginEffectResult, error)
func (s *SQLiteStore) CompleteEffect(ctx context.Context, key string, output json.RawMessage) error
func (s *SQLiteStore) GetEffect(ctx context.Context, key string) (EffectRecord, error)
func (s *SQLiteStore) CompleteEffectAndStep(
	ctx context.Context,
	key string,
	output json.RawMessage,
	step StepRecord,
	run RunRecord,
	event EventRecord,
) error
```

The deterministic default key is `runID + ":" + stepID`. `BeginEffect` must be
atomic: insert and return `New=true`, or load the existing row and return
`New=false`.

External-effect action schemas may expose optional `idempotency_key`. When it
is a non-empty string, use `actionName + ":" + idempotency_key` instead of the
default key. The field is part of the validated input and is also passed in
`Invocation.IdempotencyKey`; connectors must not treat it as service content.

- [ ] **Step 4: Enforce effect execution rules**

Before an external-effect action:

1. Call `BeginEffect`.
2. If existing status is `completed`, reuse its output and do not call action.
3. If existing status is `started`, fail with `effect_indeterminate`.
4. If newly inserted, call the action once.
5. Persist effect completion and successful step output in one transaction.

On resume, retry interrupted read-only and `ai.generate` steps. Reuse succeeded,
skipped, approved, and completed-effect steps. Keep waiting approvals suspended.
Never blindly retry an interrupted external effect.

- [ ] **Step 5: Run recovery tests**

Run:

```powershell
go test ./internal/store ./internal/engine -run 'Effect|Resume|Recovery' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/store internal/engine
git commit -m "feat: recover runs without repeating effects"
```

### Task 8: Wire Trusted Actions and CLI Commands

**Files:**
- Modify: `cmd/flowup/main.go`
- Modify: `cmd/flowup/commands.go`
- Modify: `cmd/flowup/commands_test.go`

- [ ] **Step 1: Write CLI approval and resume tests**

```go
func TestApproveCommandResumesRun(t *testing.T) {
	db, runID, approvalID := createWaitingRun(t)
	out := runCommand(t, "approve", approvalID, "--db", db)
	require.Contains(t, out, "status: succeeded")
	require.Contains(t, runCommand(t, "status", runID, "--db", db), "approved")
}

func TestRejectCommandTerminatesRun(t *testing.T) {
	db, _, approvalID := createWaitingRun(t)
	out := runCommand(t, "reject", approvalID, "--reason", "not now", "--db", db)
	require.Contains(t, out, "status: rejected")
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./cmd/flowup -run 'Approve|Reject|Resume' -v
```

Expected: FAIL because commands are missing.

- [ ] **Step 3: Register actions and secret source**

The CLI registry must include:

```text
http.request
json.select
json.validate
switch
ai.generate
approval
```

Use `policy.EnvSecrets` at execution time. Validation must not read secret
values. Construct the OpenAI client lazily so workflows without `ai.generate`
do not require `OPENAI_API_KEY`.

Extend engine construction without changing `Start` or `Resume` signatures:

```go
func WithSecrets(source policy.SecretSource) Option
func WithActionPolicy(evaluator *policy.ActionPolicy) Option
func New(store store.Store, actions *action.Registry, options ...Option) *Engine
```

- [ ] **Step 4: Add commands**

Support:

```text
flowup approve <approval-id> [--db <path>]
flowup reject <approval-id> [--reason <text>] [--db <path>]
flowup resume <run-id> [--db <path>]
```

When `run` suspends, print the approval ID and exact approve/reject commands.
`approve` prints the resumed run status. `reject` prints `status: rejected`.
`resume` refuses waiting-approval runs and tells the user to approve or reject
the named approval.

- [ ] **Step 5: Run CLI and trusted-execution tests**

Run:

```powershell
go test ./internal/policy/... ./internal/model/... ./internal/action/http/... ./internal/action/ai/... ./internal/action/approval/... ./internal/store/... ./internal/engine/... ./cmd/flowup/...
```

Expected: PASS without external network or API keys.

- [ ] **Step 6: Commit**

```powershell
git add cmd/flowup
git commit -m "feat: expose approval and recovery commands"
```

### Task 9: Trusted Execution Verification

- [ ] **Step 1: Run the repository suite**

```powershell
go test ./...
go vet ./...
```

Expected: both commands exit `0`.

- [ ] **Step 2: Verify secret values cannot reach persisted state**

Run:

```powershell
go test ./... -run 'Secret|Redact|Projection' -count=1
```

Expected: PASS. Tests must inspect stored step inputs, events, CLI output, and AI
requests for absence of fixture secret values.

- [ ] **Step 3: Verify recovery behavior repeatedly**

Run:

```powershell
go test ./internal/engine ./internal/store -run 'Approval|Effect|Resume|Recovery' -count=10
```

Expected: PASS on all ten runs.
