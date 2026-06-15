// Command engine is the push-layer service. It exposes the three-state contract
// over HTTP (Fiber), enqueues node work onto the durable queue for workers to
// pull, recovers crashed runs on startup, and serves a small dashboard.
//
//	go run ./cmd/engine --db flowup.db --addr :8080 --pipelines ./examples
//
// Run one or more cmd/worker against the same --db to execute nodes.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"

	"github.com/moyoez/flowup/internal/contracts"
	"github.com/moyoez/flowup/internal/dispatch"
	"github.com/moyoez/flowup/internal/durable"
	"github.com/moyoez/flowup/internal/engine"
	"github.com/moyoez/flowup/internal/pipelines"
	"github.com/moyoez/flowup/internal/store"
	"github.com/moyoez/flowup/internal/trace"
)

func main() {
	var db, addr, pdir string
	flag.StringVar(&db, "db", "flowup.db", "shared store: SQLite file, or a postgres:// DSN (needs -tags postgres)")
	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&pdir, "pipelines", "", "directory of *.pipeline.yaml to register (plus the built-in)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := trace.Logger(slog.LevelInfo)
	if shutdown, err := trace.InitFromEnv(); err == nil {
		defer func() { _ = shutdown(context.Background()) }()
	}

	st, err := store.Open(ctx, db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	defer st.Close()

	eng := engine.New(durable.NewSelfBuilt(st), dispatch.NewQueueDispatcher(st), st, log)
	if pdir != "" {
		ps, err := pipelines.LoadDir(pdir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load pipelines:", err)
			os.Exit(1)
		}
		for _, p := range ps {
			eng.Register(p)
		}
	}
	if n, err := eng.Recover(ctx); err == nil && n > 0 {
		log.Info("recovered crashed runs", "count", n)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	routes(app, eng, st)

	go func() {
		log.Info("engine listening", "addr", addr, "pipelines", eng.Registered())
		if err := app.Listen(addr); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	_ = app.ShutdownWithContext(context.Background())
	log.Info("engine stopped")
}

func routes(app *fiber.App, eng *engine.Engine, st store.Store) {
	app.Get("/healthz", func(c *fiber.Ctx) error { return c.SendString("ok") })

	app.Get("/pipelines", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"pipelines": eng.Registered()})
	})

	app.Post("/invoke", func(c *fiber.Ctx) error {
		var inv contracts.Invocation
		if err := sonic.Unmarshal(c.Body(), &inv); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "bad Invocation JSON"})
		}
		res, _ := eng.Invoke(c.Context(), inv)
		return c.Status(statusCode(res)).JSON(res)
	})

	// Async: start a run in the background and return immediately. The client
	// resolves the run_id via /run-by-key and watches /runs/:id/events?follow=1.
	app.Post("/runs", func(c *fiber.Ctx) error {
		var inv contracts.Invocation
		if err := sonic.Unmarshal(c.Body(), &inv); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "bad Invocation JSON"})
		}
		if inv.IdempotencyKey == "" {
			inv.IdempotencyKey = "async-" + randHex()
		}
		go func() { _, _ = eng.Invoke(context.Background(), inv) }()
		return c.Status(202).JSON(fiber.Map{"idempotency_key": inv.IdempotencyKey, "status": "accepted"})
	})

	app.Get("/run-by-key/:key", func(c *fiber.Ctx) error {
		rec, ok, err := st.GetRunByIdempotencyKey(c.Context(), c.Params("key"))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "store error"})
		}
		if !ok {
			return c.Status(404).JSON(fiber.Map{"error": "run not created yet"})
		}
		return c.JSON(fiber.Map{"run_id": rec.RunID, "status": rec.Status, "reason": rec.Reason})
	})

	app.Get("/runs/:id", func(c *fiber.Ctx) error {
		rec, ok, err := st.GetRun(c.Context(), c.Params("id"))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "store error"})
		}
		if !ok {
			return c.Status(404).JSON(fiber.Map{"error": "no such run"})
		}
		return c.JSON(fiber.Map{
			"run_id": rec.RunID, "pipeline_id": rec.PipelineID, "status": rec.Status,
			"reason": rec.Reason, "resume_token": rec.ResumeToken,
		})
	})

	app.Get("/runs/:id/events", func(c *fiber.Ctx) error {
		id := c.Params("id")
		if c.Query("follow") == "" {
			evs, err := st.ListEvents(c.Context(), id)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "store error"})
			}
			out := make([]fiber.Map, 0, len(evs))
			for _, e := range evs {
				out = append(out, fiber.Map{"seq": e.ID, "type": e.Type, "node_id": e.NodeID, "attempt": e.Attempt, "data": json.RawMessage(e.Data)})
			}
			return c.JSON(fiber.Map{"run_id": id, "events": out})
		}
		return streamEvents(c, st, id)
	})

	app.Post("/resume", func(c *fiber.Ctx) error {
		var body struct {
			ResumeToken string          `json:"resume_token"`
			HumanInput  json.RawMessage `json:"human_input"`
		}
		if err := sonic.Unmarshal(c.Body(), &body); err != nil || body.ResumeToken == "" {
			return c.Status(400).JSON(fiber.Map{"error": "need resume_token"})
		}
		res, _ := eng.Resume(c.Context(), body.ResumeToken, body.HumanInput)
		return c.Status(statusCode(res)).JSON(res)
	})

	app.Get("/", func(c *fiber.Ctx) error {
		c.Type("html")
		return c.SendString(dashboardHTML)
	})
}

// streamEvents pushes new run events as Server-Sent Events until the run is
// terminal or the client disconnects.
func streamEvents(c *fiber.Ctx, st store.Store, id string) error {
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		seen := 0
		for i := 0; i < 2400; i++ { // ~10 min cap at 250ms
			evs, err := st.ListEvents(context.Background(), id)
			if err == nil {
				for _, e := range evs[seen:] {
					data, _ := sonic.Marshal(fiber.Map{"type": e.Type, "node_id": e.NodeID, "attempt": e.Attempt, "data": json.RawMessage(e.Data)})
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
				seen = len(evs)
				if w.Flush() != nil {
					return // client gone
				}
				if rec, ok, _ := st.GetRun(context.Background(), id); ok && terminal(rec.Status) {
					fmt.Fprintf(w, "event: done\ndata: %q\n\n", rec.Status)
					_ = w.Flush()
					return
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	return nil
}

func statusCode(res contracts.InvocationResult) int {
	if res.Status == contracts.StatusFailed {
		return 422 // typed failure, not an HTTP error
	}
	return 200 // ok | needs_human
}

func terminal(status string) bool {
	return status == string(contracts.StatusOK) ||
		status == string(contracts.StatusFailed) ||
		status == string(contracts.StatusNeedsHuman)
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const dashboardHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Flowup</title>
<style>body{font:14px system-ui,sans-serif;margin:2rem;max-width:60rem}
input,button,textarea{font:inherit;padding:.3rem}#ev{background:#111;color:#0f0;padding:1rem;border-radius:6px;height:22rem;overflow:auto;white-space:pre-wrap}
.row{margin:.4rem 0}code{background:#eee;padding:.1rem .3rem;border-radius:3px}</style></head><body>
<h2>Flowup</h2>
<div class=row>pipeline: <input id=pid size=24 placeholder="pipeline_id"> params(JSON): <input id=params size=24 value="{}"> <button onclick=start()>Run</button></div>
<div class=row>or watch a run_id: <input id=rid size=26> <button onclick=stream(document.getElementById('rid').value)>Stream</button></div>
<div class=row><b>events</b> <span id=st></span></div>
<div id=ev></div>
<p>pipelines: <code id=pl></code></p>
<script>
const ev=document.getElementById('ev'),stt=document.getElementById('st')
function log(s){ev.textContent+=s+"\n";ev.scrollTop=ev.scrollHeight}
fetch('/pipelines').then(r=>r.json()).then(j=>document.getElementById('pl').textContent=(j.pipelines||[]).join(', '))
function stream(id){if(!id)return;ev.textContent='';stt.textContent='('+id+')';const es=new EventSource('/runs/'+id+'/events?follow=1')
 es.onmessage=e=>{const d=JSON.parse(e.data);log('• '+d.type+(d.node_id?' ['+d.node_id+']':'')+' '+JSON.stringify(d.data))}
 es.addEventListener('done',e=>{log('— done: '+e.data);stt.textContent+=' DONE';es.close()})
 es.onerror=()=>{es.close()}}
async function start(){const pid=document.getElementById('pid').value;let p={};try{p=JSON.parse(document.getElementById('params').value||'{}')}catch(e){alert('bad params JSON');return}
 const r=await fetch('/runs',{method:'POST',body:JSON.stringify({pipeline_id:pid,params:p})});const j=await r.json();stt.textContent='accepted key='+j.idempotency_key
 for(let i=0;i<40;i++){const rr=await fetch('/run-by-key/'+j.idempotency_key);if(rr.ok){const rj=await rr.json();document.getElementById('rid').value=rj.run_id;stream(rj.run_id);return}await new Promise(s=>setTimeout(s,250))}}
</script></body></html>`
