package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPluginBindingsPersistAndStayImmutable(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "plugins.db"))
	run := RunRecord{ID: "with-plugin", WorkflowName: "test", WorkflowVersion: 1, WorkflowYAML: []byte("workflow"), Inputs: json.RawMessage(`{}`), PluginBindings: json.RawMessage(`{"version":1,"bindings":[]}`), Status: RunRunning, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, s.CreateRun(context.Background(), run))
	run.PluginBindings = nil
	run.Status = RunSucceeded
	require.NoError(t, s.UpdateRun(context.Background(), run))
	got, err := s.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"bindings":[]}`, string(got.PluginBindings))
}

func TestPluginMigrationPreservesExistingRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := OpenSQLite(path)
	require.NoError(t, err)
	run := RunRecord{ID: "old-run", WorkflowName: "old", WorkflowVersion: 1, WorkflowYAML: []byte("workflow"), Inputs: json.RawMessage(`{}`), Status: RunSucceeded, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, s.CreateRun(context.Background(), run))
	_, err = s.db.Exec("ALTER TABLE runs DROP COLUMN plugin_bindings_json")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s = openTestStore(t, path)
	got, err := s.GetRun(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, RunSucceeded, got.Status)
	require.Empty(t, got.PluginBindings)
}
