// Package trace centralizes observability. It keeps two truth sources distinct:
// the durable event log is the business and replay truth (the data source for
// single-node replay), while OpenTelemetry is the performance and cross-service
// correlation view. run_id, node_id and attempt are the correlation IDs that
// thread trigger to engine to worker to tool.
//
// The slog logger here carries those correlation IDs; OTel spans live in
// otel.go. Exporting to a Jaeger/OTLP backend needs that backend running.
package trace

import (
	"log/slog"
	"os"
	"strings"

	"github.com/bytedance/sonic"
)

// Logger builds a text logger at the given level, writing to stderr.
func Logger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// Run returns a logger pre-tagged with the run correlation ID.
func Run(l *slog.Logger, runID string) *slog.Logger {
	return l.With("run_id", runID)
}

// Node returns a logger pre-tagged with the full correlation triple.
func Node(l *slog.Logger, runID, nodeID string, attempt int) *slog.Logger {
	return l.With("run_id", runID, "node_id", nodeID, "attempt", attempt)
}

// Sensitive-data redaction.

// RedactMask is the placeholder substituted for secret values.
const RedactMask = "***redacted***"

// secretKeyHints are case-insensitive substrings that mark a JSON key as secret.
var secretKeyHints = []string{
	"token", "secret", "password", "passwd", "apikey", "api_key",
	"authorization", "credential", "private_key", "access_key",
}

// Redact returns a copy of raw JSON with values under secret-looking keys masked.
//
// For the event log and traces only. Never apply it to operational step outputs
// (operation_outputs): downstream nodes resolve their inputs against those, and
// replay/recovery depends on them being the real recorded values.
func Redact(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := sonic.Unmarshal(raw, &v); err != nil {
		return raw // not JSON (our own structured data) → leave untouched
	}
	out, err := sonic.Marshal(redactValue(v))
	if err != nil {
		return raw
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if isSecretKey(k) {
				m[k] = RedactMask
			} else {
				m[k] = redactValue(val)
			}
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(e)
		}
		return out
	default:
		return v
	}
}

func isSecretKey(k string) bool {
	lk := strings.ToLower(k)
	for _, h := range secretKeyHints {
		if strings.Contains(lk, h) {
			return true
		}
	}
	return false
}
