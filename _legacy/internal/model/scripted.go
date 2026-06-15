package model

import (
	"context"
	"sync"
)

// Scripted is a deterministic Client for tests/CI/demos: it returns the given
// Responses in order (one per Complete call), then a default final answer. It
// lets the whole agent tool-loop be verified with zero API calls / zero cost.
type Scripted struct {
	Responses   []Response // returned in call order
	Final       string     // default final text once Responses are exhausted
	CostPerCall float64    // applied when a Response has CostUSD == 0

	mu    sync.Mutex
	calls int
}

// NewScripted builds a scripted client from a list of responses.
func NewScripted(responses ...Response) *Scripted {
	return &Scripted{Responses: responses}
}

func (s *Scripted) Name() string { return "scripted" }

// Calls reports how many completions were requested (for assertions).
func (s *Scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *Scripted) Complete(_ context.Context, _ Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i < len(s.Responses) {
		r := s.Responses[i]
		if r.CostUSD == 0 {
			r.CostUSD = s.CostPerCall
		}
		return r, nil
	}
	final := s.Final
	if final == "" {
		final = "{}"
	}
	return Response{Text: final, CostUSD: s.CostPerCall}, nil
}
