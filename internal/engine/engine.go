package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/workflow"
)

type Option func(*Engine)

type Engine struct {
	store   store.Store
	actions *action.Registry
	now     func() time.Time
	newID   func(prefix string) string
}

func New(state store.Store, actions *action.Registry, options ...Option) *Engine {
	engine := &Engine{
		store:   state,
		actions: actions,
		now:     func() time.Time { return time.Now().UTC() },
		newID:   randomID,
	}
	for _, option := range options {
		option(engine)
	}
	return engine
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
		input, ok := resolved.(map[string]any)
		if !ok {
			return run, e.failRun(ctx, &run, CodeActionInput, "action input must be an object", nil)
		}
		candidate, ok := e.actions.Get(definition.Uses)
		if !ok {
			return run, e.failRun(ctx, &run, CodeInvalidWorkflow, "unknown action "+definition.Uses, nil)
		}
		actionDefinition := candidate.Definition()
		if err := action.ValidateSchema(actionDefinition.InputSchema, input); err != nil {
			return run, e.failRun(ctx, &run, CodeActionInput, err.Error(), err)
		}
		rawInput, err := json.Marshal(input)
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

		actionContext, cancel := context.WithTimeout(ctx, actionDefinition.Timeout)
		result, executeErr := candidate.Execute(actionContext, action.Invocation{
			RunID: run.ID, StepID: definition.ID, Attempt: attempt, Input: input,
			Events: eventSink{engine: e, runID: run.ID, stepID: definition.ID},
		})
		cancel()
		if executeErr != nil {
			step.Status = store.StepFailed
			step.ErrorCode = CodeActionPermanent
			step.ErrorMessage = executeErr.Error()
			step.FinishedAt = e.now()
			_ = e.store.PutStep(ctx, step)
			return run, e.failRun(ctx, &run, CodeActionPermanent, "execute action", executeErr)
		}
		if result.Pause != nil {
			return run, e.failRun(ctx, &run, CodeActionPermanent, "action pause is not supported yet", nil)
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
		if err := e.store.PutStep(ctx, step); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "persist successful step", err)
		}
		if err := e.appendEvent(ctx, run.ID, definition.ID, "step.succeeded", map[string]any{
			"attempt": attempt,
		}); err != nil {
			return run, e.failRun(ctx, &run, CodePersistence, "append step success event", err)
		}
		run.CurrentStep = index + 1
		run.UpdatedAt = e.now()
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
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = encoded
	}
	return e.store.AppendEvent(ctx, store.EventRecord{
		RunID: runID, StepID: stepID, Type: eventType, Data: raw, CreatedAt: e.now(),
	})
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
