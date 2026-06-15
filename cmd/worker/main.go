// Command worker is a standalone execution-layer daemon. It connects to the
// shared store, advertises its capabilities, and pulls matching NodeTasks off
// the durable queue — running each via the Router (native → sandbox; semantic/
// agent → model+tools). Multiple workers can run against one store; a dead
// worker's lease is reclaimed by the reaper.
//
//	go run ./cmd/worker --db flowup.db --caps os
//	# container backend + real model:
//	FLOWUP_MODEL=anthropic go run ./cmd/worker --db flowup.db --image alpine --engine podman
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/dispatch"
	"github.com/moyoez/flowup/internal/model"
	"github.com/moyoez/flowup/internal/runner"
	"github.com/moyoez/flowup/internal/sandbox"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/trace"
)

func main() {
	var db, id, caps, image, engine string
	flag.StringVar(&db, "db", "flowup.db", "shared store: SQLite file, or a postgres:// DSN (needs -tags postgres)")
	flag.StringVar(&id, "id", "", "worker id (default: host-pid)")
	flag.StringVar(&caps, "caps", "os", "comma-separated capabilities this worker offers")
	flag.StringVar(&image, "image", "", "container image (empty = subprocess backend)")
	flag.StringVar(&engine, "engine", "podman", "container engine (podman|docker)")
	flag.Parse()

	if id == "" {
		host, _ := os.Hostname()
		id = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if shutdown, err := trace.InitFromEnv(); err == nil {
		defer func() { _ = shutdown(context.Background()) }()
	}

	st, err := store.Open(ctx, db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	defer st.Close()

	var backend sandbox.Backend
	if image != "" {
		backend = sandbox.NewContainer(engine, image)
	} else {
		backend = sandbox.NewSubprocess()
	}
	rt := runner.NewRouter(backend, model.New(), runner.DefaultTools(backend))
	// Relay live answer tokens to the event log so the engine's SSE streams them.
	rt.SetTokenSink(func(runID, nodeID, chunk string) {
		data, _ := sonic.Marshal(map[string]string{"text": chunk})
		_ = st.AppendEvent(ctx, store.EventRecord{RunID: runID, NodeID: nodeID, Type: "node_token", Data: data})
	})

	w := dispatch.NewWorker(id, splitCaps(caps), st, rt)
	go dispatch.Reap(ctx, st, 2*time.Second)

	fmt.Printf("worker %s up: caps=%v backend=%s model=%s db=%s\n", id, splitCaps(caps), backend.Name(), modelName(), db)
	_ = w.Run(ctx) // blocks until SIGINT/SIGTERM
	fmt.Println("worker stopped")
}

func splitCaps(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func modelName() string {
	if mc := model.New(); mc != nil {
		return mc.Name()
	}
	return "none"
}
