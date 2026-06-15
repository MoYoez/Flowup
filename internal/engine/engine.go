package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/policy"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/workflow"
)

type Option func(*Engine)

type Engine struct {
	store       store.Store
	actions     *action.Registry
	now         func() time.Time
	newID       func(prefix string) string
	secrets     policy.SecretSource
	maxAttempts int
	backoff     func(attempt int) time.Duration
}

func New(state store.Store, actions *action.Registry, options ...Option) *Engine {
	engine := &Engine{
		store:       state,
		actions:     actions,
		now:         func() time.Time { return time.Now().UTC() },
		newID:       randomID,
		secrets:     policy.EnvSecrets{},
		maxAttempts: 3,
		backoff:     func(attempt int) time.Duration { return time.Duration(attempt) * 200 * time.Millisecond },
	}
	for _, option := range options {
		option(engine)
	}
	return engine
}

func WithSecrets(source policy.SecretSource) Option {
	return func(engine *Engine) {
		if source != nil {
			engine.secrets = source
		}
	}
}

// WithRetry bounds how many times a transient failure from a read-only action is
// retried, and how long to wait between attempts. External effects are never
// retried automatically. A maxAttempts of 1 disables retries.
func WithRetry(maxAttempts int, backoff func(attempt int) time.Duration) Option {
	return func(engine *Engine) {
		if maxAttempts > 0 {
			engine.maxAttempts = maxAttempts
		}
		if backoff != nil {
			engine.backoff = backoff
		}
	}
}

func withClock(now func() time.Time) Option {
	return func(engine *Engine) {
		engine.now = now
	}
}

func withIDGenerator(generator func(string) string) Option {
	return func(engine *Engine) {
		engine.newID = generator
	}
}

func (e *Engine) Start(ctx context.Context, source []byte, inputs map[string]any) (store.RunRecord, error) {
	wf, err := workflow.Parse(source)
	if err != nil {
		return store.RunRecord{}, coded(CodeInvalidWorkflow, err.Error(), err)
	}
	if _, err := workflow.Validate(wf, e.actions); err != nil {
		return store.RunRecord{}, coded(CodeInvalidWorkflow, err.Error(), err)
	}
	normalizedInputs, err := normalizeInputs(inputs)
	if err != nil {
		return store.RunRecord{}, coded(CodeInvalidInputs, err.Error(), err)
	}
	if err := workflow.ValidateInputs(wf.Inputs, normalizedInputs); err != nil {
		return store.RunRecord{}, coded(CodeInvalidInputs, err.Error(), err)
	}
	rawInputs, err := json.Marshal(normalizedInputs)
	if err != nil {
		return store.RunRecord{}, coded(CodeInvalidInputs, "encode workflow inputs", err)
	}
	now := e.now()
	run := store.RunRecord{
		ID:              e.newID("run"),
		WorkflowName:    wf.Name,
		WorkflowVersion: wf.Version,
		WorkflowYAML:    append([]byte(nil), source...),
		Inputs:          rawInputs,
		Status:          store.RunRunning,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := e.store.CreateRun(ctx, run); err != nil {
		return store.RunRecord{}, coded(CodePersistence, "create run", err)
	}
	if err := e.appendEvent(ctx, run.ID, "", "run.started", map[string]any{
		"workflow": wf.Name,
		"version":  wf.Version,
	}); err != nil {
		return run, e.failRun(ctx, &run, CodePersistence, "append run start event", err)
	}
	return e.execute(ctx, wf, run, normalizedInputs)
}

func (e *Engine) Resume(ctx context.Context, runID string) (store.RunRecord, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return store.RunRecord{}, coded(CodePersistence, "load run", err)
	}
	switch run.Status {
	case store.RunSucceeded, store.RunFailed, store.RunRejected, store.RunCancelled:
		return run, nil
	case store.RunWaitingApproval:
		return run, coded(CodeActionPermanent, "run is waiting for approval", nil)
	}
	wf, err := workflow.Parse(run.WorkflowYAML)
	if err != nil {
		return run, e.failRun(ctx, &run, CodeInvalidWorkflow, "parse stored workflow", err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(run.Inputs, &inputs); err != nil {
		return run, e.failRun(ctx, &run, CodePersistence, "decode stored inputs", err)
	}
	return e.execute(ctx, wf, run, inputs)
}

func (e *Engine) execute(
	ctx context.Context,
	wf workflow.Workflow,
	run store.RunRecord,
	inputs map[string]any,
) (store.RunRecord, error) {
	for index := run.CurrentStep; index < len(wf.Steps); index++ {
		definition := wf.Steps[index]
		expressionContext, err := e.expressionContext(ctx, run.ID, inputs)
		if err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "load step context", err)
		}
		if definition.If != "" {
			enabled, err := workflow.EvaluateCondition(definition.If, expressionContext)
			if err != nil {
				return run, e.failRun(ctx, &run, CodeInvalidWorkflow, "evaluate step condition", err)
			}
			if !enabled {
				step := store.StepRecord{
					RunID: run.ID, StepID: definition.ID, StepIndex: index,
					ActionName: definition.Uses, Status: store.StepSkipped,
					FinishedAt: e.now(),
				}
				if err := e.store.PutStep(ctx, step); err != nil {
					return run, e.failRun(ctx, &run, CodePersistence, "persist skipped step", err)
				}
				if err := e.appendEvent(ctx, run.ID, definition.ID, "step.skipped", nil); err != nil {
					return run, e.failRun(ctx, &run, CodePersistence, "append skipped event", err)
				}
				run.CurrentStep = index + 1
				run.UpdatedAt = e.now()
				if err := e.store.UpdateRun(ctx, run); err != nil {
					return run, coded(CodePersistence, "advance skipped step", err)
				}
				continue
			}
		}

		resolved, err := workflow.Resolve(definition.With, expressionContext)
		if err != nil {
			return run, e.failRun(ctx, &run, CodeActionInput, "resolve action input", err)
		}
		opaqueInput, ok := resolved.(map[string]any)
		if !ok {
			return run, e.failRun(ctx, &run, CodeActionInput, "action input must be an object", nil)
		}
		candidate, ok := e.actions.Get(definition.Uses)
		if !ok {
			return run, e.failRun(ctx, &run, CodeInvalidWorkflow, "unknown action "+definition.Uses, nil)
		}
		actionDefinition := candidate.Definition()
		input, redactedInput, err := policy.ResolveSecrets(
			opaqueInput,
			actionDefinition.SecretPaths,
			e.secrets,
		)
		if err != nil {
			return run, e.failRun(ctx, &run, CodePolicyViolation, err.Error(), err)
		}
		if err := action.ValidateSchema(actionDefinition.InputSchema, input); err != nil {
			return run, e.failRun(ctx, &run, CodeActionInput, err.Error(), err)
		}
		rawInput, err := json.Marshal(redactedInput)
		if err != nil {
			return run, e.failRun(ctx, &run, CodeActionInput, "encode action input", err)
		}
		attempt := 1
		if previous, err := e.store.GetStep(ctx, run.ID, definition.ID); err == nil {
			attempt = previous.Attempt + 1
		} else if err != store.ErrNotFound {
			return run, e.failRun(ctx, &run, CodePersistence, "load previous step", err)
		}
		step := store.StepRecord{
			RunID: run.ID, StepID: definition.ID, StepIndex: index,
			ActionName: definition.Uses, Input: rawInput, Status: store.StepRunning,
			Attempt: attempt, StartedAt: e.now(),
		}
		if err := e.store.PutStep(ctx, step); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "persist running step", err)
		}
		if err := e.appendEvent(ctx, run.ID, definition.ID, "step.started", map[string]any{
			"action":  definition.Uses,
			"attempt": attempt,
		}); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "append step start event", err)
		}

		effectClass := candidate.Effect(input)
		effectKey := ""
		effectReused := false
		var result action.Result
		var executeErr error
		if effectClass == action.EffectExternal {
			if preparer, ok := candidate.(action.Preparer); ok {
				if err := preparer.Prepare(input); err != nil {
					step.Status = store.StepFailed
					step.ErrorCode = CodeActionInput
					step.ErrorMessage = err.Error()
					step.FinishedAt = e.now()
					_ = e.store.PutStep(ctx, step)
					return run, e.failRun(ctx, &run, CodeActionInput, err.Error(), err)
				}
			}
			effectKey = run.ID + ":" + definition.ID
			if supplied, ok := input["idempotency_key"].(string); ok && supplied != "" {
				effectKey = definition.Uses + ":" + supplied
			}
			effect, created, err := e.store.BeginEffect(ctx, store.EffectRecord{
				Key: effectKey, RunID: run.ID, StepID: definition.ID,
				ActionName: definition.Uses, Status: store.EffectStarted, CreatedAt: e.now(),
			})
			if err != nil {
				return run, e.failRun(ctx, &run, CodePersistence, "begin external effect", err)
			}
			if !created {
				if effect.Status == store.EffectCompleted {
					result.Output = effect.Output
					effectReused = true
				} else {
					step.Status = store.StepFailed
					step.ErrorCode = CodeEffectIndeterminate
					step.ErrorMessage = "external effect may have occurred"
					step.FinishedAt = e.now()
					_ = e.store.PutStep(ctx, step)
					return run, e.failRun(
						ctx, &run, CodeEffectIndeterminate,
						"external effect may have occurred; refusing to repeat it", nil,
					)
				}
			} else {
				result, executeErr = e.executeAction(ctx, candidate, actionDefinition, action.Invocation{
					RunID: run.ID, StepID: definition.ID, Attempt: attempt, Input: input,
					IdempotencyKey: effectKey,
					Events:         eventSink{engine: e, runID: run.ID, stepID: definition.ID},
				})
			}
		} else {
			result, executeErr = e.executeWithRetry(ctx, candidate, actionDefinition, action.Invocation{
				RunID: run.ID, StepID: definition.ID, Attempt: attempt, Input: input,
				Events: eventSink{engine: e, runID: run.ID, stepID: definition.ID},
			})
		}
		if executeErr != nil {
			step.Status = store.StepFailed
			step.ErrorCode = CodeActionPermanent
			step.ErrorMessage = executeErr.Error()
			step.FinishedAt = e.now()
			_ = e.store.PutStep(ctx, step)
			return run, e.failRun(ctx, &run, CodeActionPermanent, "execute action", executeErr)
		}
		if result.Pause != nil {
			if result.Pause.Kind != "approval" {
				return run, e.failRun(ctx, &run, CodeActionPermanent, "unsupported pause kind", nil)
			}
			now := e.now()
			approval := store.ApprovalRecord{
				ID: e.newID("approval"), RunID: run.ID, StepID: definition.ID,
				Message: result.Pause.Message, Preview: result.Pause.Preview,
				Status: store.ApprovalPending, CreatedAt: now,
			}
			step.Status = store.StepWaitingApproval
			run.Status = store.RunWaitingApproval
			run.UpdatedAt = now
			event, err := makeEvent(run.ID, definition.ID, "approval.requested", map[string]any{
				"approval_id": approval.ID,
				"message":     approval.Message,
			}, now)
			if err != nil {
				return run, e.failRun(ctx, &run, CodePersistence, "encode approval event", err)
			}
			if err := e.store.PauseForApproval(ctx, approval, step, run, event); err != nil {
				return run, e.failRun(ctx, &run, CodePersistence, "persist approval pause", err)
			}
			return run, nil
		}
		var output any
		if len(result.Output) == 0 {
			result.Output = json.RawMessage(`null`)
		}
		if err := json.Unmarshal(result.Output, &output); err != nil {
			step.Status = store.StepFailed
			step.ErrorCode = CodeActionOutput
			step.ErrorMessage = "action returned invalid JSON"
			step.FinishedAt = e.now()
			_ = e.store.PutStep(ctx, step)
			return run, e.failRun(ctx, &run, CodeActionOutput, "action returned invalid JSON", err)
		}
		if err := action.ValidateSchema(actionDefinition.OutputSchema, output); err != nil {
			step.Status = store.StepFailed
			step.ErrorCode = CodeActionOutput
			step.ErrorMessage = err.Error()
			step.FinishedAt = e.now()
			_ = e.store.PutStep(ctx, step)
			return run, e.failRun(ctx, &run, CodeActionOutput, err.Error(), err)
		}
		step.Output = append(json.RawMessage(nil), result.Output...)
		step.Status = store.StepSucceeded
		step.FinishedAt = e.now()
		run.CurrentStep = index + 1
		run.UpdatedAt = e.now()
		if effectClass == action.EffectExternal {
			if effectReused {
				if err := e.store.PutStep(ctx, step); err != nil {
					return run, e.failRun(ctx, &run, CodePersistence, "persist reused effect step", err)
				}
				if err := e.appendEvent(ctx, run.ID, definition.ID, "step.succeeded", map[string]any{
					"attempt": attempt,
					"reused":  true,
				}); err != nil {
					return run, e.failRun(ctx, &run, CodePersistence, "append reused effect event", err)
				}
				if err := e.store.UpdateRun(ctx, run); err != nil {
					return run, coded(CodePersistence, "advance reused effect step", err)
				}
				continue
			}
			event, err := makeEvent(run.ID, definition.ID, "step.succeeded", map[string]any{
				"attempt": attempt,
			}, e.now())
			if err != nil {
				return run, e.failRun(ctx, &run, CodePersistence, "encode step success event", err)
			}
			if err := e.store.CompleteEffectAndStep(
				ctx, effectKey, result.Output, e.now(), step, run, event,
			); err != nil {
				return run, e.failRun(ctx, &run, CodePersistence, "complete external effect", err)
			}
			continue
		}
		if err := e.store.PutStep(ctx, step); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "persist successful step", err)
		}
		if err := e.appendEvent(ctx, run.ID, definition.ID, "step.succeeded", map[string]any{
			"attempt": attempt,
		}); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "append step success event", err)
		}
		if err := e.store.UpdateRun(ctx, run); err != nil {
			return run, coded(CodePersistence, "advance successful step", err)
		}
	}

	expressionContext, err := e.expressionContext(ctx, run.ID, inputs)
	if err != nil {
		return run, e.failRun(ctx, &run, CodePersistence, "load workflow output context", err)
	}
	resolvedOutput, err := workflow.Resolve(wf.Outputs, expressionContext)
	if err != nil {
		return run, e.failRun(ctx, &run, CodeActionOutput, "resolve workflow outputs", err)
	}
	rawOutput, err := json.Marshal(resolvedOutput)
	if err != nil {
		return run, e.failRun(ctx, &run, CodeActionOutput, "encode workflow outputs", err)
	}
	run.Output = rawOutput
	run.Status = store.RunSucceeded
	run.ErrorCode = ""
	run.ErrorMessage = ""
	run.UpdatedAt = e.now()
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return run, coded(CodePersistence, "complete run", err)
	}
	if err := e.appendEvent(ctx, run.ID, "", "run.succeeded", nil); err != nil {
		return run, coded(CodePersistence, "append run success event", err)
	}
	return run, nil
}

func (e *Engine) executeAction(
	ctx context.Context,
	candidate action.Action,
	definition action.Definition,
	invocation action.Invocation,
) (action.Result, error) {
	actionContext, cancel := context.WithTimeout(ctx, definition.Timeout)
	defer cancel()
	return candidate.Execute(actionContext, invocation)
}

// executeWithRetry retries transient failures for read-only actions. It is never
// used for external effects, where a retry could duplicate a side effect.
func (e *Engine) executeWithRetry(
	ctx context.Context,
	candidate action.Action,
	definition action.Definition,
	invocation action.Invocation,
) (action.Result, error) {
	var (
		result action.Result
		err    error
	)
	for attempt := 1; ; attempt++ {
		result, err = e.executeAction(ctx, candidate, definition, invocation)
		if err == nil || attempt >= e.maxAttempts || !action.IsTransient(err) {
			return result, err
		}
		timer := time.NewTimer(e.backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, err
		case <-timer.C:
		}
	}
}

func (e *Engine) Approve(ctx context.Context, approvalID string) (store.RunRecord, error) {
	return e.decideApproval(ctx, approvalID, store.ApprovalApproved, "")
}

func (e *Engine) Reject(ctx context.Context, approvalID, reason string) (store.RunRecord, error) {
	return e.decideApproval(ctx, approvalID, store.ApprovalRejected, reason)
}

func (e *Engine) decideApproval(
	ctx context.Context,
	approvalID string,
	decision store.ApprovalStatus,
	reason string,
) (store.RunRecord, error) {
	approval, err := e.store.GetApproval(ctx, approvalID)
	if err != nil {
		return store.RunRecord{}, coded(CodePersistence, "load approval", err)
	}
	if approval.Status != store.ApprovalPending {
		return store.RunRecord{}, coded(CodeActionPermanent, "approval is already decided", store.ErrConflict)
	}
	run, err := e.store.GetRun(ctx, approval.RunID)
	if err != nil {
		return store.RunRecord{}, coded(CodePersistence, "load approval run", err)
	}
	step, err := e.store.GetStep(ctx, approval.RunID, approval.StepID)
	if err != nil {
		return run, coded(CodePersistence, "load approval step", err)
	}
	now := e.now()
	eventType := "approval.approved"
	output := map[string]any{"approved": true}
	if decision == store.ApprovalRejected {
		eventType = "approval.rejected"
		output = map[string]any{"approved": false, "reason": reason}
		step.Status = store.StepRejected
		run.Status = store.RunRejected
	} else {
		step.Status = store.StepApproved
		run.Status = store.RunRunning
		run.CurrentStep = step.StepIndex + 1
	}
	step.Output, err = json.Marshal(output)
	if err != nil {
		return run, coded(CodePersistence, "encode approval output", err)
	}
	step.FinishedAt = now
	run.UpdatedAt = now
	event, err := makeEvent(run.ID, step.StepID, eventType, map[string]any{
		"approval_id": approvalID,
		"reason":      reason,
	}, now)
	if err != nil {
		return run, coded(CodePersistence, "encode approval decision event", err)
	}
	if _, err := e.store.DecideApprovalAndUpdate(
		ctx, approvalID, decision, reason, now, step, run, event,
	); err != nil {
		return run, coded(CodePersistence, "persist approval decision", err)
	}
	if decision == store.ApprovalRejected {
		return run, nil
	}
	wf, err := workflow.Parse(run.WorkflowYAML)
	if err != nil {
		return run, e.failRun(ctx, &run, CodeInvalidWorkflow, "parse stored workflow", err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(run.Inputs, &inputs); err != nil {
		return run, e.failRun(ctx, &run, CodePersistence, "decode stored inputs", err)
	}
	return e.execute(ctx, wf, run, inputs)
}

func (e *Engine) expressionContext(
	ctx context.Context,
	runID string,
	inputs map[string]any,
) (workflow.Context, error) {
	steps, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		return workflow.Context{}, err
	}
	values := make(map[string]workflow.StepValue, len(steps))
	for _, step := range steps {
		var output any
		if len(step.Output) > 0 {
			if err := json.Unmarshal(step.Output, &output); err != nil {
				return workflow.Context{}, fmt.Errorf("decode step %q output: %w", step.StepID, err)
			}
		}
		values[step.StepID] = workflow.StepValue{Status: string(step.Status), Output: output}
	}
	return workflow.Context{Inputs: inputs, Steps: values}, nil
}

func (e *Engine) failRun(
	ctx context.Context,
	run *store.RunRecord,
	code string,
	message string,
	cause error,
) error {
	run.Status = store.RunFailed
	run.ErrorCode = code
	run.ErrorMessage = message
	run.UpdatedAt = e.now()
	if err := e.store.UpdateRun(ctx, *run); err != nil {
		return coded(CodePersistence, "persist failed run", err)
	}
	_ = e.appendEvent(ctx, run.ID, "", "run.failed", map[string]any{
		"code":    code,
		"message": message,
	})
	return coded(code, message, cause)
}

func (e *Engine) appendEvent(
	ctx context.Context,
	runID string,
	stepID string,
	eventType string,
	data any,
) error {
	raw := json.RawMessage(`{}`)
	if data != nil {
		encoded, err := json.Marshal(policy.Redact(data))
		if err != nil {
			return err
		}
		raw = encoded
	}
	return e.store.AppendEvent(ctx, store.EventRecord{
		RunID: runID, StepID: stepID, Type: eventType, Data: raw, CreatedAt: e.now(),
	})
}

func makeEvent(
	runID string,
	stepID string,
	eventType string,
	data any,
	createdAt time.Time,
) (store.EventRecord, error) {
	raw := json.RawMessage(`{}`)
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return store.EventRecord{}, err
		}
		raw = encoded
	}
	return store.EventRecord{
		RunID: runID, StepID: stepID, Type: eventType, Data: raw, CreatedAt: createdAt,
	}, nil
}

type eventSink struct {
	engine *Engine
	runID  string
	stepID string
}

func (sink eventSink) Emit(ctx context.Context, event action.Event) error {
	return sink.engine.appendEvent(ctx, sink.runID, sink.stepID, event.Type, event.Data)
}

func normalizeInputs(inputs map[string]any) (map[string]any, error) {
	if inputs == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("encode inputs: %w", err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, fmt.Errorf("decode inputs: %w", err)
	}
	return normalized, nil
}

func randomID(prefix string) string {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buffer[:])
}
