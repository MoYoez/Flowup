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

## 2026-06-15: Block private networks by default

Decision: `http.request` rejects connections to loopback, private, and
link-local addresses unless `FLOWUP_ALLOW_PRIVATE_NETWORK` is set.

Reason: untrusted data (AI output, external responses) can flow into a request
URL, so the metadata endpoint and internal services are an SSRF target.

Consequence: the check runs at dial time against the resolved IP, which also
defeats DNS rebinding; local testing must opt in explicitly.

## 2026-06-15: Retry only read-only transients

Decision: the engine retries transient failures for read-only actions with
backoff, but never retries external effects.

Reason: a flaky network should not fail a whole run, yet retrying a write could
duplicate a side effect.

Consequence: actions tag retriable errors as transient; writes still rely on
manual resume guarded by effect records.

## 2026-06-15: Validate before recording effects

Decision: external actions may declare a prepare step that the engine runs
before it records the durable effect.

Reason: an input that could never reach the network (a rejected URL) should not
leave a started effect that recovery would treat as indeterminate.

Consequence: pre-send validation failures fail cleanly as `action_input` and
leave no effect record.
