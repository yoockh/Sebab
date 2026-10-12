package lab

import (
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler serves the lab routes:
//
//	/ok                 200
//	/status/{code}      that status (429 adds Retry-After: 120)
//	/slow?ms=N          200 after N milliseconds
//	/hang               never answers (until the client gives up)
//	/loop               redirects to itself forever
//	/away               redirects to elsewhere.lab.test (unexpected host)
//	/redirect           redirects to /ok on the same host
//	/closed             closes the connection without a response
//	/json               {"status":"ok","items":[{"name":"a","state":"ready"}]}
//	/portal             200 with a login page (captive portal look-alike)
//	/auth/bearer        200 with "Authorization: Bearer <Token>", else 401
//	/auth/header        200 with "X-API-Key: <Token>", else 401
//	/auth/basic         200 with basic auth lab:<Token>, else 401
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || code < 200 || code > 599 {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "120")
		}
		w.WriteHeader(code)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
			fmt.Fprintln(w, "slow ok")
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/hang", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		http.Redirect(w, r, fmt.Sprintf("/loop?n=%d", n+1), http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		target := *r.URL
		target.Host = "elsewhere.lab.test:" + portOf(r.Host)
		target.Path = "/ok"
		target.Scheme = "http"
		if r.TLS != nil {
			target.Scheme = "https"
		}
		http.Redirect(w, r, target.String(), http.StatusFound)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok","items":[{"name":"a","state":"ready"}]}`)
	})
	mux.HandleFunc("/portal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, "<html><body>Please sign in to the hotel Wi-Fi</body></html>")
	})
	mux.HandleFunc("/closed", func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		if c, _, err := hj.Hijack(); err == nil {
			c.Close()
		}
	})
	mux.HandleFunc("/auth/bearer", requireAuth(func(r *http.Request) bool {
		return equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), Token) &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	}))
	mux.HandleFunc("/auth/header", requireAuth(func(r *http.Request) bool {
		return equal(r.Header.Get("X-API-Key"), Token)
	}))
	mux.HandleFunc("/auth/basic", requireAuth(func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == BasicUser && equal(p, Token)
	}))
	return mux
}

func requireAuth(ok func(*http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ok(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintln(w, "authorized")
	}
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func portOf(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[i+1:]
	}
	return "80"
}

// quietLog drops the server's TLS handshake noise from test output.
func quietLog() *log.Logger { return log.New(io.Discard, "", 0) }
