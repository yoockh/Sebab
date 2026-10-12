package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Scrubber removes secrets from an encoded line. It is the last line of defence:
// event builders already avoid secrets, and the encoder scrubs every byte it writes.
type Scrubber interface {
	ScrubBytes([]byte) []byte
}

// Encoder writes events as JSON lines. It is safe for concurrent use.
type Encoder struct {
	mu    sync.Mutex
	w     io.Writer
	scrub Scrubber
}

// NewEncoder returns a JSON-lines encoder. scrub may be nil.
func NewEncoder(w io.Writer, scrub Scrubber) *Encoder {
	return &Encoder{w: w, scrub: scrub}
}

// Marshal encodes one event as a single line without the trailing newline.
func Marshal(ev any, scrub Scrubber) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	line := bytes.TrimRight(buf.Bytes(), "\n")
	if scrub != nil {
		line = scrub.ScrubBytes(line)
	}
	return line, nil
}

// Encode writes one event followed by a newline.
func (e *Encoder) Encode(ev any) error {
	line, err := Marshal(ev, e.scrub)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}
