package config

import (
	"time"
)

// Engine holds the resolved engine-wide settings.
type Engine struct {
	Heartbeat time.Duration
	Canary    []string // empty means disabled
	DNSCanary []string
}

// Settings are a monitor's fully resolved values (defaults applied).
type Settings struct {
	Monitor          Monitor
	Interval         time.Duration
	Timeout          time.Duration
	FailThreshold    int
	RecoverThreshold int
	SlowThreshold    time.Duration // 0 disables the HTTP_SLOW check
	TLSWarnDays      int           // 0 disables the expiry warning
	ExpectStatus     []int         // empty means any 2xx
	FollowRedirects  bool
}

// EngineSettings resolves the engine-wide settings.
func (c *Config) EngineSettings() Engine {
	e := Engine{Heartbeat: defaultHeartbeat, Canary: DefaultCanary, DNSCanary: DefaultDNSCanary}
	d := c.Defaults
	if d.Heartbeat != nil {
		e.Heartbeat = d.Heartbeat.D()
	}
	if d.Canary != nil {
		e.Canary = *d.Canary
	}
	if d.DNSCanary != nil {
		e.DNSCanary = *d.DNSCanary
	}
	return e
}

// Resolve applies defaults to a monitor.
func (c *Config) Resolve(m Monitor) Settings {
	d := c.Defaults
	s := Settings{
		Monitor:          m,
		Interval:         pickDur(m.Interval, d.Interval, DefaultInterval),
		Timeout:          pickDur(m.Timeout, d.Timeout, defaultTimeout),
		FailThreshold:    pickInt(m.FailThreshold, d.FailThreshold, defaultThreshold),
		RecoverThreshold: pickInt(m.RecoverThreshold, d.RecoverThreshold, defaultThreshold),
		SlowThreshold:    pickDur(m.SlowThreshold, d.SlowThreshold, defaultSlowThreshold),
		TLSWarnDays:      defaultTLSWarnDays,
		ExpectStatus:     m.ExpectStatus,
		FollowRedirects:  m.FollowRedirects == nil || *m.FollowRedirects,
	}
	if d.TLSWarnDays != nil {
		s.TLSWarnDays = *d.TLSWarnDays
	}
	if m.TLSWarnDays != nil {
		s.TLSWarnDays = *m.TLSWarnDays
	}
	if s.Timeout >= s.Interval {
		s.Timeout = s.Interval / 2
	}
	return s
}

// ResolveAll resolves every monitor in config order.
func (c *Config) ResolveAll() []Settings {
	out := make([]Settings, 0, len(c.Monitors))
	for _, m := range c.Monitors {
		out = append(out, c.Resolve(m))
	}
	return out
}

func pickDur(primary, fallback *Duration, def time.Duration) time.Duration {
	if primary != nil {
		return primary.D()
	}
	if fallback != nil {
		return fallback.D()
	}
	return def
}

func pickInt(primary, fallback, def int) int {
	if primary > 0 {
		return primary
	}
	if fallback > 0 {
		return fallback
	}
	return def
}
