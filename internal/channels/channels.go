// Package channels delivers engine notifications to external services:
// a generic JSON webhook, ntfy and Telegram. They are opt-in ([[notify]] in
// config.toml) because they send monitor names and causes to a third party.
// Monitor targets are never included in the payload.
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/secret"
)

// Message is what a channel sends.
type Message struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Urgency  string `json:"urgency"`
	Monitor  string `json:"monitor,omitempty"`
	Status   string `json:"status,omitempty"`
	Code     string `json:"code,omitempty"`
	Side     string `json:"side,omitempty"`
	Kind     string `json:"kind,omitempty"` // notice kind for engine-level messages
	Time     string `json:"time"`
	ID       string `json:"id"`
	Source   string `json:"source"`
	Version  int    `json:"v"`
	Severity string `json:"-"`
}

// Resolver looks up secret references.
type Resolver interface {
	Resolve(ref string) (string, string, error)
}

// Dispatcher sends notify events to the configured channels. It implements
// engine.Sink and never blocks the engine.
type Dispatcher struct {
	Client   *http.Client
	Secrets  Resolver
	Redactor *secret.Redactor
	// OnError reports a delivery problem (already scrubbed).
	OnError func(msg string)

	mu       sync.Mutex
	channels []config.Channel
	queue    chan Message
	failing  map[int]bool
}

// NewDispatcher starts the delivery goroutine; it stops when ctx ends.
func NewDispatcher(ctx context.Context, chans []config.Channel, secrets Resolver, red *secret.Redactor,
	onError func(string)) *Dispatcher {
	d := &Dispatcher{Client: &http.Client{Timeout: 10 * time.Second}, Secrets: secrets, Redactor: red,
		OnError: onError, channels: chans, queue: make(chan Message, 64), failing: map[int]bool{}}
	go d.loop(ctx)
	return d
}

// Configure replaces the channel list (config reload).
func (d *Dispatcher) Configure(chans []config.Channel) {
	d.mu.Lock()
	d.channels = chans
	d.failing = map[int]bool{}
	d.mu.Unlock()
}

// Emit implements engine.Sink.
func (d *Dispatcher) Emit(ev any) {
	var m Message
	switch e := ev.(type) {
	case event.Check:
		if !e.Notify || e.Notification == nil {
			return
		}
		m = fromNotification(*e.Notification, e.TS)
		m.Monitor, m.Status, m.Side = e.Monitor.Name, string(e.Status), string(e.Side)
		if e.Cause != nil {
			m.Code = e.Cause.Code
		}
	case event.Notice:
		if !e.Notify || e.Notification == nil {
			return
		}
		m = fromNotification(*e.Notification, e.TS)
		m.Kind = e.Kind
	default:
		return
	}
	d.mu.Lock()
	n := len(d.channels)
	d.mu.Unlock()
	if n == 0 {
		return
	}
	select {
	case d.queue <- m:
	default:
	}
}

func fromNotification(n event.Notification, ts event.Timestamp) Message {
	return Message{Title: n.Title, Body: n.Body, Urgency: n.Urgency, ID: n.ID, Source: "sebab", Version: 1,
		Time: ts.Time().UTC().Format(time.RFC3339)}
}

func (d *Dispatcher) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-d.queue:
			d.mu.Lock()
			chans := append([]config.Channel(nil), d.channels...)
			d.mu.Unlock()
			for i, ch := range chans {
				cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := d.send(cctx, ch, m)
				if err != nil && ctx.Err() == nil {
					// One retry for transient failures.
					time.Sleep(time.Second)
					err = d.send(cctx, ch, m)
				}
				cancel()
				d.track(i, ch, err)
			}
		}
	}
}

// track reports the first failure of a channel and stays quiet until it recovers.
func (d *Dispatcher) track(i int, ch config.Channel, err error) {
	d.mu.Lock()
	was := d.failing[i]
	d.failing[i] = err != nil
	d.mu.Unlock()
	if err != nil && !was && d.OnError != nil {
		d.OnError(d.Redactor.Scrub(fmt.Sprintf("notify %s: %v", ch.Type, err)))
	}
}

func (d *Dispatcher) token(ch config.Channel) (string, error) {
	if ch.Secret == "" {
		return "", nil
	}
	v, _, err := d.Secrets.Resolve(ch.Secret)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", ch.Secret, err)
	}
	d.Redactor.Add(v)
	return v, nil
}

// Send delivers one message to one channel synchronously (sebab notify-test).
func (d *Dispatcher) Send(ctx context.Context, ch config.Channel, m Message) error {
	return d.send(ctx, ch, m)
}

func (d *Dispatcher) send(ctx context.Context, ch config.Channel, m Message) error {
	tok, err := d.token(ch)
	if err != nil {
		return err
	}
	var req *http.Request
	switch ch.Type {
	case "webhook":
		body, _ := json.Marshal(m)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	case "ntfy":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, strings.NewReader(m.Body))
		if err == nil {
			req.Header.Set("Title", m.Title)
			req.Header.Set("Priority", ntfyPriority(m.Urgency))
			req.Header.Set("Tags", ntfyTag(m))
		}
	case "telegram":
		api := ch.APIURL
		if api == "" {
			api = "https://api.telegram.org"
		}
		body, _ := json.Marshal(map[string]any{"chat_id": ch.ChatID, "text": m.Title + "\n" + m.Body,
			"disable_web_page_preview": true})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(api, "/")+"/bot"+tok+"/sendMessage", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		tok = "" // the token travels in the path, not in a header
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "sebab")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) {
			// url.Error repeats the URL, which can hold a token: keep only the cause.
			err = ue.Unwrap()
		}
		return fmt.Errorf("deliver: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("server answered %d", resp.StatusCode)
	}
	return nil
}

func ntfyPriority(urgency string) string {
	switch urgency {
	case "critical":
		return "5"
	case "low":
		return "2"
	}
	return "3"
}

func ntfyTag(m Message) string {
	switch {
	case m.Status == "up" || m.Kind == "online" || m.Kind == "vpn_up":
		return "white_check_mark"
	case m.Urgency == "critical":
		return "rotating_light"
	}
	return "warning"
}
