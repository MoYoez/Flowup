package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/moyoez/flowup/internal/action"
)

// Install copies declared local files into a new version directory, then updates
// discovery. Old directories stay available to already-started runs.
func Install(root, path string, reserved []string) ([]Binding, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "plugin.yaml")
	}
	_, raw, err := Load(path)
	if err != nil {
		return nil, err
	}
	var source Snapshot
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, name := range reserved {
		blocked[name] = true
	}
	for _, binding := range source.Bindings {
		if blocked[binding.Spec.Name] {
			return nil, fmt.Errorf("duplicate action %q: cannot replace a built-in", binding.Spec.Name)
		}
	}
	installed, err := readInstalled(root)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	packages := filepath.Join(absRoot, "packages")
	if err := os.MkdirAll(packages, 0700); err != nil {
		return nil, err
	}
	var added []Binding
	for _, binding := range source.Bindings {
		dir, err := os.MkdirTemp(packages, binding.Spec.Name+"-")
		if err != nil {
			return nil, err
		}
		spec := binding.Spec
		spec.Command = append([]string(nil), spec.Command...)
		files := append([]string(nil), spec.Files...)
		if rel, err := filepath.Rel(binding.Directory, binding.Executable); err == nil && filepath.IsLocal(rel) {
			files = append(files, rel)
			spec.Command = append([]string(nil), spec.Command...)
			spec.Command[0] = "." + string(filepath.Separator) + rel
		}
		for _, file := range files {
			if strings.EqualFold(filepath.Clean(file), ".flowup-manifest.json") {
				return nil, fmt.Errorf("plugin file name .flowup-manifest.json is reserved")
			}
			data, err := os.ReadFile(filepath.Join(binding.Directory, file))
			if err != nil {
				return nil, err
			}
			destination := filepath.Join(dir, file)
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				return nil, err
			}
			info, err := os.Stat(filepath.Join(binding.Directory, file))
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
				return nil, err
			}
			original := filepath.Join(binding.Directory, file)
			for i, arg := range spec.Command {
				if !filepath.IsAbs(arg) {
					continue
				}
				arg = filepath.Clean(arg)
				if arg == original || (runtime.GOOS == "windows" && strings.EqualFold(arg, original)) {
					spec.Command[i] = destination
				}
			}
		}
		manifest, err := json.Marshal(Manifest{Version: 1, Plugins: []Spec{spec}})
		if err != nil {
			return nil, err
		}
		manifestPath := filepath.Join(dir, ".flowup-manifest.json")
		if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
			return nil, err
		}
		_, saved, err := Load(manifestPath)
		if err != nil {
			return nil, err
		}
		var snapshot Snapshot
		if err := json.Unmarshal(saved, &snapshot); err != nil {
			return nil, err
		}
		added = append(added, snapshot.Bindings...)
	}
	byName := map[string]Binding{}
	for _, b := range installed.Bindings {
		byName[b.Spec.Name] = b
	}
	for _, b := range added {
		byName[b.Spec.Name] = b
	}
	installed.Bindings = nil
	for _, b := range byName {
		installed.Bindings = append(installed.Bindings, b)
	}
	sort.Slice(installed.Bindings, func(i, j int) bool { return installed.Bindings[i].Spec.Name < installed.Bindings[j].Spec.Name })
	if err := writeInstalled(root, installed); err != nil {
		return nil, err
	}
	return added, nil
}

func Uninstall(root, name string) error {
	installed, err := readInstalled(root)
	if err != nil {
		return err
	}
	kept := make([]Binding, 0, len(installed.Bindings))
	found := false
	for _, b := range installed.Bindings {
		if b.Spec.Name == name {
			found = true
		} else {
			kept = append(kept, b)
		}
	}
	if !found {
		return fmt.Errorf("plugin %q is not installed in this project", name)
	}
	installed.Bindings = kept
	return writeInstalled(root, installed)
}

// Installed loads only actions referenced by this workflow. An unavailable
// unrelated plugin must not prevent core-only workflows from running.
func Installed(root string, names map[string]bool) ([]action.Action, json.RawMessage, error) {
	installed, err := readInstalled(root)
	if err != nil {
		return nil, nil, err
	}
	selected := Snapshot{Version: 1}
	for _, b := range installed.Bindings {
		if names[b.Spec.Name] {
			selected.Bindings = append(selected.Bindings, b)
		}
	}
	if len(selected.Bindings) == 0 {
		return nil, nil, nil
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return nil, nil, err
	}
	actions, err := selected.actions()
	return actions, raw, err
}

func readInstalled(root string) (Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(root, "installed.json"))
	if os.IsNotExist(err) {
		return Snapshot{Version: 1}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("read installed plugins: %w", err)
	}
	if snapshot.Version != 1 {
		return Snapshot{}, fmt.Errorf("unsupported installed plugin registry version")
	}
	return snapshot, nil
}

func writeInstalled(root string, snapshot Snapshot) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".installed-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(root, "installed.json"))
}
