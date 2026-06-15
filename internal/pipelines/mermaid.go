package pipelines

import (
	"fmt"
	"strings"
)

// Mermaid renders the pipeline definition as a Mermaid flowchart. The diagram
// (primary and backup models, retries, the needs_human loopback, the terminal
// states) is computed from the YAML, never hand-drawn — change the YAML and the
// diagram follows.
func (p *Pipeline) Mermaid() string {
	byID := make(map[string]Node, len(p.Nodes))
	hasDependents := make(map[string]bool)
	needHuman := false
	for _, n := range p.Nodes {
		byID[n.ID] = n
		for _, d := range n.Needs {
			hasDependents[d] = true
		}
		if n.OnNeedsHuman != nil {
			needHuman = true
		}
	}

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("flowchart TD")
	inProps := strings.Join(schemaPropertyKeys(p.InputSchema), ", ")
	w(`  START(["Invocation · %s<br/>params: %s"])`, esc(p.Pipeline), esc(inProps))
	w(`  OK(["ok: 类型化 output"])`)
	w(`  FAILED(["failed: 分类 reason"])`)
	if needHuman {
		w(`  NEEDS_HUMAN(["needs_human<br/>handoff + resume_token"])`)
	}
	w("")

	for _, n := range p.Nodes {
		w(`  %s["%s"]`, n.ID, nodeLabel(n))
	}
	w("")

	for _, n := range p.Nodes {
		if len(n.Needs) == 0 {
			w("  START --> %s", n.ID)
		}
	}
	for _, n := range p.Nodes {
		for _, d := range n.Needs {
			lbl := produced(byID[d])
			if lbl != "" {
				w(`  %s -->|"%s"| %s`, d, esc(lbl), n.ID)
			} else {
				w("  %s --> %s", d, n.ID)
			}
		}
	}
	for _, n := range p.Nodes {
		if !hasDependents[n.ID] {
			w("  %s --> OK", n.ID)
		}
	}

	for _, n := range p.Nodes {
		if n.OnNeedsHuman != nil {
			when := n.OnNeedsHuman.When
			if when == "" {
				when = "需人工"
			}
			w("  %s -. %s .-> NEEDS_HUMAN", n.ID, esc(when))
			w("  NEEDS_HUMAN -. resume_token .-> %s", n.ID)
		}
	}
	for _, n := range p.Nodes {
		if n.Retry != nil {
			w("  %s -. 重试耗尽/超预算 .-> FAILED", n.ID)
		}
	}
	w("")

	w("  classDef ok fill:#e6f4ea,stroke:#34a853,color:#137333;")
	w("  classDef fail fill:#fce8e6,stroke:#ea4335,color:#a50e0e;")
	w("  classDef human fill:#fef7e0,stroke:#f9ab00,color:#b06000;")
	w("  class OK ok;")
	w("  class FAILED fail;")
	if needHuman {
		w("  class NEEDS_HUMAN human;")
	}
	return b.String()
}

func nodeLabel(n Node) string {
	parts := []string{fmt.Sprintf("%s · %s", n.ID, n.Kind)}
	if len(n.RequiresCaps) > 0 {
		parts = append(parts, "caps: "+strings.Join(n.RequiresCaps, ","))
	}
	switch {
	case len(n.Profile.Models) > 1:
		parts = append(parts, "models: "+strings.Join(n.Profile.Models, " → ")+"  (主→备)")
	case len(n.Profile.Models) == 1:
		parts = append(parts, "model: "+n.Profile.Models[0])
	}
	if n.Retry != nil {
		parts = append(parts, fmt.Sprintf("retry×%d → 滑备用模型", n.Retry.MaxAttempts))
	}
	if n.SideEffect {
		parts = append(parts, "side_effect · 幂等去重")
	}
	for i := range parts {
		parts[i] = esc(parts[i])
	}
	return strings.Join(parts, "<br/>")
}

// produced lists the fields a node guarantees on its edge label.
func produced(n Node) string {
	req := schemaRequired(n.OutputSchema)
	if len(req) == 0 {
		req = schemaPropertyKeys(n.OutputSchema)
	}
	return strings.Join(req, ", ")
}

func esc(s string) string { return strings.ReplaceAll(s, `"`, "'") }
