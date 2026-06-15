package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/model"
)

// FS reads/writes files confined to a work directory (path traversal is blocked).
type FS struct {
	Root string
}

// NewFS roots file operations at root (a temp dir if empty).
func NewFS(root string) *FS {
	if root == "" {
		root = filepath.Join(os.TempDir(), "flowup-fs")
	}
	_ = os.MkdirAll(root, 0o755)
	return &FS{Root: root}
}

func (*FS) Name() string { return "fs" }

func (*FS) Spec() model.ToolSpec {
	return model.ToolSpec{
		Name:        "fs",
		Description: "Read or write a file inside the work directory. op='read'|'write', path (relative), content (for write).",
		InputSchema: json.RawMessage(`{"type":"object","required":["op","path"],"properties":{"op":{"type":"string","enum":["read","write"]},"path":{"type":"string"},"content":{"type":"string"}}}`),
	}
}

func (f *FS) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Op, Path, Content string
	}
	if err := sonic.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("bad args: %w", err)
	}
	full, err := f.safePath(a.Path)
	if err != nil {
		return "", err
	}
	switch a.Op {
	case "read":
		b, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "write":
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
	default:
		return "", fmt.Errorf("fs: unknown op %q", a.Op)
	}
}

// safePath joins rel under Root and rejects escapes (e.g. "../").
func (f *FS) safePath(rel string) (string, error) {
	full := filepath.Join(f.Root, filepath.Clean("/"+rel))
	root := filepath.Clean(f.Root)
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("fs: path escapes work dir")
	}
	return full, nil
}
