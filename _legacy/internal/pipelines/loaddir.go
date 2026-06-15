package pipelines

import (
	"fmt"
	"path/filepath"
)

// LoadDir loads and validates every *.pipeline.yaml in a directory (a simple
// pipeline registry source for the engine service).
func LoadDir(dir string) ([]*Pipeline, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.pipeline.yaml"))
	if err != nil {
		return nil, err
	}
	out := make([]*Pipeline, 0, len(matches))
	for _, m := range matches {
		p, err := Load(m)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", m, err)
		}
		out = append(out, p)
	}
	return out, nil
}
