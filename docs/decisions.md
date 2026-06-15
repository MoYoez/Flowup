# Flowup v1 Decisions

## 2026-06-15: Manual CLI execution

Decision: workflows start only through explicit CLI commands.

Reason: the first release should prove deterministic execution and recovery
before adding trigger infrastructure.

Consequence: v1 has no scheduler, webhook receiver, daemon, or HTTP API.

## 2026-06-15: Single ordered step list

Decision: a workflow contains one sequential list of steps.

Reason: jobs, DAG scheduling, and parallelism add failure modes without helping
the initial fixed workflows.

Consequence: a step may reference only inputs and earlier steps.

## 2026-06-15: Registered declarative actions only

Decision: every operation is a compiled-in action with schemas and effect
metadata.

Reason: arbitrary shell, tools, and plugins would bypass the trust boundary.

Consequence: new capabilities require a reviewed action implementation.

## 2026-06-15: Explicit AI projection

Decision: `ai.generate` receives only its declared `input`.

Reason: model access to ambient workflow context would be difficult to audit.

Consequence: AI has no tools, credentials, undeclared outputs, or retry control.

## 2026-06-15: Whole-field secret references

Decision: secret references occupy complete declared secret fields.

Reason: interpolation makes reliable placement validation and redaction
impossible.

Consequence: secrets cannot appear in conditions, outputs, normal fields, or AI
input.

## 2026-06-15: Explicit durable approval

Decision: workflow authors place `approval` steps themselves.

Reason: approval is a business decision and must remain visible in the workflow.

Consequence: write actions without an earlier approval produce a warning rather
than an automatically inserted step.

## 2026-06-15: SQLite-only persistence

Decision: SQLite is the sole v1 store.

Reason: a single-process manual CLI does not need a replaceable distributed
storage layer.

Consequence: Postgres, queues, outbox workers, and DBOS code are removed.

## 2026-06-15: No old-format compatibility

Decision: the old pipeline YAML is rejected.

Reason: compatibility would preserve the removed agent and DAG concepts.

Consequence: existing pipeline files must be rewritten as v1 workflows.

## 2026-06-15: Indeterminate effects are not repeated

Decision: an interrupted external effect without safe reconciliation fails as
`effect_indeterminate`.

Reason: duplicating comments or messages is worse than requiring manual review.

Consequence: completed effect records are reused; started records are never
blindly retried.
