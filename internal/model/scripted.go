package model

import (
	"context"
	"fmt"
	"sync"
)

type Script struct {
	Response Response
	Err      error
}

type Scripted struct {
	mu       sync.Mutex
	scripts  []Script
	requests []Request
}

func NewScripted(scripts ...Script) *Scripted {
	return &Scripted{scripts: append([]Script(nil), scripts...)}
}

func (s *Scripted) Generate(_ context.Context, request Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if len(s.scripts) == 0 {
		return Response{}, fmt.Errorf("scripted model has no response")
	}
	script := s.scripts[0]
	s.scripts = s.scripts[1:]
	return script.Response, script.Err
}

func (s *Scripted) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}
