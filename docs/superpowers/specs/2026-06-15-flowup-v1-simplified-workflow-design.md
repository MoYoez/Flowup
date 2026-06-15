# Flowup v1 Simplified Workflow Engine Design

## Summary

Flowup v1 is a small Go workflow engine inspired by the declarative execution
model of GitHub Actions, without attempting syntax compatibility.

Users manually start a workflow from the CLI. A workflow contains one ordered
list of steps. Each step invokes a registered declarative action with validated
inputs and outputs. AI is available only through a single-call structured
generation action. It cannot select tools, inspect undeclared context, change
the workflow, or decide whether to retry.

The design deliberately removes the previous general agent runtime,
distributed worker architecture, and arbitrary code execution.

## Product Boundary

Flowup v1 provides:

- A Go CLI and single-process execution engine.
- Manually invoked YAML workflows.
- One ordered list of steps per workflow.
- Declarative built-in actions and official connectors.
- Explicit step inputs and outputs.
- Conditions and deterministic value switching.
- Structured AI generation with explicit input projection.
- Durable approval pauses resumed through CLI commands.
- SQLite-backed state, event history, idempotency, and crash recovery.
- Static workflow validation before execution.

Flowup v1 does not provide:

- Autonomous agents or model-driven tool calls.
- Arbitrary shell, Git, or filesystem execution.
- User-defined code or a plugin protocol.
- Automatic triggers, cron schedules, or webhooks.
- A web UI, HTTP API, MCP server, or event streaming.
- Distributed workers, task queues, capability routing, or parallel jobs.
- GitHub Actions syntax compatibility.
- Backward compatibility with the current pipeline YAML format.

## Core Trust Model

The workflow definition and Flowup runtime are trusted.

AI model output and external service responses are untrusted. They must pass
declared output schemas before later steps can consume them.

AI receives only values explicitly projected into the `ai.generate` input.
It cannot access the workflow context, credentials, action registry, or prior
step outputs that are not referenced by that input.

Secrets are resolved only by the action that declares a secret-bearing input.
A secret reference must occupy the complete value of that input field; it
cannot be embedded into a general string. Secrets cannot be referenced by
workflow outputs, conditions, non-secret action fields, or AI inputs. They are
masked in events and diagnostic output.

All external effects are performed by registered actions. AI can produce data
that a later action consumes, but it cannot execute the action itself.

## Workflow Model

A workflow has:

- `name`: stable workflow name.
- `version`: workflow schema version.
- `inputs`: typed values supplied by the user.
- `steps`: an ordered list of action invocations.
- `outputs`: explicit values returned after successful execution.

Steps execute in declaration order. A step may read workflow inputs and outputs
from earlier completed steps. There is no job layer and no parallel execution
in v1.

### Example

```yaml
name: triage-issue
version: 1

inputs:
  issue_url:
    type: string
    required: true

steps:
  - id: issue
    uses: github.issue.get
    with:
      url: ${{ inputs.issue_url }}
      token: ${{ secrets.GITHUB_TOKEN }}

  - id: classify
    uses: ai.generate
    with:
      model: default
      prompt: Classify the issue by category and priority.
      input:
        title: ${{ steps.issue.output.title }}
        body: ${{ steps.issue.output.body }}
      output_schema:
        type: object
        required: [category, priority]
        properties:
          category:
            type: string
          priority:
            type: string
            enum: [low, medium, high]

  - id: route
    uses: switch
    with:
      value: ${{ steps.classify.output.priority }}
      cases:
        high: approval
        default: notify

  - id: approve_alert
    uses: approval
    if: ${{ steps.route.output == "approval" }}
    with:
      message: Send a high-priority issue alert.
      preview:
        category: ${{ steps.classify.output.category }}
        priority: ${{ steps.classify.output.priority }}

  - id: notify
    uses: slack.message.send
    if: ${{ steps.route.output == "notify" || steps.approve_alert.status == "approved" }}
    with:
      channel: engineering
      text: New ${{ steps.classify.output.priority }} issue
      token: ${{ secrets.SLACK_TOKEN }}

outputs:
  category: ${{ steps.classify.output.category }}
  priority: ${{ steps.classify.output.priority }}
```

## Expression Rules

Expressions use the `${{ ... }}` form but are Flowup-specific.

Allowed references:

- `inputs.<name>`
- `steps.<step-id>.output`
- `steps.<step-id>.output.<field>`
- `steps.<step-id>.status`
- `secrets.<name>`, only in action fields declared as secret-bearing

Conditions support:

- Equality and inequality.
- Boolean `&&` and `||`.
- Parentheses.
- Literal strings, numbers, booleans, and null.
- References to workflow inputs and earlier step results.

Conditions cannot call functions, evaluate user code, access environment
variables, or reference a later step.

The `switch` action performs deterministic equality matching and returns the
selected case value as its output. It does not alter the instruction pointer or
create hidden graph edges. Later steps use `if` to decide whether to execute.

## Step Statuses

Each step has one of these statuses:

- `pending`
- `running`
- `succeeded`
- `skipped`
- `waiting_approval`
- `approved`
- `rejected`
- `failed`

Run terminal statuses are:

- `succeeded`
- `failed`
- `rejected`
- `cancelled`

`skipped` satisfies ordering and permits the next step to be evaluated.

An approval step initially transitions to `waiting_approval` and suspends the
run. `flowup approve <approval-id>` changes the step to `approved` and resumes
the run. `flowup reject <approval-id>` changes it to `rejected` and terminates
the run as `rejected`.

## Action Model

Every action implements a fixed runtime interface and publishes metadata:

- Stable action name.
- Input JSON Schema.
- Output JSON Schema.
- Which input fields accept secret references.
- Whether the action is read-only or produces an external effect.
- Its fixed timeout and retry classification behavior.

The engine rejects unknown actions and validates action inputs during
`validate` and again immediately before execution.

Action outputs are validated before being persisted as successful.

### Core Actions

The v1 core action set is:

- `http.request`
- `json.select`
- `json.validate`
- `switch`
- `ai.generate`
- `approval`

`http.request` supports an explicit method, URL, headers, query, and body. Its
network policy is evaluated before the request. Response size and timeout are
bounded.

`json.select` produces a new value from explicitly selected paths.

`json.validate` validates a value against a supplied JSON Schema and returns
the validated value.

`ai.generate` makes one model request per attempt and requires an output
schema. It has no tools and cannot stream intermediate model output.

`approval` persists a human-readable request and pauses the run without
performing an external effect.

### Official Connectors

The first official connectors are GitHub and Slack.

Initial GitHub actions:

- `github.issue.get`
- `github.issue.comment`
- `github.pull_request.get`
- `github.pull_request.comment`

Initial Slack actions:

- `slack.message.get`
- `slack.message.send`

Connector actions are compiled into the Flowup binary and follow the same
schema, secret, policy, timeout, idempotency, and audit rules as core actions.
There is no v1 extension or plugin protocol.

Read actions are marked read-only. Comment and send actions are external
effects and must use an idempotency key derived from the run and step unless the
workflow explicitly supplies a stable key.

An approval step is not automatically inserted before write actions. The
workflow author must place approval explicitly. Static validation should emit a
warning when a write action has no preceding approval path, but this is not a
hard error in v1.

## AI Boundary

`ai.generate` is a constrained transformation action:

- Exactly one model completion per attempt.
- No tool definitions or tool calls.
- No access to secrets.
- No access to undeclared workflow context.
- Required JSON output schema.
- Fixed timeout.
- Predeclared maximum attempts.
- Predeclared ordered model fallback list.

If model output cannot be parsed or fails its schema, the runtime may retry up
to the declared limit. Retrying and fallback selection are deterministic engine
behavior, not model decisions.

The stored event records the model identifier, attempt, duration, token usage
when available, and validation result. It does not store provider reasoning
content.

## Policy Enforcement

Policy validation occurs before execution and again at the action boundary.

V1 policies include:

- Secret references are accepted only by declared secret-bearing fields.
- Secret references must occupy a complete declared secret-bearing field and
  are never interpolated into general strings.
- AI input rejects secret references at validation time.
- HTTP URLs must use allowed schemes.
- Optional host allowlists constrain `http.request`.
- Response bodies have fixed maximum sizes.
- Action input and output schemas are mandatory for registered actions.
- External-effect actions use persisted idempotency records.
- Events redact secret-looking fields as defense in depth.

The policy package owns these decisions. Individual actions cannot bypass it.

## Persistence and Recovery

SQLite is the only v1 store.

The store persists:

- Run identity, workflow name, version, inputs, and terminal output.
- Current step index and run status.
- Step inputs, outputs, status, attempt count, and timestamps.
- Approval requests and decisions.
- External-effect idempotency records.
- Structured execution events.

The engine writes the step state before and after action execution.

After a crash:

- Completed and skipped steps are not repeated.
- An `ai.generate` result already persisted as successful is reused.
- A waiting approval remains waiting.
- An external effect with a completed idempotency record is not repeated.
- A step left in `running` is recovered according to its action classification.

Read-only actions and AI generation may be retried after an interrupted
`running` state. External-effect actions require an idempotency record and
connector-specific reconciliation before retry. If safe reconciliation is not
available, the run fails with an explicit indeterminate-effect error instead of
blindly repeating the action.

## CLI

The v1 command surface is:

```text
flowup validate <workflow.yaml>
flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]
flowup status <run-id> [--db <path>]
flowup approve <approval-id> [--db <path>]
flowup reject <approval-id> [--reason <text>] [--db <path>]
flowup resume <run-id> [--db <path>]
flowup trace <run-id> [--db <path>]
```

`validate` performs schema, reference, condition, action, secret-placement, and
write-without-approval warning checks.

`run` validates before creating a run. It prints the run ID and final status.
When suspended, it prints the approval ID and the exact approve/reject commands.

`status` prints the current run and step statuses.

`approve` and `reject` persist the decision. `approve` resumes execution in the
same command after recording the approval.

`resume` recovers a nonterminal run that is not waiting for an approval.

`trace` prints structured events with secrets redacted.

## Error Model

CLI errors distinguish:

- Invalid workflow definitions.
- Invalid user inputs.
- Policy violations.
- Action input or output schema violations.
- Transient external failures.
- Permanent external failures.
- AI output violations.
- Approval rejection.
- Indeterminate external effects.
- Internal persistence errors.

Errors contain a stable code and a human-readable message. Provider response
bodies, credentials, and raw internal stack traces are not printed by default.

The previous `ok`, `failed`, and `needs_human` public contract is removed. CLI
commands use run and step statuses directly.

## Package Structure

The target package layout is:

```text
cmd/
  flowup/
internal/
  workflow/              # YAML model, expressions, validation
  engine/                # ordered execution, pause, resume, recovery
  action/                # action interface, metadata, registry
    http/
    json/
    ai/
    approval/
  connector/
    github/
    slack/
  policy/                # secret, AI projection, network, effect rules
  store/                 # SQLite runs, steps, approvals, events, idempotency
  model/                 # OpenAI-compatible structured completion
```

Packages should expose narrow interfaces. The engine depends on the action
registry, policy evaluator, store, and workflow model. Actions do not depend on
the engine.

## Destructive Cleanup

The refactor intentionally removes:

- `cmd/engine`
- `cmd/worker`
- `internal/agent`
- `internal/dispatch`
- `internal/durable` as a replaceable substrate abstraction
- `internal/mcp`
- `internal/runner`
- `internal/sandbox`
- `internal/tools`
- `internal/ratelimit` unless a concrete v1 action uses it
- OpenTelemetry build-tag infrastructure unless retained by the simplified
  event model
- The Anthropic client
- Model streaming support and token events
- Postgres and DBOS comparison code
- Distributed queue and worker tests
- Agent tool-loop tests
- Old pipeline examples and generated binaries

The existing SQLite implementation, JSON Schema utilities, template concepts,
redaction logic, and applicable crash/idempotency tests may be adapted rather
than rewritten.

No compatibility loader or migration command will accept the old
`pipeline/kind/profile/tools/needs` format.

## Documentation Cleanup

The root README will be rewritten around the manual CLI workflow model.

The following documents are obsolete and will be removed after their still
relevant decisions are captured:

- `docs/agent-pipeline-tech-research.md`
- `docs/agent-pipeline-infra-build-plan.md`

`docs/decisions.md` will be rewritten as a short record of v1 decisions:

- Manual CLI execution.
- Single ordered step list.
- Declarative registered actions only.
- Explicit AI input projection.
- Explicit durable approval.
- SQLite persistence.
- No backward compatibility.

## Testing Strategy

Tests are organized around externally visible guarantees:

- Workflow parsing and static reference validation.
- Condition and switch evaluation.
- Secret placement rejection.
- AI input projection isolation.
- Action input and output schema validation.
- Sequential execution and skipped steps.
- Approval pause, approve, reject, and resume.
- Crash recovery without repeating completed AI calls.
- Idempotent GitHub and Slack write actions.
- Indeterminate external-effect recovery behavior.
- CLI command output and exit codes.

Connector tests use local HTTP test servers. AI tests use a scripted model
client. The default test suite requires no external services or API keys.

## Acceptance Criteria

The v1 redesign is complete when:

1. The old pipeline format and agent runtime no longer compile or appear in
   product documentation.
2. A user can validate and manually run a sequential workflow.
3. AI sees only explicitly projected input and returns schema-valid JSON.
4. An approval step survives process exit and resumes through the CLI.
5. A successful completed step is not repeated after process recovery.
6. GitHub and Slack actions execute through typed connectors without exposing
   their credentials to AI or logs.
7. The default test suite passes without Docker, Postgres, or network access.
