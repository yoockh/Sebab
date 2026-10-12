package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/secret"
)

const token = "chan-canary-token-81f2"

type secrets map[string]string

func (s secrets) Resolve(ref string) (string, string, error) {
	if v, ok := s[ref]; ok {
		return v, "test", nil
	}
	return "", "", errors.New("missing")
}

type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (c *capture) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, r)
		c.body = append(c.body, string(b))
		c.mu.Unlock()
		w.WriteHeader(status)
	}
}

func (c *capture) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.reqs)
		c.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d requests, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func downEvent() event.Check {
	c := event.NewCheck(time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC))
	c.Monitor = event.MonitorRef{ID: "api", Name: "Payments API", Kind: "http", Target: "https://internal.example/secret-path"}
	c.Status, c.Side = event.StatusDown, event.SideTheirs
	c.Cause = &event.Cause{Layer: event.LayerTLS, Code: "TLS_EXPIRED", Summary: "TLS certificate expired 2 days ago"}
	c.Notify = true
	c.Notification = &event.Notification{ID: "api:down:1", Title: "Payments API is down",
		Body: "TLS certificate expired 2 days ago.", Urgency: "critical"}
	return c
}

func TestWebhookNtfyTelegram(t *testing.T) {
	var hook, ntfy, tg capture
	hs := httptest.NewServer(hook.handler(200))
	defer hs.Close()
	ns := httptest.NewServer(ntfy.handler(200))
	defer ns.Close()
	ts := httptest.NewServer(tg.handler(200))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	red := secret.NewRedactor()
	d := NewDispatcher(ctx, []config.Channel{
		{Type: "webhook", URL: hs.URL + "/hook", Secret: "hook"},
		{Type: "ntfy", URL: ns.URL + "/topic"},
		{Type: "telegram", Secret: "tg", ChatID: "42", APIURL: ts.URL},
	}, secrets{"hook": token, "tg": token}, red, nil)
	d.Emit(downEvent())
	d.Emit(event.NewHeartbeat(time.Now())) // ignored
	notNotify := downEvent()
	notNotify.Notify = false
	d.Emit(notNotify) // ignored

	hook.wait(t, 1)
	ntfy.wait(t, 1)
	tg.wait(t, 1)

	var m Message
	if err := json.Unmarshal([]byte(hook.body[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Title != "Payments API is down" || m.Code != "TLS_EXPIRED" || m.Monitor != "Payments API" || m.Side != "theirs" {
		t.Errorf("webhook payload %+v", m)
	}
	if hook.reqs[0].Header.Get("Authorization") != "Bearer "+token {
		t.Error("webhook secret should be sent as a bearer token")
	}
	if strings.Contains(hook.body[0], "internal.example") {
		t.Error("targets must never be sent to external channels")
	}
	if ntfy.reqs[0].Header.Get("Title") != "Payments API is down" || ntfy.reqs[0].Header.Get("Priority") != "5" ||
		ntfy.body[0] != "TLS certificate expired 2 days ago." {
		t.Errorf("ntfy request %+v %q", ntfy.reqs[0].Header, ntfy.body[0])
	}
	if tg.reqs[0].URL.Path != "/bot"+token+"/sendMessage" || !strings.Contains(tg.body[0], `"chat_id":"42"`) {
		t.Errorf("telegram request %s %s", tg.reqs[0].URL.Path, tg.body[0])
	}
	if tg.reqs[0].Header.Get("Authorization") != "" {
		t.Error("telegram carries the token in the path only")
	}
}

func TestFailuresReportedOnceAndScrubbed(t *testing.T) {
	var tg capture
	ts := httptest.NewServer(tg.handler(500))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var errs []string
	d := NewDispatcher(ctx, []config.Channel{{Type: "telegram", Secret: "tg", ChatID: "1", APIURL: ts.URL}},
		secrets{"tg": token}, secret.NewRedactor(), func(s string) { mu.Lock(); errs = append(errs, s); mu.Unlock() })
	d.Emit(downEvent())
	d.Emit(downEvent())
	tg.wait(t, 4) // two messages, one retry each
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 || !strings.Contains(errs[0], "server answered 500") {
		t.Fatalf("want one failure report, got %v", errs)
	}
	for _, e := range errs {
		if strings.Contains(e, token) {
			t.Fatalf("token leaked in error: %s", e)
		}
	}
}

func TestUnreachableChannelErrorIsScrubbed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 1)
	// A closed loopback port: the URL (with the token in its path) must not appear.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	d := NewDispatcher(ctx, []config.Channel{{Type: "telegram", Secret: "tg", ChatID: "1", APIURL: url}},
		secrets{"tg": token}, secret.NewRedactor(), func(s string) { got <- s })
	d.Emit(downEvent())
	select {
	case e := <-got:
		if strings.Contains(e, token) || strings.Contains(e, "/bot") {
			t.Fatalf("error leaks the token URL: %s", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no error reported")
	}
}

func TestNoChannelsNoWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewDispatcher(ctx, nil, secrets{}, secret.NewRedactor(), nil)
	d.Emit(downEvent())
	if len(d.queue) != 0 {
		t.Error("nothing should be queued without channels")
	}
}
