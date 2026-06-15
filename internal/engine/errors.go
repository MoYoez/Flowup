package engine

import (
	"errors"
)

const (
	CodeInvalidWorkflow     = "invalid_workflow"
	CodeInvalidInputs       = "invalid_inputs"
	CodePolicyViolation     = "policy_violation"
	CodeActionInput         = "action_input"
	CodeActionOutput        = "action_output"
	CodeActionTransient     = "action_transient"
	CodeActionPermanent     = "action_permanent"
	CodeApprovalRejected    = "approval_rejected"
	CodeEffectIndeterminate = "effect_indeterminate"
	CodePersistence         = "persistence"
)

type Error struct {
	Code      string
	Message   string
	Retriable bool
	Cause     error
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func AsError(err error, target **Error) bool {
	return errors.As(err, target)
}

func coded(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}
