//go:build postgres

package pgtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// dbosBuildCalls counts executions of the (non-deterministic) build step, so we
// can prove DBOS memoizes it — the same property our self-built engine gives.
var dbosBuildCalls int32

func dbosBuild(ctx context.Context) (string, error) {
	n := atomic.AddInt32(&dbosBuildCalls, 1)
	return "artifact-v" + strconv.FormatInt(int64(n), 10), nil
}

// dbosDeployWorkflow mirrors build→deploy with durable steps.
func dbosDeployWorkflow(ctx dbos.DBOSContext, input string) (string, error) {
	art, err := dbos.RunAsStep(ctx, dbosBuild)
	if err != nil {
		return "", err
	}
	return dbos.RunAsStep(ctx, func(c context.Context) (string, error) {
		return "deployed:" + art, nil
	})
}

// TestDBOSArm is the DBOS-Go control arm: it runs the same kind of durable
// workflow (steps + idempotency) on real Postgres, showing DBOS delivers step
// memoization and exactly-once natively — the comparison point for the
// self-built arm validated in pg_test.go.
func TestDBOSArm(t *testing.T) {
	dctx, err := dbos.NewDBOSContext(context.Background(), dbos.Config{
		AppName:     "flowup-dbos-spike",
		DatabaseURL: "postgres://flowup:flowup@localhost:55434/flowup?sslmode=disable",
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatal(err)
	}
	dbos.RegisterWorkflow(dctx, dbosDeployWorkflow)
	if err := dbos.Launch(dctx); err != nil {
		t.Fatal(err)
	}
	defer dbos.Shutdown(dctx, 10*time.Second)

	// Reset AFTER Launch so any recovery pass doesn't skew the count.
	atomic.StoreInt32(&dbosBuildCalls, 0)

	// Unique per run (embedded-postgres persists its data dir across runs).
	wfID := fmt.Sprintf("dbos-spike-%d", time.Now().UnixNano())

	h1, err := dbos.RunWorkflow(dctx, dbosDeployWorkflow, "in", dbos.WithWorkflowID(wfID))
	if err != nil {
		t.Fatal(err)
	}
	r1, err := h1.GetResult()
	if err != nil {
		t.Fatal(err)
	}

	// Same workflow ID again → recorded result, build step NOT re-run (idempotency).
	h2, err := dbos.RunWorkflow(dctx, dbosDeployWorkflow, "in", dbos.WithWorkflowID(wfID))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := h2.GetResult()
	if err != nil {
		t.Fatal(err)
	}

	if r1 != r2 || r1 != "deployed:artifact-v1" {
		t.Fatalf("idempotent re-run mismatch: r1=%q r2=%q", r1, r2)
	}
	if got := atomic.LoadInt32(&dbosBuildCalls); got != 1 {
		t.Fatalf("DBOS must memoize the build step (run once), ran %d times", got)
	}
	t.Logf("DBOS arm OK on Postgres: result=%q, build step memoized (executed exactly once)", r1)
}
