// Package pipelines is the YAML-truth-source side of the system: load a
// predefined, parameterized pipeline definition, validate it, and derive views
// (e.g. Mermaid) from it. The definition is the single source of truth; the
// visualization is a derived view, never a second truth.
package pipelines

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/moyoez/flowup/internal/contracts"
)

// Pipeline is the parsed YAML definition. Field shapes line up with the Go
// contract types so a node definition turns into a contracts.NodeTask with no
// surprises.
type Pipeline struct {
	Pipeline    string         `yaml:"pipeline" json:"pipeline"`
	Version     int            `yaml:"version" json:"version"`
	Description string         `yaml:"description" json:"description"`
	InputSchema map[string]any `yaml:"input_schema" json:"input_schema"`
	Nodes       []Node         `yaml:"nodes" json:"nodes"`
	Output      map[string]any `yaml:"output" json:"output"` // template expressions resolved against final run state
}

// Node is one step in the DAG.
type Node struct {
	ID             string         `yaml:"id" json:"id"`
	Kind           contracts.Kind `yaml:"kind" json:"kind"`
	Needs          []string       `yaml:"needs" json:"needs"`
	RequiresCaps   []string       `yaml:"requires_caps" json:"requires_caps"`
	Profile        Profile        `yaml:"profile" json:"profile"`
	Inputs         map[string]any `yaml:"inputs" json:"inputs"`
	Limits         Limits         `yaml:"limits" json:"limits"`
	Retry          *Retry         `yaml:"retry" json:"retry"`
	SideEffect     bool           `yaml:"side_effect" json:"side_effect"`
	IdempotencyKey string         `yaml:"idempotency_key" json:"idempotency_key"`
	OnNeedsHuman   *OnNeedsHuman  `yaml:"on_needs_human" json:"on_needs_human"`
	OutputSchema   map[string]any `yaml:"output_schema" json:"output_schema"`
}

// Profile is the casting block (maps to contracts.ExecProfile).
type Profile struct {
	Models []string `yaml:"models" json:"models"`
	Role   string   `yaml:"role" json:"role"`
	Tools  []string `yaml:"tools" json:"tools"`
	Temp   float64  `yaml:"temp" json:"temp"`
}

// Limits bound a node's cost (maps to contracts.Limits).
type Limits struct {
	MaxSteps   int     `yaml:"max_steps" json:"max_steps"`
	BudgetUSD  float64 `yaml:"budget_usd" json:"budget_usd"`
	TimeoutSec int     `yaml:"timeout_sec" json:"timeout_sec"`
}

// Retry says how many attempts a node gets and which reasons are retriable.
// attempt drives both the retry count and the slide down Profile.Models.
type Retry struct {
	MaxAttempts int      `yaml:"max_attempts" json:"max_attempts"`
	On          []string `yaml:"on" json:"on"`
}

// OnNeedsHuman declares the first-class pause for a node.
type OnNeedsHuman struct {
	When    string `yaml:"when" json:"when"`
	Handoff string `yaml:"handoff" json:"handoff"`
}

// Load reads and validates a pipeline definition from a YAML file.
func Load(path string) (*Pipeline, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pipeline %s: %w", path, err)
	}
	return LoadBytes(b)
}

// LoadBytes parses and validates a pipeline definition from YAML bytes.
func LoadBytes(b []byte) (*Pipeline, error) {
	var p Pipeline
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse pipeline yaml: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// NodeByID returns the node with the given id, or false.
func (p *Pipeline) NodeByID(id string) (Node, bool) {
	for _, n := range p.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// MaxAttempts is the number of attempts a node gets (>= 1).
func (n Node) MaxAttempts() int {
	if n.Retry == nil || n.Retry.MaxAttempts < 1 {
		return 1
	}
	return n.Retry.MaxAttempts
}

// RetriesOn reports whether the node's retry policy covers a given reason class.
func (n Node) RetriesOn(reasonClass string) bool {
	if n.Retry == nil {
		return false
	}
	if len(n.Retry.On) == 0 {
		return true // retry on any failure if max_attempts is set but no filter
	}
	for _, r := range n.Retry.On {
		if r == reasonClass {
			return true
		}
	}
	return false
}

// ExecProfile converts the YAML profile into the wire contract.
func (n Node) ExecProfile() contracts.ExecProfile {
	return contracts.ExecProfile{
		Models: n.Profile.Models,
		Role:   n.Profile.Role,
		Tools:  n.Profile.Tools,
		Temp:   n.Profile.Temp,
	}
}

// ContractLimits converts the YAML limits into the wire contract.
func (n Node) ContractLimits() contracts.Limits {
	return contracts.Limits{
		MaxSteps:   n.Limits.MaxSteps,
		BudgetUSD:  n.Limits.BudgetUSD,
		TimeoutSec: n.Limits.TimeoutSec,
	}
}

// Validate checks the definition is structurally sound: unique ids, references
// resolve, the graph is a DAG with an entry, and casting is non-empty. This is
// the "the definition is the truth, so the truth must be valid" gate.
func (p *Pipeline) Validate() error {
	if p.Pipeline == "" {
		return fmt.Errorf("pipeline: missing top-level `pipeline` name")
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("pipeline %q: no nodes", p.Pipeline)
	}

	ids := make(map[string]bool, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.ID == "" {
			return fmt.Errorf("pipeline %q: a node has an empty id", p.Pipeline)
		}
		if ids[n.ID] {
			return fmt.Errorf("pipeline %q: duplicate node id %q", p.Pipeline, n.ID)
		}
		ids[n.ID] = true
	}

	for _, n := range p.Nodes {
		if n.Kind == "" {
			return fmt.Errorf("node %q: missing kind", n.ID)
		}
		if len(n.Profile.Models) == 0 {
			return fmt.Errorf("node %q: profile.models is empty (no casting)", n.ID)
		}
		for _, dep := range n.Needs {
			if !ids[dep] {
				return fmt.Errorf("node %q: needs unknown node %q", n.ID, dep)
			}
			if dep == n.ID {
				return fmt.Errorf("node %q: depends on itself", n.ID)
			}
		}
		if n.Retry != nil && n.Retry.MaxAttempts < 1 {
			return fmt.Errorf("node %q: retry.max_attempts must be >= 1", n.ID)
		}
		if n.SideEffect && n.IdempotencyKey == "" {
			return fmt.Errorf("node %q: side_effect node must declare an idempotency_key", n.ID)
		}
	}

	if _, err := p.TopoOrder(); err != nil {
		return err
	}
	return nil
}

// TopoOrder returns the nodes in a deterministic topological order (Kahn's
// algorithm; ties broken by declaration order). It is also the acyclicity check.
func (p *Pipeline) TopoOrder() ([]Node, error) {
	order := make(map[string]int, len(p.Nodes))
	for i, n := range p.Nodes {
		order[n.ID] = i
	}
	indeg := make(map[string]int, len(p.Nodes))
	dependents := make(map[string][]string, len(p.Nodes))
	for _, n := range p.Nodes {
		indeg[n.ID] = len(n.Needs)
		for _, dep := range n.Needs {
			dependents[dep] = append(dependents[dep], n.ID)
		}
	}

	var ready []string
	for _, n := range p.Nodes {
		if indeg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return order[ready[i]] < order[ready[j]] })

	var out []Node
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		n, _ := p.NodeByID(id)
		out = append(out, n)

		var unlocked []string
		for _, d := range dependents[id] {
			indeg[d]--
			if indeg[d] == 0 {
				unlocked = append(unlocked, d)
			}
		}
		sort.Slice(unlocked, func(i, j int) bool { return order[unlocked[i]] < order[unlocked[j]] })
		ready = append(ready, unlocked...)
	}

	if len(out) != len(p.Nodes) {
		return nil, fmt.Errorf("pipeline %q: dependency cycle detected", p.Pipeline)
	}
	return out, nil
}
