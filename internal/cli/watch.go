package cli

import (
	"context"
	"time"

	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/version"
)

func runWatch(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("watch", env)
	format := fs.String("format", "jsonl", "output format: jsonl")
	heartbeat := fs.Duration("heartbeat", 15*time.Second, "heartbeat interval")
	_ = fs.String("session", "", "opaque session token used by frontends to find this process")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *format != "jsonl" {
		return usageError(env, "watch: unsupported format %q", *format)
	}
	if *heartbeat < time.Second {
		return usageError(env, "watch: heartbeat must be at least 1s")
	}
	enc := event.NewEncoder(env.Stdout, nil)
	if err := enc.Encode(event.NewHello(time.Now(), version.Version, 0, *heartbeat, "embedded")); err != nil {
		return ExitInternal
	}
	ticker := time.NewTicker(*heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ExitOK
		case now := <-ticker.C:
			if err := enc.Encode(event.NewHeartbeat(now)); err != nil {
				// The reader went away (broken pipe): exit quietly.
				return ExitOK
			}
		}
	}
}
