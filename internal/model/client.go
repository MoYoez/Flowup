package model

import "os"

// New selects a model client from the environment:
//
//	FLOWUP_MODEL=anthropic   → real Anthropic client (needs ANTHROPIC_API_KEY)
//	otherwise                → nil (no model configured; agent/semantic nodes fail
//	                           with model_runner_not_configured upstream)
//
// Tests/demos construct NewScripted(...) directly.
func New() Client {
	switch os.Getenv("FLOWUP_MODEL") {
	case "anthropic":
		return NewAnthropic("")
	case "openai":
		return NewOpenAI()
	default:
		return nil
	}
}
