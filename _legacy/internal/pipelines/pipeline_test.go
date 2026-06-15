package pipelines_test

import (
	"strings"
	"testing"

	"github.com/moyoez/flowup/internal/pipelines"
)

func TestDemoPipelineLoadsAndValidates(t *testing.T) {
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Pipeline != "deploy_repo_to_paas" {
		t.Fatalf("unexpected pipeline id %q", p.Pipeline)
	}
	order, err := p.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{order[0].ID, order[1].ID, order[2].ID}
	want := []string{"build", "deploy", "verify"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("topo order = %v, want %v", got, want)
		}
	}
}

func TestMermaidIsDerivedFromDefinition(t *testing.T) {
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := p.Mermaid()
	for _, sub := range []string{
		"flowchart TD",
		"params: repo_url, target_env",
		"build · agent",
		"claude-opus-4-6 → claude-sonnet-4-6", // primary→backup slide
		`"artifact_path, build_sha"`,          // produced edge label
		"NEEDS_HUMAN",
		"resume_token",
	} {
		if !strings.Contains(m, sub) {
			t.Errorf("mermaid missing %q", sub)
		}
	}
}

func TestTemplateResolution(t *testing.T) {
	ctx := map[string]any{
		"params": map[string]any{"repo_url": "u", "target_env": "staging"},
		"build":  map[string]any{"build_sha": "sha1"},
	}
	// Whole-string single reference preserves the value.
	v, err := pipelines.ResolveValue("{{ params.repo_url }}", ctx)
	if err != nil || v != "u" {
		t.Fatalf("single ref: v=%v err=%v", v, err)
	}
	// Interpolation joins into a string (the deploy idempotency key shape).
	v2, err := pipelines.ResolveValue("{{ params.repo_url }}@{{ build.build_sha }}->{{ params.target_env }}", ctx)
	if err != nil || v2 != "u@sha1->staging" {
		t.Fatalf("interpolation: v=%v err=%v", v2, err)
	}
	// Unknown reference is an error, not a silent empty.
	if _, err := pipelines.ResolveValue("{{ params.nope }}", ctx); err == nil {
		t.Fatal("want error for unknown reference")
	}
}

func TestValidateRejectsCycle(t *testing.T) {
	yaml := `pipeline: cyclic
nodes:
  - {id: a, kind: native, needs: [b], profile: {models: [m]}}
  - {id: b, kind: native, needs: [a], profile: {models: [m]}}
`
	if _, err := pipelines.LoadBytes([]byte(yaml)); err == nil {
		t.Fatal("want a cycle-detection error")
	}
}

func TestValidateRejectsUnknownDependency(t *testing.T) {
	yaml := `pipeline: bad
nodes:
  - {id: a, kind: native, needs: [ghost], profile: {models: [m]}}
`
	if _, err := pipelines.LoadBytes([]byte(yaml)); err == nil {
		t.Fatal("want an unknown-dependency error")
	}
}

func TestSchemaValidationEnforcesEnum(t *testing.T) {
	p, err := pipelines.Load("../../examples/deploy_repo_to_paas.pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"repo_url":"https://x","target_env":"bogus"}`)
	if err := pipelines.ValidateJSONAgainstSchema(p.InputSchema, bad); err == nil {
		t.Fatal("want enum violation for target_env=bogus")
	}
	good := []byte(`{"repo_url":"https://x","target_env":"staging"}`)
	if err := pipelines.ValidateJSONAgainstSchema(p.InputSchema, good); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
}

// Arrays/maps resolve element-wise so a native node's argv can be templated.
func TestTemplateResolvesArrays(t *testing.T) {
	ctx := map[string]any{"params": map[string]any{"msg": "hi", "argv": []any{"echo", "x"}}}

	// Whole-string ref to an array preserves the array (type, not stringified).
	v, err := pipelines.ResolveValue("{{ params.argv }}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if arr, ok := v.([]any); !ok || len(arr) != 2 {
		t.Fatalf("want []any len 2, got %T %v", v, v)
	}

	// Templates inside array elements resolve.
	v2, err := pipelines.ResolveValue([]any{"sh", "-c", "{{ params.msg }}"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a2 := v2.([]any); a2[2] != "hi" {
		t.Fatalf("array element not resolved: %v", a2)
	}
}
