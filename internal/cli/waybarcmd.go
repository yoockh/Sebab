package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/server"
	"github.com/yoockh/sebab/internal/waybar"
)

// runWaybar prints Waybar custom-module JSON, one line per state change. It
// attaches to a running engine or runs an embedded one. Waybar does not display
// Sebab's notifications, so the engine sends desktop notifications itself.
func runWaybar(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("waybar", env)
	embedded := fs.Bool("embedded", false, "always run an embedded engine, never attach to a daemon")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	agg := waybar.NewAggregator(nil)
	ww := &waybarWriter{agg: agg, out: &guardedWriter{w: env.Stdout, onErr: cancel}}
	ww.print(agg.Render())
	go ww.tick(ctx)
	sock := rt.paths.SocketPath()
	if !*embedded && sock != "" && server.Alive(sock) {
		if err := server.Subscribe(ctx, sock, ww, false); err == nil || ctx.Err() != nil {
			return ExitOK
		}
		if out, changed := agg.SetStalled(); changed {
			ww.print(out)
		}
	}
	return runEngine(ctx, rt, ww, "embedded", sock, false)
}

// waybarWriter receives event lines and prints Waybar JSON on change.
type waybarWriter struct {
	mu  sync.Mutex
	agg *waybar.Aggregator
	out io.Writer
	buf []byte
}

func (w *waybarWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	var lines [][]byte
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, append([]byte(nil), w.buf[:i]...))
		w.buf = w.buf[i+1:]
	}
	w.mu.Unlock()
	for _, l := range lines {
		if out, changed := w.agg.Apply(l); changed {
			if err := w.print(out); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

func (w *waybarWriter) print(o waybar.Output) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w.out, string(b))
	return err
}

func (w *waybarWriter) tick(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if out, changed := w.agg.Tick(); changed {
				_ = w.print(out)
			}
		}
	}
}
