//go:build !otel

package trace

import "context"

// This is the default (no-tracing) build. Spans are no-ops with zero dependency
// on the OpenTelemetry SDK. Build with -tags otel to emit real spans.

// EndFunc finishes a span, recording the three-state outcome.
type EndFunc func(status, reason string)

// InitFromEnv is a no-op without -tags otel.
func InitFromEnv() (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

func StartRun(ctx context.Context, _ string) (context.Context, EndFunc) {
	return ctx, func(string, string) {}
}

func StartNode(ctx context.Context, _, _ string) (context.Context, EndFunc) {
	return ctx, func(string, string) {}
}
