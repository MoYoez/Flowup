package workflow

type Workflow struct {
	Name    string               `yaml:"name"`
	Version int                  `yaml:"version"`
	Inputs  map[string]InputSpec `yaml:"inputs,omitempty"`
	Steps   []Step               `yaml:"steps"`
	Outputs map[string]any       `yaml:"outputs,omitempty"`
}

type InputSpec struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required,omitempty"`
}

type Step struct {
	ID   string         `yaml:"id"`
	Uses string         `yaml:"uses"`
	If   string         `yaml:"if,omitempty"`
	With map[string]any `yaml:"with,omitempty"`
}
