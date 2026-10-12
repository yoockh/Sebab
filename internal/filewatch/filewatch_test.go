package filewatch

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchSeesWritesAndRenames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Watch(ctx, path, 20*time.Millisecond, func() { n.Add(1) }); close(done) }()
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(path, []byte("a"), 0o600)
	waitFor(t, func() bool { return n.Load() >= 1 })
	tmp := filepath.Join(dir, ".tmp")
	_ = os.WriteFile(tmp, []byte("b"), 0o600)
	_ = os.Rename(tmp, path)
	waitFor(t, func() bool { return n.Load() >= 2 })
	before := n.Load()
	_ = os.WriteFile(filepath.Join(dir, "other"), []byte("x"), 0o600)
	time.Sleep(100 * time.Millisecond)
	if n.Load() != before {
		t.Error("changes to other files must be ignored")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop")
	}
}

func TestPollFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c")
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollLoop(ctx, path, 10*time.Millisecond, func() { n.Add(1) })
	time.Sleep(30 * time.Millisecond)
	_ = os.WriteFile(path, []byte("x"), 0o600)
	waitFor(t, func() bool { return n.Load() >= 1 })
}
