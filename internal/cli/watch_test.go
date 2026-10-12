package cli

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/event"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing command output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func testEnv() (Env, *syncBuffer, *syncBuffer) {
	out, errOut := &syncBuffer{}, &syncBuffer{}
	return Env{Stdin: strings.NewReader(""), Stdout: out, Stderr: errOut,
		Getenv: func(string) string { return "" }}, out, errOut
}

func TestWatchSpikeEmitsHelloAndHeartbeats(t *testing.T) {
	env, out, _ := testEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	if code := Run(ctx, []string{"watch", "--format", "jsonl", "--heartbeat", "1s"}, env); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("want hello + heartbeat, got %q", out.String())
	}
	h, err := event.Peek([]byte(lines[0]))
	if err != nil || h.Type != event.TypeHello {
		t.Fatalf("first line %q: %v", lines[0], err)
	}
	h, err = event.Peek([]byte(lines[1]))
	if err != nil || h.Type != event.TypeHeartbeat {
		t.Fatalf("second line %q: %v", lines[1], err)
	}
}

func TestUnknownCommand(t *testing.T) {
	env, _, errOut := testEnv()
	if code := Run(context.Background(), []string{"nope"}, env); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("stderr %q", errOut.String())
	}
}
