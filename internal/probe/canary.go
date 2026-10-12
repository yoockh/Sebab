package probe

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
)

// CanaryChecker tests whether the local side can reach the internet. Results are
// cached briefly so a burst of failing monitors triggers one canary round.
type CanaryChecker struct {
	Prober   *Prober
	Targets  []string // tcp://host:port
	DNSNames []string
	Timeout  time.Duration
	CacheFor time.Duration
	mu       sync.Mutex
	last     diagnose.Canary
	lastAt   time.Time
	lastDNS  bool
	inflight chan struct{}
}

// Check runs (or reuses) a canary round. withDNS also resolves the DNS canary names.
func (c *CanaryChecker) Check(ctx context.Context, withDNS bool) diagnose.Canary {
	c.mu.Lock()
	disabled := len(c.Targets) == 0
	c.mu.Unlock()
	if disabled {
		v := diagnose.Canary{Disabled: true}
		c.mu.Lock()
		names := c.DNSNames
		c.mu.Unlock()
		if withDNS && len(names) > 0 {
			v.DNSChecked, v.DNSOK = true, c.checkDNS(ctx, names)
		}
		return v
	}
	for {
		c.mu.Lock()
		now := c.Prober.Now()
		if !c.lastAt.IsZero() && now.Sub(c.lastAt) < c.CacheFor && (c.lastDNS || !withDNS) {
			v := c.last
			c.mu.Unlock()
			return v
		}
		if c.inflight == nil {
			done := make(chan struct{})
			c.inflight = done
			c.mu.Unlock()
			v := c.run(ctx, withDNS)
			c.mu.Lock()
			c.last, c.lastAt, c.lastDNS = v, c.Prober.Now(), withDNS
			c.inflight = nil
			c.mu.Unlock()
			close(done)
			return v
		}
		wait := c.inflight
		c.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return diagnose.Canary{Checked: true, OK: true} // shutting down: do not claim offline
		}
	}
}

// Configure replaces the canary targets (config reload).
func (c *CanaryChecker) Configure(targets, dnsNames []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Targets, c.DNSNames = targets, dnsNames
	c.lastAt = time.Time{}
}

// Invalidate drops the cached verdict (used when the network changes).
func (c *CanaryChecker) Invalidate() {
	c.mu.Lock()
	c.lastAt = time.Time{}
	c.mu.Unlock()
}

func (c *CanaryChecker) run(ctx context.Context, withDNS bool) diagnose.Canary {
	c.mu.Lock()
	targets, names := c.Targets, c.DNSNames
	c.mu.Unlock()
	v := diagnose.Canary{Checked: true, OK: c.checkTCP(ctx, targets)}
	if withDNS && len(names) > 0 {
		v.DNSChecked, v.DNSOK = true, c.checkDNS(ctx, names)
	}
	return v
}

func (c *CanaryChecker) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 3 * time.Second
}

// checkTCP connects to every target in parallel; one success is enough.
func (c *CanaryChecker) checkTCP(ctx context.Context, targets []string) bool {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	results := make(chan bool, len(targets))
	for _, target := range targets {
		go func(target string) {
			host, port, err := config.ParseTCPTarget(target)
			if err != nil {
				results <- false
				return
			}
			addr := host
			if net.ParseIP(host) == nil {
				addrs, err := c.Prober.Resolver.Lookup(ctx, host)
				if err != nil || len(addrs) == 0 {
					results <- false
					return
				}
				addr = addrs[0].IP.String()
			}
			conn, err := c.Prober.Dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
			if err != nil {
				results <- false
				return
			}
			conn.Close()
			results <- true
		}(target)
	}
	ok := false
	for range targets {
		if <-results {
			ok = true
			cancel() // stop the others early
		}
	}
	return ok
}

func (c *CanaryChecker) checkDNS(ctx context.Context, names []string) bool {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	for _, name := range names {
		if _, err := c.Prober.Resolver.Lookup(ctx, name); err == nil {
			return true
		}
	}
	return false
}
