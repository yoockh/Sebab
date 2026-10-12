package engine

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/event"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/internal/secret"
)

// ProbeChecker is the production Checker: it resolves secrets and CA files and
// runs the prober. Secret values are registered with the redactor before use.
type ProbeChecker struct {
	Prober   *probe.Prober
	Secrets  *secret.Store
	Redactor *secret.Redactor
	// OnError reports a problem as an error event. Repeated identical errors
	// for the same monitor are reported once.
	OnError func(ev event.Error)
	Now     func() time.Time

	mu      sync.Mutex
	lastErr map[string]string
}

// Check implements Checker.
func (c *ProbeChecker) Check(ctx context.Context, s config.Settings) probe.Result {
	spec, problems := BuildSpec(s, c.Secrets, c.Redactor)
	c.report(s.Monitor.ID, problems)
	return c.Prober.Run(ctx, spec)
}

func (c *ProbeChecker) report(id string, problems []SpecProblem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastErr == nil {
		c.lastErr = map[string]string{}
	}
	key := ""
	for _, p := range problems {
		key += p.Code + ":" + p.Message + ";"
	}
	if key == c.lastErr[id] {
		return
	}
	c.lastErr[id] = key
	if c.OnError == nil {
		return
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	for _, p := range problems {
		ev := event.NewError(now(), p.Code, c.Redactor.Scrub(p.Message))
		ev.Monitor = id
		c.OnError(ev)
	}
}

// SpecProblem is a non-fatal problem found while building a spec.
type SpecProblem struct {
	Code    string
	Message string
}

// BuildSpec converts resolved settings to a probe spec. Missing secrets or CA
// files are reported as problems; the check still runs (and will most likely
// fail with HTTP_AUTH or TLS_UNTRUSTED, which tells the user what to fix).
func BuildSpec(s config.Settings, store *secret.Store, red *secret.Redactor) (probe.Spec, []SpecProblem) {
	m := s.Monitor
	spec := probe.Spec{
		Kind: m.Kind(), Timeout: s.Timeout, ExpectStatus: s.ExpectStatus, SlowThreshold: s.SlowThreshold,
		TLSWarnDays: s.TLSWarnDays, FollowRedirects: s.FollowRedirects, AllowedRedirectHosts: m.AllowedRedirectHosts,
		Method: m.Method, Headers: m.Headers, InsecureTLS: m.InsecureTLS, Assertions: m.Assert,
	}
	var problems []SpecProblem
	if m.RequiresVPN != nil {
		spec.RequiresVPN = m.RequiresVPN.Interface
	}
	if m.Kind() == "http" {
		u, err := url.Parse(m.URL)
		if err != nil {
			problems = append(problems, SpecProblem{event.ErrConfigInvalid, fmt.Sprintf("monitor '%s': bad URL", m.ID)})
			return spec, problems
		}
		spec.URL, spec.Host = u, u.Hostname()
		spec.Port = defaultPort(u)
	} else {
		host, port, err := config.ParseTCPTarget(m.Target)
		if err != nil {
			problems = append(problems, SpecProblem{event.ErrConfigInvalid, fmt.Sprintf("monitor '%s': %v", m.ID, err)})
			return spec, problems
		}
		spec.Host, spec.Port = host, port
	}
	if m.CAFile != "" {
		pool, err := loadCAFile(m.CAFile)
		if err != nil {
			problems = append(problems, SpecProblem{event.ErrConfigInvalid, fmt.Sprintf("monitor '%s': ca_file: %v", m.ID, err)})
		} else {
			spec.RootCAs = pool
		}
	}
	if m.Auth != nil {
		if store == nil {
			problems = append(problems, SpecProblem{event.ErrSecretMissing, fmt.Sprintf("monitor '%s': no secret store", m.ID)})
			return spec, problems
		}
		value, _, err := store.Resolve(m.Auth.Secret)
		if err != nil {
			msg := fmt.Sprintf("monitor '%s': secret %q unavailable: %v", m.ID, m.Auth.Secret, err)
			if errors.Is(err, secret.ErrNotFound) {
				msg = fmt.Sprintf("monitor '%s': secret %q not found; store it with 'sebab secret set %s'",
					m.ID, m.Auth.Secret, m.Auth.Secret)
			}
			problems = append(problems, SpecProblem{event.ErrSecretMissing, msg})
			return spec, problems
		}
		if red != nil {
			if m.Auth.Type == "basic" {
				red.AddBasic(m.Auth.Username, value)
			} else {
				red.Add(value)
			}
		}
		spec.Auth = &probe.Credentials{Type: m.Auth.Type, Header: m.Auth.Header, Username: m.Auth.Username, Secret: value}
	}
	return spec, problems
}

func defaultPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		var n int
		fmt.Sscanf(p, "%d", &n)
		return n
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

func loadCAFile(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("no PEM certificates found")
	}
	return pool, nil
}
