package engine

import (
	"context"

	"github.com/moyoez/flowup/internal/contracts"
)

// Dispatcher sends a NodeTask to a capability-matched Worker and returns its
// three-state NodeResult. It can be an in-process worker or a real transport
// (a Postgres-backed queue with leases, heartbeats, visibility timeouts and
// capability routing). Keeping it behind this interface lets the engine stay
// unaware of how nodes actually run.
type Dispatcher interface {
	Dispatch(ctx context.Context, task contracts.NodeTask) (contracts.NodeResult, error)
}
