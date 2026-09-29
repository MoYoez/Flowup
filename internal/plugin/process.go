package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/moyoez/flowup/internal/action"
	"github.com/moyoez/flowup/internal/policy"
)

const maxOutputBytes = 1 << 20
const maxStderrBytes = 64 << 10

type Process struct{ binding Binding }

func (p *Process) Definition() action.Definition {
	s := p.binding.Spec
	timeout, _ := time.ParseDuration(s.Timeout)
	return action.Definition{Name: s.Name, InputSchema: s.InputSchema, OutputSchema: s.OutputSchema, SecretPaths: s.SecretPaths, Timeout: timeout}
}

func (p *Process) Effect(map[string]any) action.EffectClass { return p.binding.Spec.Effect }
func (p *Process) Prepare(map[string]any) error             { return p.binding.verify() }

func (p *Process) Execute(ctx context.Context, invocation action.Invocation) (action.Result, error) {
	if err := p.binding.verify(); err != nil {
		return action.Result{}, err
	}
	request := struct {
		ProtocolVersion int            `json:"protocol_version"`
		RunID           string         `json:"run_id"`
		StepID          string         `json:"step_id"`
		Attempt         int            `json:"attempt"`
		IdempotencyKey  string         `json:"idempotency_key,omitempty"`
		Input           map[string]any `json:"input"`
	}{1, invocation.RunID, invocation.StepID, invocation.Attempt, invocation.IdempotencyKey, invocation.Input}
	data, err := json.Marshal(request)
	if err != nil {
		return action.Result{}, fmt.Errorf("encode plugin request: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limitedOutput{limit: maxOutputBytes, cancel: cancel}
	stderr := &limitedOutput{limit: maxStderrBytes, cancel: cancel}
	cmd := exec.CommandContext(ctx, p.binding.Executable, p.binding.Spec.Command[1:]...)
	cmd.Dir = p.binding.Directory
	cmd.Env = p.environment()
	cmd.Stdin = bytes.NewReader(append(data, '\n'))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return action.Result{}, fmt.Errorf("plugin %q exceeded output limit (stdout 1 MiB, stderr 64 KiB)", p.binding.Spec.Name)
	}
	if ctx.Err() != nil {
		return action.Result{}, fmt.Errorf("plugin %q stopped: %w", p.binding.Spec.Name, ctx.Err())
	}
	if err != nil {
		return action.Result{}, fmt.Errorf("plugin %q process failed: %w (stderr withheld)", p.binding.Spec.Name, err)
	}
	var response struct {
		Output json.RawMessage `json:"output"`
		Error  *struct {
			Message   string `json:"message"`
			Transient bool   `json:"transient"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return action.Result{}, fmt.Errorf("plugin %q returned an invalid JSON response", p.binding.Spec.Name)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return action.Result{}, fmt.Errorf("plugin %q must return exactly one JSON response", p.binding.Spec.Name)
	}
	if (len(response.Output) > 0) == (response.Error != nil) {
		return action.Result{}, fmt.Errorf("plugin %q must return either output or error", p.binding.Spec.Name)
	}
	if response.Error != nil {
		message := p.redact(response.Error.Message, invocation.Input)
		err := fmt.Errorf("plugin %q: %s", p.binding.Spec.Name, message)
		if response.Error.Transient {
			return action.Result{}, action.Transient(err)
		}
		return action.Result{}, err
	}
	return action.Result{Output: response.Output}, nil
}

func (p *Process) environment() []string {
	names := append([]string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR", "PATHEXT"}, p.binding.Spec.Env...)
	seen := map[string]bool{}
	env := []string{}
	for _, name := range names {
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(name)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (p *Process) redact(message string, input map[string]any) string {
	secrets := policy.SecretValues(input, p.binding.Spec.SecretPaths)
	for _, name := range p.binding.Spec.Env {
		if value := os.Getenv(name); value != "" {
			secrets = append(secrets, value)
		}
	}
	return policy.RedactText(message, secrets)
}

type limitedOutput struct {
	bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *limitedOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.Len()
	if n > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.exceeded = true
		b.cancel()
	} else {
		_, _ = b.Buffer.Write(data)
	}
	return n, nil
}
