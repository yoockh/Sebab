// Package filewatch calls back when a file changes, using inotify on the parent
// directory (so atomic rename-over writes are seen) with a polling fallback.
package filewatch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Watch blocks until ctx ends, calling onChange (debounced) after path changes.
func Watch(ctx context.Context, path string, debounce time.Duration, onChange func()) {
	events := make(chan struct{}, 1)
	notify := func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	if err := inotifyLoop(ctx, path, notify); err != nil {
		go pollLoop(ctx, path, 3*time.Second, notify)
	}
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-events:
			if timer == nil {
				timer = time.NewTimer(debounce)
			} else {
				timer.Reset(debounce)
			}
			fire = timer.C
		case <-fire:
			fire = nil
			onChange()
		}
	}
}

func inotifyLoop(ctx context.Context, path string, notify func()) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return err
	}
	mask := uint32(unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_CREATE | unix.IN_DELETE)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		unix.Close(fd)
		return err
	}
	// os.NewFile registers the non-blocking fd with the runtime poller, so
	// Close below interrupts a pending Read.
	f := os.NewFile(uintptr(fd), "inotify")
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	go func() {
		buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			for off := 0; off+unix.SizeofInotifyEvent <= n; {
				ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
				nameBytes := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
				if name := cString(nameBytes); name == base {
					notify()
				}
				off += unix.SizeofInotifyEvent + int(ev.Len)
			}
		}
	}()
	return nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func pollLoop(ctx context.Context, path string, every time.Duration, notify func()) {
	sig := func() string {
		st, err := os.Stat(path)
		if err != nil {
			return "missing"
		}
		return st.ModTime().String() + "/" + strconv.FormatInt(st.Size(), 10)
	}
	last := sig()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s := sig(); s != last {
				last = s
				notify()
			}
		}
	}
}
