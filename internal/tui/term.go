package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Terminal puts a tty into raw mode on the alternate screen and restores it.
type Terminal struct {
	in    *os.File
	out   io.Writer
	saved *unix.Termios
}

// OpenTerminal switches in to raw mode. It fails when in is not a terminal.
func OpenTerminal(in *os.File, out io.Writer) (*Terminal, error) {
	fd := int(in.Fd())
	saved, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("not a terminal: %w", err)
	}
	raw := *saved
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, fmt.Errorf("raw mode: %w", err)
	}
	t := &Terminal{in: in, out: out, saved: saved}
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	return t, nil
}

// Size returns the terminal width and height (80x24 when unknown).
func (t *Terminal) Size() (int, int) {
	ws, err := unix.IoctlGetWinsize(int(t.in.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// Draw replaces the screen content.
func (t *Terminal) Draw(frame string) {
	var b bytes.Buffer
	b.WriteString("\x1b[H\x1b[2J")
	b.WriteString(bytes.NewBufferString(frame).String())
	// Raw mode disables output post-processing: turn \n into \r\n.
	_, _ = t.out.Write(bytes.ReplaceAll(b.Bytes(), []byte("\n"), []byte("\r\n")))
}

// Close restores the terminal.
func (t *Terminal) Close() {
	fmt.Fprint(t.out, "\x1b[?25h\x1b[?1049l")
	_ = unix.IoctlSetTermios(int(t.in.Fd()), unix.TCSETS, t.saved)
}

// Key is a decoded key press.
type Key int

// Keys the TUI understands.
const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyEnter
	KeyRefresh
	KeyQuit
)

// DecodeKeys turns raw input bytes into keys.
func DecodeKeys(b []byte) []Key {
	var keys []Key
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b && i+2 < len(b) && b[i+1] == '[':
			switch b[i+2] {
			case 'A':
				keys = append(keys, KeyUp)
			case 'B':
				keys = append(keys, KeyDown)
			}
			i += 2
		case c == 'k':
			keys = append(keys, KeyUp)
		case c == 'j':
			keys = append(keys, KeyDown)
		case c == '\r' || c == '\n' || c == ' ':
			keys = append(keys, KeyEnter)
		case c == 'r':
			keys = append(keys, KeyRefresh)
		case c == 'q' || c == 3 || c == 4: // q, Ctrl+C, Ctrl+D
			keys = append(keys, KeyQuit)
		}
	}
	return keys
}
