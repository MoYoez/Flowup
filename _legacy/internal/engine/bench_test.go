package engine_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
)

// instantDispatcher returns ok immediately, so the benchmark measures the
// engine + durable + SQLite store overhead per run (not node work) — the
// documented "single-store ceiling" concern.
type instantDispatcher struct{}

func (instantDispatcher) Dispatch(_ context.Context, task contracts.NodeTask) (contracts.NodeResult, error) {
	return contracts.NodeResult{RunID: task.RunID, NodeID: task.NodeID, Status: contracts.StatusOK, Output: []byte("{}")}, nil
}

const benchYAML = `pipeline: bench
version: 1
input_schema: { type: object }
nodes:
  - { id: a, kind: native, profile: { models: [m] }, inputs: {}, limits: {} }
  - { id: b, kind: native, needs: [a], profile: { models: [m] }, inputs: {}, limits: {} }
  - { id: c, kind: native, needs: [b], profile: { models: [m] }, inputs: {}, limits: {} }
output: {}
`

// BenchmarkInvokeThroughput drives 3-step runs through the engine + SQLite.
func BenchmarkInvokeThroughput(b *testing.B) {
	ctx := context.Background()
	st, err := store.NewSQLite(ctx, filepath.Join(b.TempDir(), "b.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	p, err := pipelines.LoadBytes([]byte(benchYAML))
	if err != nil {
		b.Fatal(err)
	}
	eng := engine.New(durable.NewSelfBuilt(st), instantDispatcher{}, st, nil)
	eng.Register(p)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := eng.Invoke(ctx, contracts.Invocation{
			PipelineID: "bench", Params: []byte("{}"), IdempotencyKey: fmt.Sprintf("b%d", i),
		})
		if err != nil || res.Status != contracts.StatusOK {
			b.Fatalf("invoke: %s/%s err=%v", res.Status, res.Reason, err)
		}
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "runs/s")
}
