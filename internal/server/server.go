// Package server exposes a running engine on a Unix socket in $XDG_RUNTIME_DIR so
// several frontends share one engine (no double probing). The protocol is JSON
// lines: the client sends one request object, the server answers with one
// response object, or, for "subscribe", with a stream of events.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// Request is a client command.
type Request struct {
	Cmd     string `json:"cmd"` // subscribe, check, reload, report, status, ping
	Monitor string `json:"monitor,omitempty"`
}

// Response answers non-streaming commands.
type Response struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Source   string `json:"source,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

// Backend is what the server needs from the engine.
type Backend interface {
	Snapshot(ctx context.Context) []any
	Trigger(id string)
	Reload() error
	Report(ctx context.Context, id string) (string, error)
}

// Broadcaster fans events out to subscribers. It implements engine.Sink.
// Slow subscribers are dropped rather than blocking the engine.
type Broadcaster struct {
	mu    sync.Mutex
	subs  map[chan []byte]struct{}
	scrub event.Scrubber
}

// NewBroadcaster returns a broadcaster that scrubs every line with scrub.
func NewBroadcaster(scrub event.Scrubber) *Broadcaster {
	return &Broadcaster{subs: map[chan []byte]struct{}{}, scrub: scrub}
}

// Emit implements engine.Sink.
func (b *Broadcaster) Emit(ev any) {
	line, err := event.Marshal(ev, b.scrub)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- line:
		default:
			delete(b.subs, ch) // too slow: the client reconnects and gets a fresh snapshot
			close(ch)
		}
	}
}

func (b *Broadcaster) subscribe() chan []byte {
	ch := make(chan []byte, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broadcaster) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

// Server serves one engine on a Unix socket.
type Server struct {
	Path    string
	Backend Backend
	Events  *Broadcaster
	Scrub   event.Scrubber
	Version string
	Source  string

	ln net.Listener
	wg sync.WaitGroup
}

// ErrInUse means another live engine already owns the socket.
var ErrInUse = errors.New("another sebab engine is already serving the socket")

// Listen creates the socket (0600 inside a 0700 directory). A stale socket left
// by a crashed engine is removed; a live one returns ErrInUse.
func (s *Server) Listen() error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod runtime dir: %w", err)
	}
	if Alive(s.Path) {
		return ErrInUse
	}
	_ = os.Remove(s.Path)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", s.Path)
	syscall.Umask(old)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.Path, err)
	}
	s.ln = ln
	return nil
}

// Serve accepts connections until ctx ends, then removes the socket.
func (s *Server) Serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			break
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, conn)
		}()
	}
	s.wg.Wait()
	_ = os.Remove(s.Path)
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if !samePeer(conn) {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(conn, Response{Error: "bad request"})
		return
	}
	switch req.Cmd {
	case "subscribe":
		s.stream(ctx, conn)
	case "check":
		s.Backend.Trigger(req.Monitor)
		s.reply(conn, Response{OK: true})
	case "reload":
		if err := s.Backend.Reload(); err != nil {
			s.reply(conn, Response{Error: err.Error()})
			return
		}
		s.reply(conn, Response{OK: true})
	case "report":
		md, err := s.Backend.Report(ctx, req.Monitor)
		if err != nil {
			s.reply(conn, Response{Error: err.Error()})
			return
		}
		s.reply(conn, Response{OK: true, Markdown: md})
	case "status", "ping":
		s.reply(conn, Response{OK: true, Engine: s.Version, Source: s.Source, PID: os.Getpid()})
	default:
		s.reply(conn, Response{Error: "unknown command"})
	}
}

func (s *Server) reply(conn net.Conn, r Response) {
	line, err := event.Marshal(r, s.Scrub)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write(append(line, '\n'))
}

// stream sends the snapshot, then live events, until the client goes away.
func (s *Server) stream(ctx context.Context, conn net.Conn) {
	ch := s.Events.subscribe()
	defer s.Events.unsubscribe(ch)
	w := bufio.NewWriter(conn)
	write := func(line []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := w.Write(append(line, '\n')); err != nil {
			return false
		}
		return w.Flush() == nil
	}
	for _, ev := range s.Backend.Snapshot(ctx) {
		line, err := event.Marshal(ev, s.Scrub)
		if err != nil || !write(line) {
			return
		}
	}
	// Detect client hang-up even when no events flow.
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(gone)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case line, ok := <-ch:
			if !ok || !write(line) {
				return
			}
		}
	}
}

// samePeer checks SO_PEERCRED so only the socket owner's processes are served.
func samePeer(conn net.Conn) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var credErr error
	_ = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return credErr == nil && cred != nil && int(cred.Uid) == os.Getuid()
}

// Alive reports whether a live engine answers on path.
func Alive(path string) bool {
	if path == "" {
		return false
	}
	resp, err := Call(context.Background(), path, Request{Cmd: "ping"}, time.Second)
	return err == nil && resp.OK
}

// Call sends one request and reads one response.
func Call(ctx context.Context, path string, req Request, timeout time.Duration) (Response, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("connect to engine: %w", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	data, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(data) == 0 {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Subscribe connects and copies the event stream to w until ctx ends or the
// connection drops.
func Subscribe(ctx context.Context, path string, w io.Writer) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("connect to engine: %w", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	if _, err := conn.Write([]byte(`{"cmd":"subscribe"}` + "\n")); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	_, err = io.Copy(w, conn)
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = io.EOF
	}
	return fmt.Errorf("engine stream ended: %w", err)
}
