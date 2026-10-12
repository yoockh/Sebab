package cli

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yoockh/sebab/internal/server"
	"github.com/yoockh/sebab/internal/tui"
)

// runTUI shows monitors in the terminal. Like `watch`, it attaches to a
// running engine or runs an embedded one; the TUI does not raise desktop
// notifications itself, so the engine does.
func runTUI(ctx context.Context, env Env, args []string) int {
	fs := newFlagSet("tui", env)
	embedded := fs.Bool("embedded", false, "always run an embedded engine, never attach to a daemon")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	in, ok := env.Stdin.(*os.File)
	if !ok {
		return usageError(env, "tui: needs an interactive terminal")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	term, err := tui.OpenTerminal(in, env.Stdout)
	if err != nil {
		return usageError(env, "tui: %v", err)
	}
	defer term.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	model := tui.NewModel()
	ui := &tuiState{model: model, term: term, style: tui.ANSI}
	if env.Getenv("NO_COLOR") != "" {
		ui.style = tui.Plain
	}
	feed := &lineFeeder{apply: func(l []byte) { model.Apply(l); ui.redraw() }}
	sock := rt.paths.SocketPath()
	go func() {
		if !*embedded && sock != "" && server.Alive(sock) {
			if err := server.Subscribe(ctx, sock, feed, false); err == nil || ctx.Err() != nil {
				return
			}
		}
		runEngine(ctx, rt, feed, "embedded", sock, false)
		cancel()
	}()

	keys := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := in.Read(buf)
			if err != nil {
				cancel()
				return
			}
			keys <- append([]byte(nil), buf[:n]...)
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	ui.redraw()
	for {
		select {
		case <-ctx.Done():
			return ExitOK
		case <-tick.C:
			ui.redraw()
		case <-winch:
			ui.redraw()
		case b := <-keys:
			for _, k := range tui.DecodeKeys(b) {
				if k == tui.KeyQuit {
					return ExitOK
				}
				ui.handle(ctx, k, sock)
			}
			ui.redraw()
		}
	}
}

type tuiState struct {
	mu       sync.Mutex
	model    *tui.Model
	term     *tui.Terminal
	style    tui.Style
	selected int
	expanded bool
	message  string
}

func (s *tuiState) handle(ctx context.Context, k tui.Key, sock string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.model.IDs())
	switch k {
	case tui.KeyUp:
		s.selected = max(0, s.selected-1)
	case tui.KeyDown:
		s.selected = min(max(n-1, 0), s.selected+1)
	case tui.KeyEnter:
		s.expanded = !s.expanded
	case tui.KeyRefresh:
		s.message = "checking now..."
		go func() {
			if _, err := server.Call(ctx, sock, server.Request{Cmd: "check"}, 2*time.Second); err != nil {
				s.mu.Lock()
				s.message = "refresh failed: " + err.Error()
				s.mu.Unlock()
			}
		}()
	}
}

func (s *tuiState) redraw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.term.Size()
	s.term.Draw(s.model.Render(tui.View{Width: w, Height: h, Selected: s.selected, Expanded: s.expanded,
		Now: time.Now(), Style: s.style, Message: s.message}))
}

// lineFeeder splits a byte stream into event lines.
type lineFeeder struct {
	mu    sync.Mutex
	buf   []byte
	apply func([]byte)
}

func (f *lineFeeder) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		f.apply(append([]byte(nil), f.buf[:i]...))
		f.buf = f.buf[i+1:]
	}
}
