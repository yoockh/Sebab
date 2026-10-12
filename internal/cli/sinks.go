package cli

import (
	"context"

	"github.com/yoockh/sebab/internal/engine"
)

// extraSinks returns platform sinks (desktop notifications) for an engine.
// The embedded engine behind a frontend adds none: the frontend displays the
// notifications it receives in the event stream.
func extraSinks(_ context.Context, _ *runtime, _ string) []engine.Sink { return nil }
