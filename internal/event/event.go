// Package event defines the versioned Sebab event stream (schema v1).
//
// Every event is one JSON object per line. Frontends must ignore unknown fields
// and unknown "type" values, so new fields and types are additive. Removing or
// renaming a field bumps Version.
package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// Version is the schema version carried in every event's "v" field.
const Version = 1

// Type names an event kind.
type Type string

// Event types. Notice and Monitors are additive extensions of the PRD draft.
const (
	TypeHello     Type = "hello"
	TypeCheck     Type = "check"
	TypeSummary   Type = "summary"
	TypeHeartbeat Type = "heartbeat"
	TypeError     Type = "error"
	TypeNotice    Type = "notice"
	TypeMonitors  Type = "monitors"
)

// Status is a monitor's state.
type Status string

// The five monitor states from the PRD status model.
const (
	StatusUp       Status = "up"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
	StatusUnknown  Status = "unknown"
	StatusPaused   Status = "paused"
)

// Side says whose fault a failure is.
type Side string

// Sides. Empty side is encoded only for healthy checks.
const (
	SideYours   Side = "yours"
	SideTheirs  Side = "theirs"
	SideUnknown Side = "unknown"
)

// Layer is one step of the diagnosis pipeline.
type Layer string

// Layers in probe order.
const (
	LayerLocal Layer = "local"
	LayerVPN   Layer = "vpn"
	LayerDNS   Layer = "dns"
	LayerTCP   Layer = "tcp"
	LayerTLS   Layer = "tls"
	LayerHTTP  Layer = "http"
)

// Timestamp encodes as an RFC 3339 UTC string with millisecond precision.
type Timestamp time.Time

// TS converts a time to a Timestamp.
func TS(t time.Time) Timestamp { return Timestamp(t) }

// Time returns the underlying time.
func (t Timestamp) Time() time.Time { return time.Time(t) }

// MarshalJSON implements json.Marshaler.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(timeLayout))
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("timestamp: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("timestamp: %w", err)
	}
	*t = Timestamp(parsed)
	return nil
}

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// Header is embedded in every event.
type Header struct {
	V    int       `json:"v"`
	Type Type      `json:"type"`
	TS   Timestamp `json:"ts"`
}

func header(t Type, ts time.Time) Header { return Header{V: Version, Type: t, TS: TS(ts)} }

// Hello is the first event of every stream.
type Hello struct {
	Header
	Engine      string `json:"engine"`
	Monitors    int    `json:"monitors"`
	HeartbeatMS int64  `json:"heartbeat_ms"`
	Source      string `json:"source,omitempty"` // "embedded" or "daemon"
}

// NewHello builds a hello event.
func NewHello(ts time.Time, engine string, monitors int, heartbeat time.Duration, source string) Hello {
	return Hello{Header: header(TypeHello, ts), Engine: engine, Monitors: monitors,
		HeartbeatMS: heartbeat.Milliseconds(), Source: source}
}

// MonitorRef identifies a monitor inside a check event.
type MonitorRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Target      string `json:"target,omitempty"` // always redacted
	InsecureTLS bool   `json:"insecure_tls,omitempty"`
}

// Cause is the diagnosis of a non-healthy check.
type Cause struct {
	Layer   Layer  `json:"layer"`
	Code    string `json:"code"`
	Summary string `json:"summary"`
	Hint    string `json:"hint"`
}

// Timings holds per-step durations in milliseconds. Nil means the step did not
// complete (failed or never ran), encoded as JSON null.
type Timings struct {
	DNS   *int64 `json:"dns"`
	TCP   *int64 `json:"tcp"`
	TLS   *int64 `json:"tls"`
	TTFB  *int64 `json:"ttfb"`
	Total *int64 `json:"total"`
}

// MS converts a duration to a millisecond pointer for Timings.
func MS(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

// Notification is a message the engine decided to show. Frontends dedupe by ID.
type Notification struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Urgency string `json:"urgency"` // "low", "normal" or "critical"
}

// Details carries optional facts about a check.
type Details struct {
	HTTPStatus    int        `json:"http_status,omitempty"`
	Address       string     `json:"address,omitempty"`
	Family        string     `json:"family,omitempty"`
	TLSNotAfter   *Timestamp `json:"tls_not_after,omitempty"`
	RetryAfterSec int        `json:"retry_after_s,omitempty"`
}

// Check reports the result of one check of one monitor.
type Check struct {
	Header
	Monitor      MonitorRef    `json:"monitor"`
	Status       Status        `json:"status"`
	Observed     Status        `json:"observed,omitempty"`
	Side         Side          `json:"side,omitempty"`
	Cause        *Cause        `json:"cause,omitempty"`
	Timings      Timings       `json:"timings_ms"`
	Since        *Timestamp    `json:"since,omitempty"`
	Details      *Details      `json:"details,omitempty"`
	Notify       bool          `json:"notify"`
	Notification *Notification `json:"notification,omitempty"`
}

// NewCheck builds a check event header; callers fill the rest.
func NewCheck(ts time.Time) Check { return Check{Header: header(TypeCheck, ts)} }

// Summary is the aggregate state across monitors.
type Summary struct {
	Header
	Up           int    `json:"up"`
	Degraded     int    `json:"degraded"`
	Down         int    `json:"down"`
	Unknown      int    `json:"unknown"`
	Paused       int    `json:"paused"`
	Offline      bool   `json:"offline"`
	OfflineCause string `json:"offline_cause,omitempty"`
}

// NewSummary builds a summary event header.
func NewSummary(ts time.Time) Summary { return Summary{Header: header(TypeSummary, ts)} }

// Heartbeat proves the engine loop is alive.
type Heartbeat struct {
	Header
}

// NewHeartbeat builds a heartbeat event.
func NewHeartbeat(ts time.Time) Heartbeat { return Heartbeat{Header: header(TypeHeartbeat, ts)} }

// Error reports an engine-side error that frontends should surface.
type Error struct {
	Header
	Code    string `json:"code"`
	Message string `json:"message"`
	Monitor string `json:"monitor,omitempty"`
}

// Error codes.
const (
	ErrConfigInvalid = "CONFIG_INVALID"
	ErrConfigMissing = "CONFIG_MISSING"
	ErrSecretMissing = "SECRET_MISSING"
	ErrInternal      = "INTERNAL"
)

// NewError builds an error event.
func NewError(ts time.Time, code, message string) Error {
	return Error{Header: header(TypeError, ts), Code: code, Message: message}
}

// Notice kinds.
const (
	NoticeOffline  = "offline"
	NoticeOnline   = "online"
	NoticeVPNDown  = "vpn_down"
	NoticeVPNUp    = "vpn_up"
	NoticeResumed  = "resumed"
	NoticeReloaded = "config_reloaded"
)

// Notice is an engine-level message that is not tied to one monitor.
type Notice struct {
	Header
	Kind         string        `json:"kind"`
	Message      string        `json:"message"`
	Monitors     []string      `json:"monitors,omitempty"`
	Notify       bool          `json:"notify"`
	Notification *Notification `json:"notification,omitempty"`
}

// NewNotice builds a notice event.
func NewNotice(ts time.Time, kind, message string) Notice {
	return Notice{Header: header(TypeNotice, ts), Kind: kind, Message: message}
}

// MonitorInfo describes a configured monitor (no secrets, target redacted).
type MonitorInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	IntervalS   int    `json:"interval_s"`
	Paused      bool   `json:"paused"`
	RequiresVPN string `json:"requires_vpn,omitempty"`
	Auth        string `json:"auth,omitempty"` // auth type only
	InsecureTLS bool   `json:"insecure_tls,omitempty"`
}

// Monitors lists the configured monitors. Sent after hello and on reload.
type Monitors struct {
	Header
	Monitors []MonitorInfo `json:"monitors"`
}

// NewMonitors builds a monitors event.
func NewMonitors(ts time.Time, list []MonitorInfo) Monitors {
	if list == nil {
		list = []MonitorInfo{}
	}
	return Monitors{Header: header(TypeMonitors, ts), Monitors: list}
}

// Peek decodes only the header of a raw event line.
func Peek(line []byte) (Header, error) {
	var h Header
	if err := json.Unmarshal(line, &h); err != nil {
		return Header{}, fmt.Errorf("decode event header: %w", err)
	}
	return h, nil
}
