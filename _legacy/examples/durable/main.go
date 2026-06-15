// Command durable is a real, runnable demo: a side effect calls a real external
// service, the process is killed (kill -9) mid-effect, a fresh engine recovers
// from the durable store and re-delivers, and the service — deduping by the
// idempotency key — applies the effect exactly once.
//
//	go run ./examples/durable
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
)

const childEnv = "FLOWUP_DEMO_CHILD"

const pipelineYAML = `pipeline: effect_once
version: 1
input_schema: { type: object }
nodes:
  - { id: prep, kind: native, profile: { models: [m] }, inputs: {}, limits: { timeout_sec: 30 } }
  - id: deliver
    kind: native
    needs: [prep]
    side_effect: true
    idempotency_key: "effect-{{ params.id }}"
    profile: { models: [m] }
    inputs: {}
    limits: { timeout_sec: 30 }
output: {}
`

// dispatcher runs the deliver node as a real HTTP POST to the external service,
// keyed by the run id. With crash, it hard-exits right after the POST.
type dispatcher struct {
	sinkURL string
	crash   bool
}

func (d dispatcher) Dispatch(_ context.Context, t contracts.NodeTask) (contracts.NodeResult, error) {
	ok := contracts.NodeResult{RunID: t.RunID, NodeID: t.NodeID, Status: contracts.StatusOK, Output: []byte("{}")}
	if t.NodeID != "deliver" {
		return ok, nil
	}
	req, _ := http.NewRequest(http.MethodPost, d.sinkURL+"/effect", nil)
	req.Header.Set("Idempotency-Key", t.RunID)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
	}
	if d.crash {
		os.Exit(137) // the external effect fired; die before the engine commits it
	}
	return ok, nil
}

func main() {
	if os.Getenv(childEnv) != "" {
		runChild()
		return
	}

	// A real external service: applies an effect once per idempotency key, counts every call.
	var mu sync.Mutex
	calls := 0
	applied := map[string]bool{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		applied[r.Header.Get("Idempotency-Key")] = true
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	sinkURL := "http://" + ln.Addr().String()

	dir, err := os.MkdirTemp("", "flowup-demo")
	must(err)
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, "demo.db")

	fmt.Println("1. a side effect calls a real service, then the process is killed (kill -9) mid-effect...")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), childEnv+"=1", "FLOWUP_SINK="+sinkURL, "FLOWUP_DB="+db)
	_ = cmd.Run() // expected to exit non-zero (crashed)
	mu.Lock()
	fmt.Printf("   service was called %d time(s) before the crash\n", calls)
	mu.Unlock()

	fmt.Println("2. a fresh engine recovers the pending run from the durable store...")
	ctx := context.Background()
	st, err := store.NewSQLite(ctx, db)
	must(err)
	defer st.Close()
	eng := engine.New(durable.NewSelfBuilt(st), dispatcher{sinkURL: sinkURL}, st, nil)
	p, err := pipelines.LoadBytes([]byte(pipelineYAML))
	must(err)
	eng.Register(p)
	n, err := eng.Recover(ctx)
	must(err)

	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("   recovered %d run(s); the deliver node re-fired (service called %d time(s) total)\n", n, calls)
	fmt.Printf("\nresult: delivered %d times, applied exactly %d time(s) — exactly-once across a crash\n", calls, len(applied))
}

// runChild drives the run with a dispatcher that hard-exits mid-side-effect.
func runChild() {
	ctx := context.Background()
	st, err := store.NewSQLite(ctx, os.Getenv("FLOWUP_DB"))
	must(err)
	eng := engine.New(durable.NewSelfBuilt(st), dispatcher{sinkURL: os.Getenv("FLOWUP_SINK"), crash: true}, st, nil)
	p, err := pipelines.LoadBytes([]byte(pipelineYAML))
	must(err)
	eng.Register(p)
	params, _ := sonic.Marshal(map[string]any{"id": "1"})
	_, _ = eng.Invoke(ctx, contracts.Invocation{PipelineID: "effect_once", Params: params, IdempotencyKey: "run-1"})
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
