package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/event"
)

const (
	maxRedirects = 10
	maxBodyBytes = 64 << 10
)

// httpTrace records what happened on the first connection of a check.
type httpTrace struct {
	mu          sync.Mutex
	dialed      bool
	dialFailure *Failure
	tlsStart    time.Time
	tlsDone     time.Time
	tlsFailure  *Failure
	notAfter    time.Time
	expiring    bool
	wrote       time.Time
	firstByte   time.Time
}

func (h *httpTrace) firstDial() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	first := !h.dialed
	h.dialed = true
	return first
}

func (t *run) httpCheck(ctx context.Context) {
	u := t.spec.URL
	tr := &httpTrace{}
	method := strings.ToUpper(t.spec.Method)
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, t.clientTrace(tr)), method, u.String(), nil)
	if err != nil {
		t.fail(&Failure{Code: diagnose.HTTPClientError, Facts: diagnose.Facts{Host: u.Hostname(), Detail: "invalid request"}})
		return
	}
	t.applyHeaders(req)
	proxy := t.proxyFunc()
	proxyHost := ""
	if pu, err := proxy(req); err == nil && pu != nil {
		proxyHost = pu.Host
	}
	transport := &http.Transport{
		Proxy:                  proxy,
		DialContext:            t.dialContext(tr, proxyHost),
		TLSClientConfig:        t.tlsConfig(tr, u.Hostname()),
		TLSHandshakeTimeout:    stepBudget(t.spec.Timeout),
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		TLSNextProto:           map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxResponseHeaderBytes: 1 << 20,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: t.checkRedirect(u)}
	resp, err := client.Do(req)
	if err != nil {
		t.fail(t.classifyHTTPError(ctx, err, tr, u))
		t.recordTimings(tr)
		return
	}
	_, _ = io.CopyN(io.Discard, resp.Body, maxBodyBytes)
	resp.Body.Close()
	t.recordTimings(tr)
	if u.Scheme == "https" {
		t.pass(event.LayerTLS)
	}
	t.res.HTTPStatus = resp.StatusCode
	t.res.TLSNotAfter = tr.notAfter
	if f := t.evaluate(resp, tr); f != nil {
		t.fail(f)
		return
	}
	t.pass(event.LayerHTTP)
}

func (t *run) proxyFunc() func(*http.Request) (*url.URL, error) {
	switch {
	case t.p.NoProxy:
		return func(*http.Request) (*url.URL, error) { return nil, nil }
	case t.p.Proxy != nil:
		return t.p.Proxy
	default:
		return http.ProxyFromEnvironment
	}
}

func (t *run) applyHeaders(req *http.Request) {
	if t.p.UserAgent != "" {
		req.Header.Set("User-Agent", t.p.UserAgent)
	}
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	a := t.spec.Auth
	if a == nil {
		return
	}
	switch a.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.Secret)
	case "header":
		req.Header.Set(a.Header, a.Secret)
	case "basic":
		req.SetBasicAuth(a.Username, a.Secret)
	}
}

func (t *run) clientTrace(tr *httpTrace) *httptrace.ClientTrace {
	set := func(dst *time.Time) {
		tr.mu.Lock()
		if dst.IsZero() {
			*dst = t.p.Now()
		}
		tr.mu.Unlock()
	}
	return &httptrace.ClientTrace{
		TLSHandshakeStart:    func() { set(&tr.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { set(&tr.tlsDone) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { set(&tr.wrote) },
		GotFirstResponseByte: func() { set(&tr.firstByte) },
	}
}

// dialContext routes the transport's connections through the layered
// resolve+dial so DNS and TCP failures keep their cause codes.
func (t *run) dialContext(tr *httpTrace, proxyHost string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _ string, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, _ := strconv.Atoi(portStr)
		first := tr.firstDial()
		conn, f := t.connect(ctx, host, port, first)
		if f != nil {
			f.Facts.Proxy = proxyHost
			tr.mu.Lock()
			if tr.dialFailure == nil {
				tr.dialFailure = f
			}
			tr.mu.Unlock()
			return nil, f
		}
		return conn, nil
	}
}

func (t *run) tlsConfig(tr *httpTrace, host string) *tls.Config {
	return &tls.Config{
		// Verification is done in VerifyConnection against the injected clock,
		// which also lets us name the exact failure.
		InsecureSkipVerify: true, //nolint:gosec // verified manually below
		NextProtos:         []string{"http/1.1"},
		MinVersion:         tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			f := t.verifyTLS(cs, tr, host)
			if f != nil {
				tr.mu.Lock()
				if tr.tlsFailure == nil {
					tr.tlsFailure = f
				}
				tr.mu.Unlock()
				return f
			}
			return nil
		},
	}
}

func (t *run) verifyTLS(cs tls.ConnectionState, tr *httpTrace, target string) *Failure {
	if len(cs.PeerCertificates) == 0 {
		return &Failure{Code: diagnose.TLSHandshake, Facts: diagnose.Facts{Host: target, Detail: "no certificate"}}
	}
	leaf := cs.PeerCertificates[0]
	now := t.p.Now()
	name := cs.ServerName
	if name == "" {
		name = target
	}
	facts := diagnose.Facts{Host: name, Now: now, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
		CertNames: certNames(leaf)}
	tr.mu.Lock()
	if tr.notAfter.IsZero() && strings.EqualFold(name, target) {
		tr.notAfter = leaf.NotAfter
		warn := time.Duration(t.spec.TLSWarnDays) * 24 * time.Hour
		tr.expiring = warn > 0 && now.Before(leaf.NotAfter) && leaf.NotAfter.Sub(now) < warn
	}
	tr.mu.Unlock()
	if t.spec.InsecureTLS {
		return nil
	}
	switch {
	case now.Before(leaf.NotBefore):
		return &Failure{Code: diagnose.TLSNotYetValid, Facts: facts}
	case now.After(leaf.NotAfter):
		return &Failure{Code: diagnose.TLSExpired, Facts: facts}
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: t.spec.RootCAs, Intermediates: inter, CurrentTime: now})
	if err != nil {
		var invalid x509.CertificateInvalidError
		if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
			// An intermediate expired; report it as an expired chain.
			facts.Detail = "certificate chain expired"
			return &Failure{Code: diagnose.TLSExpired, Facts: facts}
		}
		facts.SelfSigned = isSelfSigned(leaf)
		facts.Detail = "unknown authority"
		return &Failure{Code: diagnose.TLSUntrusted, Facts: facts}
	}
	if err := leaf.VerifyHostname(name); err != nil {
		return &Failure{Code: diagnose.TLSHostMismatch, Facts: facts}
	}
	return nil
}

func certNames(c *x509.Certificate) []string {
	names := slices.Clone(c.DNSNames)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = append(names, c.Subject.CommonName)
	}
	return names
}

// isSelfSigned checks the signature directly: CheckSignatureFrom would reject a
// self-signed leaf because it is not a CA.
func isSelfSigned(c *x509.Certificate) bool {
	return bytes.Equal(c.RawIssuer, c.RawSubject) &&
		c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

// checkRedirect enforces the hop limit and the allowed-host list, and strips the
// custom auth header when leaving the original host (Go already strips
// Authorization and cookies across hosts).
func (t *run) checkRedirect(orig *url.URL) func(*http.Request, []*http.Request) error {
	allowed := map[string]bool{}
	base := strings.ToLower(orig.Hostname())
	allowed[base] = true
	allowed[strings.TrimPrefix(base, "www.")] = true
	allowed["www."+strings.TrimPrefix(base, "www.")] = true
	for _, h := range t.spec.AllowedRedirectHosts {
		allowed[strings.ToLower(h)] = true
	}
	return func(req *http.Request, via []*http.Request) error {
		if !t.spec.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) > maxRedirects {
			return &Failure{Code: diagnose.HTTPRedirLoop, Facts: diagnose.Facts{Host: orig.Hostname(), Redirects: len(via)}}
		}
		host := strings.ToLower(req.URL.Hostname())
		if !allowed[host] {
			return &Failure{Code: diagnose.HTTPUnexpRedir, Facts: diagnose.Facts{Host: orig.Hostname(), RedirectHost: host}}
		}
		if host != base && t.spec.Auth != nil && t.spec.Auth.Type == "header" {
			req.Header.Del(t.spec.Auth.Header)
		}
		return nil
	}
}

func (t *run) classifyHTTPError(ctx context.Context, err error, tr *httpTrace, u *url.URL) *Failure {
	tr.mu.Lock()
	dialFail, tlsFail := tr.dialFailure, tr.tlsFailure
	dialed, wrote := tr.dialed, tr.wrote
	tr.mu.Unlock()
	var f *Failure
	switch {
	case errors.As(err, &f):
		return f
	case tlsFail != nil:
		return tlsFail
	case dialFail != nil:
		return dialFail
	}
	host := u.Hostname()
	timeout := isTimeout(err) || ctx.Err() != nil
	// Connected over HTTPS but the request was never written: the handshake failed.
	if u.Scheme == "https" && dialed && wrote.IsZero() {
		detail := shortErr(err)
		if timeout {
			detail = "timed out"
		}
		return &Failure{Code: diagnose.TLSHandshake, Facts: diagnose.Facts{Host: host, Detail: tlsDetail(detail)}}
	}
	from := wrote
	if from.IsZero() {
		from = t.res.Start
	}
	if timeout {
		return &Failure{Code: diagnose.HTTPTimeout, Facts: diagnose.Facts{Host: host, Elapsed: t.p.Now().Sub(from)}}
	}
	detail := "connection closed without a response"
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, syscall.ECONNRESET) {
		detail = shortErr(err)
	}
	return &Failure{Code: diagnose.HTTPServerError, Facts: diagnose.Facts{Host: host, Detail: detail}}
}

func tlsDetail(s string) string {
	s = strings.TrimPrefix(s, "tls: ")
	s = strings.TrimPrefix(s, "remote error: tls: ")
	return s
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

func (t *run) recordTimings(tr *httpTrace) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if !tr.tlsStart.IsZero() && !tr.tlsDone.IsZero() && tr.tlsFailure == nil {
		t.res.Timings.TLS = event.MS(tr.tlsDone.Sub(tr.tlsStart))
	}
	if !tr.wrote.IsZero() && !tr.firstByte.IsZero() {
		t.res.Timings.TTFB = event.MS(tr.firstByte.Sub(tr.wrote))
	}
}

// evaluate maps the final response to an outcome: failures first, then
// degradations (TLS expiring soon, slow response).
func (t *run) evaluate(resp *http.Response, tr *httpTrace) *Failure {
	code := resp.StatusCode
	host := t.spec.URL.Hostname()
	facts := diagnose.Facts{Host: host, HTTPStatus: code, Expected: t.spec.ExpectStatus}
	if !t.statusExpected(code) {
		switch {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return &Failure{Code: diagnose.HTTPAuth, Facts: facts}
		case code == http.StatusTooManyRequests:
			facts.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), t.p.Now())
			return &Failure{Code: diagnose.HTTPRateLimited, Facts: facts}
		case code == http.StatusBadGateway || code == http.StatusGatewayTimeout:
			return &Failure{Code: diagnose.HTTPBadGateway, Facts: facts}
		case code == http.StatusServiceUnavailable:
			return &Failure{Code: diagnose.HTTPUnavailable, Facts: facts}
		case code >= 500:
			return &Failure{Code: diagnose.HTTPServerError, Facts: facts}
		case code >= 400:
			return &Failure{Code: diagnose.HTTPClientError, Facts: facts}
		default:
			return &Failure{Code: diagnose.HTTPUnexpStatus, Facts: facts}
		}
	}
	tr.mu.Lock()
	expiring, notAfter := tr.expiring, tr.notAfter
	tr.mu.Unlock()
	if expiring {
		return &Failure{Code: diagnose.TLSExpiringSoon, Facts: diagnose.Facts{Host: host, Now: t.p.Now(), NotAfter: notAfter}}
	}
	elapsed := t.p.Now().Sub(t.res.Start)
	if t.spec.SlowThreshold > 0 && elapsed > t.spec.SlowThreshold {
		return &Failure{Code: diagnose.HTTPSlow, Facts: diagnose.Facts{Host: host, HTTPStatus: code,
			Elapsed: elapsed, Threshold: t.spec.SlowThreshold}}
	}
	return nil
}

func (t *run) statusExpected(code int) bool {
	if len(t.spec.ExpectStatus) > 0 {
		return slices.Contains(t.spec.ExpectStatus, code)
	}
	if code >= 200 && code < 300 {
		return true
	}
	return !t.spec.FollowRedirects && code >= 300 && code < 400
}

// parseRetryAfter accepts delay-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil && when.After(now) {
		return when.Sub(now).Round(time.Second)
	}
	return 0
}
