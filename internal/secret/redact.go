package secret

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Mask replaces redacted values.
const Mask = "[REDACTED]"

// minSecretLen avoids redacting tiny values that would mangle normal text.
const minSecretLen = 3

// Redactor scrubs known secret values (and their common encodings) from text.
// It is safe for concurrent use and implements event.Scrubber.
type Redactor struct {
	mu     sync.RWMutex
	values map[string]struct{}
	sorted []string // longest first, so overlapping secrets mask fully
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{values: map[string]struct{}{}} }

// Add registers a secret value and its JSON-escaped, URL-encoded and base64 forms.
func (r *Redactor) Add(secret string) {
	if len(secret) < minSecretLen {
		return
	}
	forms := []string{secret, url.QueryEscape(secret), url.PathEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret))}
	if b, err := json.Marshal(secret); err == nil {
		forms = append(forms, string(b[1:len(b)-1]))
	}
	r.addForms(forms)
}

// AddBasic registers the base64 form of "user:password" as sent in basic auth.
func (r *Redactor) AddBasic(user, password string) {
	r.Add(password)
	r.addForms([]string{base64.StdEncoding.EncodeToString([]byte(user + ":" + password))})
}

func (r *Redactor) addForms(forms []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]struct{}{}
	}
	for _, f := range forms {
		if len(f) >= minSecretLen {
			r.values[f] = struct{}{}
		}
	}
	r.sorted = r.sorted[:0]
	for v := range r.values {
		r.sorted = append(r.sorted, v)
	}
	sort.Slice(r.sorted, func(i, j int) bool { return len(r.sorted[i]) > len(r.sorted[j]) })
}

// Scrub returns s with every registered secret masked.
func (r *Redactor) Scrub(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.sorted {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	return s
}

// ScrubBytes implements event.Scrubber.
func (r *Redactor) ScrubBytes(b []byte) []byte {
	if r == nil {
		return b
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.sorted {
		if bytes.Contains(b, []byte(v)) {
			b = bytes.ReplaceAll(b, []byte(v), []byte(Mask))
		}
	}
	return b
}

// sensitiveParam reports whether a query parameter name usually carries a credential.
func sensitiveParam(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"token", "secret", "password", "passwd", "pwd", "key", "auth", "sig", "session",
		"credential", "code", "jwt", "sid"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// RedactURL masks userinfo passwords and sensitive query parameter values.
// Unparseable input is masked entirely rather than risk leaking it.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" && u.Host == "" && !strings.Contains(raw, "?") {
		if strings.Contains(raw, "?") || strings.Contains(raw, "@") {
			return Mask
		}
		return raw
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), Mask)
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for k, vs := range q {
			if sensitiveParam(k) {
				for i := range vs {
					vs[i] = Mask
				}
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	// Keep the mask readable instead of "%5BREDACTED%5D".
	return strings.ReplaceAll(u.String(), url.QueryEscape(Mask), Mask)
}
