# Flowup v1 Core Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a manually invoked, sequential workflow engine with static validation, deterministic core actions, SQLite state, and the `validate`, `run`, `status`, and `trace` CLI commands.

**Architecture:** Move the tracked prototype under Go-ignored `_legacy/`, then create the new `workflow`, `action`, `store`, and `engine` contracts at their final package paths. Persist the workflow YAML with each run so later plans can resume it without requiring the original file.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3`, `github.com/santhosh-tekuri/jsonschema/v6`, `modernc.org/sqlite`

---

## File Map

| Path | Responsibility |
| --- | --- |
| `internal/workflow/types.go` | YAML workflow, inputs, steps, and outputs |
| `internal/workflow/parse.go` | strict YAML parsing and input validation |
| `internal/workflow/expression.go` | `${{ }}` references and condition evaluation |
| `internal/workflow/validate.go` | static ordering, action, schema, and reference checks |
| `internal/action/action.go` | action metadata and execution contracts |
| `internal/action/registry.go` | immutable name-to-action registry |
| `internal/action/json/select.go` | deterministic JSON projection |
| `internal/action/json/validate.go` | runtime JSON Schema validation |
| `internal/action/switch/switch.go` | deterministic equality switch |
| `internal/store/types.go` | v1 run, step, event, approval, and effect records |
| `internal/store/sqlite.go` | SQLite opening and migrations |
| `internal/store/runs.go` | run and step persistence |
| `internal/store/events.go` | append-only event persistence |
| `internal/engine/engine.go` | ordered execution and output construction |
| `internal/engine/errors.go` | stable coded errors |
| `cmd/flowup/main.go` | command dispatch and exit codes |
| `cmd/flowup/commands.go` | validate/run/status/trace handlers |
| `examples/v1/local-triage.yaml` | offline runnable workflow |

### Task 0: Quarantine the Tracked Prototype

**Files:**
- Move: `cmd/` to `_legacy/cmd/`
- Move: `internal/` to `_legacy/internal/`

- [ ] **Step 1: Confirm the baseline commit exists**

Run:

```powershell
git log -1 --oneline
git status --short
```

Expected: the most recent commit is
`chore: capture pre-v1 implementation baseline`; the working tree has no
implementation changes.

- [ ] **Step 2: Move the prototype out of the Go package graph**

Run:

```powershell
New-Item -ItemType Directory -Path _legacy -Force | Out-Null
git mv cmd _legacy/cmd
git mv internal _legacy/internal
```

Directories beginning with `_` are ignored by `go list`, allowing the v1
packages to be built at the final paths without temporary compatibility
interfaces. Reuse old implementations by reading them from `_legacy`, not by
importing them.

- [ ] **Step 3: Verify the active package graph is empty**

Run:

```powershell
go list ./...
```

Expected: warning that `./...` matched no packages.

- [ ] **Step 4: Commit**

```powershell
git add -A
git commit -m "refactor: quarantine pre-v1 prototype"
```

### Task 1: Define and Strictly Parse the v1 Workflow Format

**Files:**
- Create: `internal/workflow/types.go`
- Create: `internal/workflow/parse.go`
- Create: `internal/workflow/parse_test.go`

- [ ] **Step 1: Write parsing tests**

Create tests covering a valid workflow, duplicate step IDs, unknown YAML fields,
unsupported versions, and malformed input declarations:

```go
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
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/workflow -run TestParse -v
```

Expected: FAIL because `Parse` and workflow types do not exist.

- [ ] **Step 3: Add workflow types**

Implement:

```go
type Workflow struct {
	Name    string               `yaml:"name"`
	Version int                  `yaml:"version"`
	Inputs  map[string]InputSpec `yaml:"inputs,omitempty"`
	Steps   []Step               `yaml:"steps"`
	Outputs map[string]any       `yaml:"outputs,omitempty"`
}

type InputSpec struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required,omitempty"`
}

type Step struct {
	ID   string         `yaml:"id"`
	Uses string         `yaml:"uses"`
	If   string         `yaml:"if,omitempty"`
	With map[string]any `yaml:"with,omitempty"`
}
```

`Parse` must use `yaml.Decoder.KnownFields(true)`, require non-empty name,
version `1`, at least one step, unique IDs matching
`^[A-Za-z_][A-Za-z0-9_-]*$`, and input types from
`string`, `number`, `integer`, `boolean`, `object`, and `array`.

- [ ] **Step 4: Run parsing tests**

Run:

```powershell
go test ./internal/workflow -run TestParse -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/workflow/types.go internal/workflow/parse.go internal/workflow/parse_test.go
git commit -m "feat: parse v1 workflow definitions"
```

### Task 2: Resolve References and Evaluate Conditions

**Files:**
- Create: `internal/workflow/expression.go`
- Create: `internal/workflow/expression_test.go`

- [ ] **Step 1: Write reference resolution tests**

Use this public contract:

```go
type StepValue struct {
	Status string
	Output any
}

type Context struct {
	Inputs map[string]any
	Steps  map[string]StepValue
}

func Resolve(value any, context Context) (any, error)
func EvaluateCondition(source string, context Context) (bool, error)
func References(value any) ([]Reference, error)
```

Test complete-value references preserve JSON types, interpolation produces a
string, object/list recursion works, missing values fail, and later-step
references can be discovered:

```go
func TestResolvePreservesCompleteReferenceType(t *testing.T) {
	got, err := Resolve("${{ steps.fetch.output }}", Context{
		Steps: map[string]StepValue{"fetch": {Status: "succeeded", Output: map[string]any{"count": float64(2)}}},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"count": float64(2)}, got)
}

func TestEvaluateCondition(t *testing.T) {
	ok, err := EvaluateCondition(
		`${{ steps.route.output == "notify" || steps.approval.status == "approved" }}`,
		Context{Steps: map[string]StepValue{
			"route": {Output: "notify"},
			"approval": {Status: "skipped"},
		}},
	)
	require.NoError(t, err)
	require.True(t, ok)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/workflow -run 'TestResolve|TestEvaluateCondition' -v
```

Expected: FAIL because expression APIs do not exist.

- [ ] **Step 3: Implement the expression grammar**

Support only:

```text
condition := or
or        := and ("||" and)*
and       := equality ("&&" equality)*
equality  := primary (("==" | "!=") primary)?
primary   := reference | string | number | "true" | "false" | "null" | "(" condition ")"
reference := inputs.NAME | steps.ID.output(.FIELD)* | steps.ID.status
```

Use `reflect.DeepEqual` for equality after YAML values have been normalized
through JSON marshal/unmarshal. Reject function syntax, environment access,
index expressions, and any identifier outside `inputs` or `steps`.

- [ ] **Step 4: Run expression tests**

Run:

```powershell
go test ./internal/workflow -run 'TestResolve|TestEvaluateCondition' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/workflow/expression.go internal/workflow/expression_test.go
git commit -m "feat: evaluate workflow expressions"
```

### Task 3: Add the Action Contract and Deterministic Registry

**Files:**
- Create: `internal/action/action.go`
- Create: `internal/action/registry.go`
- Create: `internal/action/registry_test.go`

- [ ] **Step 1: Write registry tests**

```go
func TestRegistryRejectsDuplicateNames(t *testing.T) {
	a := fakeAction{name: "switch"}
	_, err := NewRegistry(a, a)
	require.ErrorContains(t, err, `duplicate action "switch"`)
}

func TestRegistryRequiresSchemas(t *testing.T) {
	_, err := NewRegistry(fakeAction{name: "broken", inputSchema: nil})
	require.ErrorContains(t, err, "input schema")
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/action -v
```

Expected: FAIL because the action package does not exist.

- [ ] **Step 3: Implement the contracts**

Use these types consistently in all later plans:

```go
type EffectClass string

const (
	EffectReadOnly EffectClass = "read_only"
	EffectExternal EffectClass = "external"
)

type Definition struct {
	Name         string
	InputSchema  map[string]any
	OutputSchema map[string]any
	SecretPaths  []string
	Timeout      time.Duration
}

type Invocation struct {
	RunID          string
	StepID         string
	Attempt        int
	Input          map[string]any
	IdempotencyKey string
	Events         EventSink
}

type Result struct {
	Output json.RawMessage
	Pause  *Pause
}

type Pause struct {
	Kind    string
	Message string
	Preview json.RawMessage
}

type Event struct {
	Type string
	Data map[string]any
}

type EventSink interface {
	Emit(context.Context, Event) error
}

type Action interface {
	Definition() Definition
	Effect(input map[string]any) EffectClass
	Execute(context.Context, Invocation) (Result, error)
}
```

`NewRegistry(actions ...Action)` must reject duplicate/empty names, nil schemas,
and non-positive timeouts. `Get(name)` returns `(Action, bool)`, and `Names()`
returns a sorted copy.

- [ ] **Step 4: Run registry tests**

Run:

```powershell
go test ./internal/action -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/action
git commit -m "feat: define registered action contract"
```

### Task 4: Implement `json.select`, `json.validate`, and `switch`

**Files:**
- Create: `internal/action/json/select.go`
- Create: `internal/action/json/select_test.go`
- Create: `internal/action/json/validate.go`
- Create: `internal/action/json/validate_test.go`
- Create: `internal/action/switch/switch.go`
- Create: `internal/action/switch/switch_test.go`

- [ ] **Step 1: Write action behavior tests**

Test:

```go
func TestSelectProjectsNamedPaths(t *testing.T) {
	got, err := runSelect(map[string]any{
		"value": map[string]any{"issue": map[string]any{"title": "Broken", "priority": "high"}},
		"paths": map[string]any{"title": "issue.title"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"title":"Broken"}`, string(got))
}

func TestSwitchUsesDefault(t *testing.T) {
	got, err := runSwitch(map[string]any{
		"value": "unknown",
		"cases": map[string]any{"high": "approval", "default": "notify"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `"notify"`, string(got))
}

func TestValidateRejectsSchemaMismatch(t *testing.T) {
	_, err := runValidate(map[string]any{
		"value": 3,
		"schema": map[string]any{"type": "string"},
	})
	require.ErrorContains(t, err, "does not validate")
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/action/json ./internal/action/switch -v
```

Expected: FAIL because actions do not exist.

- [ ] **Step 3: Implement minimal deterministic actions**

`json.select` accepts `value` and an object of output-name to dot-separated
object paths. It must reject missing paths and array indexing in v1.

`json.validate` accepts `value` and a Draft 2020-12-compatible schema, compiles
the schema using the existing JSON Schema dependency, validates the normalized
JSON value, and returns the unchanged value.

`switch` accepts scalar `value` and string-keyed `cases`. Compare case keys to
the canonical scalar text; use `default` only when no exact match exists; return
the selected case value as JSON.

All three definitions use a 5-second timeout, no secret paths, read-only effect
classification, and explicit input/output schemas.

- [ ] **Step 4: Run action tests**

Run:

```powershell
go test ./internal/action/json ./internal/action/switch -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/action/json internal/action/switch
git commit -m "feat: add deterministic core actions"
```

### Task 5: Add Static Workflow Validation

**Files:**
- Create: `internal/workflow/validate.go`
- Create: `internal/workflow/validate_test.go`

- [ ] **Step 1: Write validation tests**

```go
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
	err := Validate(wf, registryForTests())
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
	err := Validate(wf, registryForTests())
	require.ErrorContains(t, err, `unknown action "missing.action"`)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/workflow -run TestValidate -v
```

Expected: FAIL because `Validate` does not exist.

- [ ] **Step 3: Implement validation**

Expose:

```go
type Warning struct {
	Code    string
	StepID  string
	Message string
}

type ActionCatalog interface {
	Get(string) (action.Action, bool)
}

func Validate(wf Workflow, actions ActionCatalog) ([]Warning, error)
func ValidateInputs(specs map[string]InputSpec, values map[string]any) error
```

Validation must reject unknown actions; invalid/missing inputs; duplicate IDs;
later-step references; invalid conditions; workflow outputs referencing
nonexistent steps; and action `with` values that cannot satisfy the action
input schema after references are replaced with type-neutral sentinel values.
Return warnings separately from hard errors. Secret and write-action warnings
are added in later plans.

- [ ] **Step 4: Run workflow tests**

Run:

```powershell
go test ./internal/workflow -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/workflow/validate.go internal/workflow/validate_test.go
git commit -m "feat: statically validate workflows"
```

### Task 6: Replace the SQLite Store with v1 Run State

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/sqlite.go`
- Create: `internal/store/types.go`
- Create: `internal/store/runs.go`
- Create: `internal/store/events.go`
- Replace: `internal/store/store_test.go`

- [ ] **Step 1: Write store lifecycle tests**

Test creating a run, recording step transitions, appending ordered events, and
reopening the database:

```go
func TestSQLitePersistsRunAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flowup.db")
	s := openTestStore(t, path)
	run := RunRecord{
		ID: "run-1", WorkflowName: "local", WorkflowVersion: 1,
		WorkflowYAML: []byte("name: local\nversion: 1\nsteps: []\n"),
		Inputs: json.RawMessage(`{"payload":{"x":1}}`),
		Status: RunRunning,
	}
	require.NoError(t, s.CreateRun(context.Background(), run))
	require.NoError(t, s.Close())

	s = openTestStore(t, path)
	got, err := s.GetRun(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, run.WorkflowName, got.WorkflowName)
	require.JSONEq(t, string(run.Inputs), string(got.Inputs))
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/store -v
```

Expected: FAIL because the old store contract does not provide v1 records.

- [ ] **Step 3: Define v1 records and store interface**

Use:

```go
type RunStatus string
const (
	RunRunning RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunSucceeded RunStatus = "succeeded"
	RunFailed RunStatus = "failed"
	RunRejected RunStatus = "rejected"
	RunCancelled RunStatus = "cancelled"
)

type StepStatus string
const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepSkipped StepStatus = "skipped"
	StepWaitingApproval StepStatus = "waiting_approval"
	StepApproved StepStatus = "approved"
	StepRejected StepStatus = "rejected"
	StepFailed StepStatus = "failed"
)
```

`RunRecord` includes ID, workflow name/version, original workflow YAML, JSON
inputs/output, current step index, status, coded error, and timestamps.
`StepRecord` includes run ID, step ID/index, action, redacted JSON input, JSON
output, status, attempt, coded error, and timestamps. `EventRecord` includes a
monotonic integer ID, run/step IDs, type, redacted JSON data, and timestamp.

The v1 `Store` interface provides only run, step, event, approval, and effect
methods. Implement CRUD for all five record groups now. Plan 2 adds the
transactional transition methods that combine approval/effect updates with run
and step updates.

- [ ] **Step 4: Implement SQLite migrations and CRUD**

Create only these tables:

```sql
runs(id, workflow_name, workflow_version, workflow_yaml, inputs_json,
     output_json, current_step, status, error_code, error_message,
     created_at, updated_at)
steps(run_id, step_id, step_index, action_name, input_json, output_json,
      status, attempt, error_code, error_message, started_at, finished_at,
      PRIMARY KEY(run_id, step_id))
events(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id, step_id, type,
       data_json, created_at)
approvals(id, run_id, step_id, message, preview_json, status, reason,
          created_at, decided_at)
effects(key PRIMARY KEY, run_id, step_id, action_name, status,
        output_json, created_at, completed_at)
```

Keep WAL mode, busy timeout, and one open SQLite connection from the existing
implementation. Remove Postgres dialect branches from the files replaced by
this task; the dependency cleanup occurs in Plan 3.

- [ ] **Step 5: Run store tests**

Run:

```powershell
go test ./internal/store -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/store
git commit -m "feat: persist v1 workflow runs in sqlite"
```

### Task 7: Implement Ordered Execution

**Files:**
- Create: `internal/engine/errors.go`
- Create: `internal/engine/engine.go`
- Create: `internal/engine/engine_test.go`

- [ ] **Step 1: Write sequential execution tests**

Use fake actions that append their step ID to a slice. Test declaration order,
condition-based skipping, persisted output reuse, output schema rejection, and
workflow output construction:

```go
func TestEngineRunsStepsInDeclarationOrder(t *testing.T) {
	var order []string
	registry := registryWith(recordingAction{order: &order})
	engine := New(testStore(t), registry)
	run, err := engine.Start(context.Background(), workflowYAML, map[string]any{"payload": map[string]any{"x": 1}})
	require.NoError(t, err)
	require.Equal(t, store.RunSucceeded, run.Status)
	require.Equal(t, []string{"first", "second"}, order)
}

func TestEngineSkipsFalseCondition(t *testing.T) {
	run, err := testEngine(t).Start(context.Background(), conditionalWorkflow, nil)
	require.NoError(t, err)
	steps := loadSteps(t, run.ID)
	require.Equal(t, store.StepSkipped, steps[1].Status)
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./internal/engine -v
```

Expected: FAIL because the old engine uses the removed pipeline/dispatcher
contracts.

- [ ] **Step 3: Implement stable coded errors**

```go
type Error struct {
	Code      string
	Message   string
	Retriable bool
	Cause     error
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Cause }
```

Define codes for invalid workflow, invalid inputs, policy violation, action
input, action output, transient action, permanent action, approval rejected,
indeterminate effect, and persistence.

- [ ] **Step 4: Implement `Engine.Start` and the execution loop**

Use:

```go
type Engine struct {
	store   store.Store
	actions *action.Registry
	now     func() time.Time
	newID   func(prefix string) string
}

func (e *Engine) Start(ctx context.Context, source []byte, inputs map[string]any) (store.RunRecord, error)
func (e *Engine) Resume(ctx context.Context, runID string) (store.RunRecord, error)
```

`Start` parses and validates before creating a run, stores the original YAML,
then executes steps by index. For each step:

1. Rebuild expression context from persisted earlier steps.
2. Evaluate `if`; persist `skipped` when false.
3. Resolve `with`; persist the normalized input and `running` before execution.
4. Apply the action timeout.
5. Validate action output against its declared schema.
6. Persist output and `succeeded` before moving the run index.
7. On error, persist the coded step and run failure.

`Resume` reparses the stored YAML and continues at `current_step`. In this plan,
it may retry a step left `running`; Plan 2 replaces that behavior with
effect-aware recovery.

- [ ] **Step 5: Run engine tests**

Run:

```powershell
go test ./internal/engine -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/engine
git commit -m "feat: execute workflows sequentially"
```

### Task 8: Replace the CLI with Manual v1 Commands

**Files:**
- Create: `cmd/flowup/main.go`
- Create: `cmd/flowup/commands.go`
- Create: `cmd/flowup/commands_test.go`
- Create: `examples/v1/local-triage.yaml`

- [ ] **Step 1: Write CLI tests**

Call `runCLI(ctx, stdout, stderr, args)` directly:

```go
func TestValidateCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), &stdout, &stderr, []string{"validate", fixturePath(t, "valid.yaml")})
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "workflow valid")
}

func TestRunAndStatusCommands(t *testing.T) {
	db := filepath.Join(t.TempDir(), "flowup.db")
	runOut := runCommand(t, "run", fixturePath(t, "valid.yaml"), "--inputs", fixturePath(t, "inputs.json"), "--db", db)
	runID := extractField(t, runOut, "run_id")
	statusOut := runCommand(t, "status", runID, "--db", db)
	require.Contains(t, statusOut, "status: succeeded")
}
```

- [ ] **Step 2: Run tests and confirm failure**

Run:

```powershell
go test ./cmd/flowup -v
```

Expected: FAIL because the existing CLI exposes the old command surface.

- [ ] **Step 3: Implement command parsing**

Support exactly:

```text
flowup validate <workflow.yaml>
flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]
flowup status <run-id> [--db <path>]
flowup trace <run-id> [--db <path>]
```

Use `flag.NewFlagSet` per command. Default database path is `.flowup/flowup.db`.
`validate` prints warnings followed by `workflow valid`. `run` prints
`run_id: <id>` and `status: <status>`. `status` prints the run followed by steps
in index order. `trace` prints one redacted JSON event per line. Return exit code
`2` for usage/validation errors and `1` for runtime/persistence failures.

The command wiring registers `json.select`, `json.validate`, and `switch`.

- [ ] **Step 4: Add an offline workflow example**

Create `examples/v1/local-triage.yaml` using only inputs, `json.select`,
`switch`, conditions, and outputs. It must run with:

```json
{"payload":{"title":"Build failure","priority":"high"}}
```

and return title plus selected route without network or AI access.

- [ ] **Step 5: Run CLI tests and smoke test**

Run:

```powershell
go test ./cmd/flowup -v
go run ./cmd/flowup validate examples/v1/local-triage.yaml
$inputs = Join-Path $env:TEMP 'flowup-inputs.json'
Set-Content -Path $inputs -Value '{"payload":{"title":"Build failure","priority":"high"}}'
go run ./cmd/flowup run examples/v1/local-triage.yaml --inputs $inputs --db (Join-Path $env:TEMP 'flowup-core.db')
```

Expected: tests pass; validation prints `workflow valid`; the run prints a run
ID and `status: succeeded`.

- [ ] **Step 6: Commit**

```powershell
git add cmd/flowup examples/v1/local-triage.yaml
git commit -m "feat: expose manual workflow cli"
```

### Task 9: Core Plan Verification

- [ ] **Step 1: Run focused and repository tests**

```powershell
go test ./internal/workflow/... ./internal/action/... ./internal/store/... ./internal/engine/... ./cmd/flowup/...
go test ./...
go vet ./...
```

Expected: all commands exit `0`. The prototype must not appear because it is
quarantined under `_legacy/`.

- [ ] **Step 2: Inspect status**

```powershell
git status --short
git log --oneline -10
```

Expected: no unintended generated binaries or database files are tracked; the
commits in this plan are visible in task order.
