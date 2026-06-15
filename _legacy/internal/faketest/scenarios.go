package faketest

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/moyoez/flowup/internal/contracts"
)

// Demo* handlers implement the deploy_repo_to_paas pipeline used by the demos.
// They are deliberately deterministic so the engine's behavior, not the model's,
// is what gets measured.

// DemoBuild returns a build handler that:
//   - embeds the current process PID in its output, so a build that is REPLAYED
//     after a crash carries the original process's PID (proving it was not
//     re-run in the recovered process); and
//   - fails the given attempt numbers with a retriable error, to drive
//     retry → slide-to-backup-model.
func DemoBuild(failAttempts ...int) NodeHandler {
	fail := make(map[int]bool, len(failAttempts))
	for _, a := range failAttempts {
		fail[a] = true
	}
	pid := os.Getpid()
	return func(task contracts.NodeTask) contracts.NodeResult {
		if fail[task.Attempt] {
			return Failed("build_transient", true)
		}
		return OK(map[string]any{
			"artifact_path": fmt.Sprintf("/artifacts/%d/app.tar", pid),
			"build_sha":     fmt.Sprintf("sha-%d", pid),
		})
	}
}

// DemoDeploy returns a side-effecting deploy handler. If requireHuman is true it
// returns needs_human until human input is present in the task inputs. counter,
// if non-nil, is incremented every time the side effect is actually performed —
// the test asserts it stays at 1 under idempotent replay.
func DemoDeploy(requireHuman bool, counter *int32) NodeHandler {
	pid := os.Getpid()
	return func(task contracts.NodeTask) contracts.NodeResult {
		if requireHuman && !hasHumanInput(task) {
			return NeedsHuman("确认在目标环境部署并接受费用,然后回传以续跑")
		}
		if counter != nil {
			atomic.AddInt32(counter, 1)
		}
		return OK(map[string]any{
			"deployment_id": fmt.Sprintf("dep-%d", pid),
			"url":           "https://staging.example.com/app",
		})
	}
}

// DemoVerify returns a verify handler producing a healthy result.
func DemoVerify() NodeHandler {
	return func(task contracts.NodeTask) contracts.NodeResult {
		return OK(map[string]any{"healthy": true, "status_code": 200})
	}
}

func hasHumanInput(task contracts.NodeTask) bool {
	if len(task.Inputs) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if err := sonic.Unmarshal(task.Inputs, &m); err != nil {
		return false
	}
	_, ok := m["_human"]
	return ok
}
