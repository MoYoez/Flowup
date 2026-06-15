package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/tools"
)

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-body"))
	}))
	defer srv.Close()

	out, err := tools.NewHTTPGet().Invoke(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 200") || !strings.Contains(out, "hello-body") {
		t.Fatalf("unexpected http_get result: %q", out)
	}
}

func TestShellViaSandbox(t *testing.T) {
	sh := tools.NewShell(sandbox.NewSubprocess())
	argv, _ := sonic.Marshal(map[string]any{"argv": echoArgv("shelltool")})
	out, err := sh.Invoke(context.Background(), argv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "shelltool") || !strings.Contains(out, "exit=0") {
		t.Fatalf("unexpected shell result: %q", out)
	}
}

func TestRegistryWhitelist(t *testing.T) {
	reg := tools.NewRegistry(tools.NewHTTPGet())

	if !reg.Allowed("http_get", []string{"http_get"}) {
		t.Fatal("whitelisted+registered tool should be allowed")
	}
	if reg.Allowed("http_get", []string{"shell"}) {
		t.Fatal("non-whitelisted tool must not be allowed")
	}
	if reg.Allowed("nope", []string{"nope"}) {
		t.Fatal("unregistered tool must not be allowed")
	}
	// Only registered+whitelisted tools are advertised to the model.
	if specs := reg.Specs([]string{"http_get", "shell"}); len(specs) != 1 || specs[0].Name != "http_get" {
		t.Fatalf("specs should contain only the registered http_get, got %+v", specs)
	}
}

func TestFS(t *testing.T) {
	fs := tools.NewFS(t.TempDir())
	ctx := context.Background()

	w, err := fs.Invoke(ctx, json.RawMessage(`{"op":"write","path":"sub/a.txt","content":"hi-fs"}`))
	if err != nil || !strings.Contains(w, "wrote") {
		t.Fatalf("write: %q err=%v", w, err)
	}
	r, err := fs.Invoke(ctx, json.RawMessage(`{"op":"read","path":"sub/a.txt"}`))
	if err != nil || r != "hi-fs" {
		t.Fatalf("read: %q err=%v", r, err)
	}
	if _, err := fs.Invoke(ctx, json.RawMessage(`{"op":"read","path":"../../../etc/passwd"}`)); err == nil {
		t.Fatal("path traversal must be blocked")
	}
}

func TestHTTPPost(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte("created"))
	}))
	defer srv.Close()

	out, err := tools.NewHTTPPost().Invoke(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`","body":"{\"x\":1}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 201") || !strings.Contains(out, "created") {
		t.Fatalf("http_post result: %q", out)
	}
	if gotBody != `{"x":1}` {
		t.Fatalf("server received body %q", gotBody)
	}
}

func TestGitVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	g := tools.NewGit(sandbox.NewSubprocess())
	out, err := g.Invoke(context.Background(), json.RawMessage(`{"args":["--version"]}`))
	if err != nil {
		t.Fatalf("git --version: %v (%q)", err, out)
	}
	if !strings.Contains(out, "git version") {
		t.Fatalf("unexpected git output: %q", out)
	}
}

func echoArgv(s string) []string {
	if runtime.GOOS == "windows" {
		cs := os.Getenv("ComSpec")
		if cs == "" {
			cs = `C:\Windows\System32\cmd.exe`
		}
		return []string{cs, "/c", "echo " + s}
	}
	return []string{"sh", "-c", "printf %s " + s}
}
