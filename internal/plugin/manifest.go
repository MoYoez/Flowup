// Package plugin adapts explicitly trusted local programs to Flowup actions.
// A subprocess is an integration boundary, not a security sandbox.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const maxManifestBytes = 1 << 20

var actionName = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Manifest struct {
	Version int    `json:"version" yaml:"version"`
	Plugins []Spec `json:"plugins" yaml:"plugins"`
}

type Spec struct {
	Name         string             `json:"name" yaml:"name"`
	Version      string             `json:"version" yaml:"version"`
	Command      []string           `json:"command" yaml:"command"`
	Files        []string           `json:"files,omitempty" yaml:"files,omitempty"`
	Env          []string           `json:"env,omitempty" yaml:"env,omitempty"`
	InputSchema  map[string]any     `json:"input_schema" yaml:"input_schema"`
	OutputSchema map[string]any     `json:"output_schema" yaml:"output_schema"`
	SecretPaths  []string           `json:"secret_paths,omitempty" yaml:"secret_paths,omitempty"`
	Effect       action.EffectClass `json:"effect" yaml:"effect"`
	Timeout      string             `json:"timeout" yaml:"timeout"`
}

type Binding struct {
	Spec       Spec              `json:"spec"`
	Directory  string            `json:"directory"`
	Executable string            `json:"executable"`
	Hashes     map[string]string `json:"hashes"`
}

type Snapshot struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
}

// Load reads configuration and files only; it never executes a plugin.
func Load(path string) ([]action.Action, json.RawMessage, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read plugin manifest: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, nil, fmt.Errorf("plugin manifest exceeds 1 MiB")
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, fmt.Errorf("decode plugin manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, fmt.Errorf("plugin manifest must contain exactly one YAML document")
	}
	if manifest.Version != 1 || len(manifest.Plugins) == 0 {
		return nil, nil, fmt.Errorf("plugin manifest requires version: 1 and a nonempty plugins list")
	}
	snapshot := Snapshot{Version: 1}
	for _, spec := range manifest.Plugins {
		if err := validateSpec(spec); err != nil {
			return nil, nil, err
		}
		dir := filepath.Dir(abs)
		program := spec.Command[0]
		if !filepath.IsAbs(program) && strings.ContainsAny(program, `/\`) {
			program = filepath.Join(dir, program)
		}
		executable, err := exec.LookPath(program)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin %q executable: %w", spec.Name, err)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return nil, nil, err
		}
		binding := Binding{Spec: spec, Directory: dir, Executable: executable, Hashes: map[string]string{}}
		paths := []string{executable}
		for _, file := range spec.Files {
			paths = append(paths, filepath.Join(dir, file))
		}
		for _, path := range paths {
			hash, err := hashFile(path)
			if err != nil {
				return nil, nil, fmt.Errorf("plugin %q: cannot fingerprint %q: %w; use readable files and a real executable path (Windows app execution aliases are not supported)", spec.Name, path, err)
			}
			binding.Hashes[path] = hash
		}
		snapshot.Bindings = append(snapshot.Bindings, binding)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, err
	}
	actions, err := snapshot.actions()
	return actions, raw, err
}

func Restore(raw json.RawMessage) ([]action.Action, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode saved plugin bindings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing plugin bindings")
	}
	return snapshot.actions()
}

func (s Snapshot) actions() ([]action.Action, error) {
	if s.Version != 1 {
		return nil, fmt.Errorf("unsupported plugin binding version %d", s.Version)
	}
	var actions []action.Action
	seen := map[string]bool{}
	for _, binding := range s.Bindings {
		if err := validateSpec(binding.Spec); err != nil {
			return nil, err
		}
		if seen[binding.Spec.Name] {
			return nil, fmt.Errorf("duplicate action %q", binding.Spec.Name)
		}
		seen[binding.Spec.Name] = true
		if !filepath.IsAbs(binding.Directory) || !filepath.IsAbs(binding.Executable) || binding.Hashes[binding.Executable] == "" {
			return nil, fmt.Errorf("invalid plugin binding paths")
		}
		for _, file := range binding.Spec.Files {
			if binding.Hashes[filepath.Join(binding.Directory, file)] == "" {
				return nil, fmt.Errorf("plugin %q missing file fingerprint", binding.Spec.Name)
			}
		}
		if err := binding.verify(); err != nil {
			return nil, err
		}
		actions = append(actions, &Process{binding: binding})
	}
	return actions, nil
}

func validateSpec(spec Spec) error {
	fail := func(message string) error { return fmt.Errorf("plugin %q: %s", spec.Name, message) }
	if !actionName.MatchString(spec.Name) {
		return fail("name must be namespaced, e.g. local.summarize")
	}
	if strings.TrimSpace(spec.Version) == "" {
		return fail("version is required")
	}
	if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
		return fail("command must be a nonempty argv list")
	}
	for _, arg := range spec.Command {
		if strings.ContainsRune(arg, 0) {
			return fail("command contains NUL")
		}
	}
	if spec.Effect != action.EffectReadOnly && spec.Effect != action.EffectExternal {
		return fail("effect must be read_only or external")
	}
	duration, err := time.ParseDuration(spec.Timeout)
	if err != nil || duration <= 0 {
		return fail("timeout must be a positive duration, e.g. 30s")
	}
	for _, name := range spec.Env {
		if !envName.MatchString(name) {
			return fail("invalid environment variable name")
		}
	}
	for _, file := range spec.Files {
		if !filepath.IsLocal(file) {
			return fail("files must be relative paths inside the manifest directory")
		}
	}
	for _, path := range spec.SecretPaths {
		for _, part := range strings.Split(path, ".") {
			if part == "" {
				return fail("secret_paths must contain nonempty dot-separated paths")
			}
		}
	}
	for _, schema := range []map[string]any{spec.InputSchema, spec.OutputSchema} {
		if schema == nil {
			return fail("input_schema and output_schema are required")
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("mem://plugin/schema.json", schema); err != nil {
			return fail("invalid JSON Schema: " + err.Error())
		}
		if _, err := compiler.Compile("mem://plugin/schema.json"); err != nil {
			return fail("invalid JSON Schema: " + err.Error())
		}
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin file %q is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (b Binding) verify() error {
	for path, want := range b.Hashes {
		got, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("plugin %q pinned file unavailable: %w", b.Spec.Name, err)
		}
		if got != want {
			return fmt.Errorf("plugin %q file changed: %s; restore the pinned files or start a new run", b.Spec.Name, path)
		}
	}
	return nil
}
