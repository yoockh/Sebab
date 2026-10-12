// Package diagnose holds the cause-code taxonomy (PRD Appendix B), turns probe
// outcomes into human summaries and hints, and decides whose side a failure is on.
package diagnose

import (
	"github.com/yoockh/sebab/internal/event"
)

// Code is a cause code.
type Code string

// Cause codes from PRD Appendix B, plus TLS_EXPIRING_SOON for Story 10.
const (
	LocalNoLink     Code = "LOCAL_NO_LINK"
	LocalNoRoute    Code = "LOCAL_NO_ROUTE"
	LocalOffline    Code = "LOCAL_OFFLINE"
	LocalDNSBroken  Code = "LOCAL_DNS_BROKEN"
	VPNDown         Code = "VPN_DOWN"
	DNSNXDomain     Code = "DNS_NXDOMAIN"
	DNSServFail     Code = "DNS_SERVFAIL"
	DNSTimeout      Code = "DNS_TIMEOUT"
	DNSNoAddress    Code = "DNS_NO_ADDRESS"
	TCPRefused      Code = "TCP_REFUSED"
	TCPTimeout      Code = "TCP_TIMEOUT"
	TCPUnreachable  Code = "TCP_UNREACHABLE"
	TLSExpired      Code = "TLS_EXPIRED"
	TLSNotYetValid  Code = "TLS_NOT_YET_VALID"
	TLSHostMismatch Code = "TLS_HOSTNAME_MISMATCH"
	TLSUntrusted    Code = "TLS_UNTRUSTED"
	TLSHandshake    Code = "TLS_HANDSHAKE_FAILED"
	TLSExpiringSoon Code = "TLS_EXPIRING_SOON"
	HTTPBadGateway  Code = "HTTP_BAD_GATEWAY"
	HTTPUnavailable Code = "HTTP_UNAVAILABLE"
	HTTPServerError Code = "HTTP_SERVER_ERROR"
	HTTPAuth        Code = "HTTP_AUTH"
	HTTPClientError Code = "HTTP_CLIENT_ERROR"
	HTTPRateLimited Code = "HTTP_RATE_LIMITED"
	HTTPTimeout     Code = "HTTP_TIMEOUT"
	HTTPSlow        Code = "HTTP_SLOW"
	HTTPUnexpStatus Code = "HTTP_UNEXPECTED_STATUS"
	HTTPUnexpRedir  Code = "HTTP_UNEXPECTED_REDIRECT"
	HTTPRedirLoop   Code = "HTTP_REDIRECT_LOOP"
)

// Spec is the static description of a code.
type Spec struct {
	Layer   event.Layer
	Side    event.Side   // default side when the canary is fine
	Status  event.Status // status a check with this code gets
	Meaning string
}

// Taxonomy maps every code to its spec. Tests assert it matches Appendix B.
var Taxonomy = map[Code]Spec{
	LocalNoLink:     {event.LayerLocal, event.SideYours, event.StatusUnknown, "No active network interface"},
	LocalNoRoute:    {event.LayerLocal, event.SideYours, event.StatusUnknown, "No default route"},
	LocalOffline:    {event.LayerLocal, event.SideYours, event.StatusUnknown, "Canary unreachable"},
	LocalDNSBroken:  {event.LayerLocal, event.SideYours, event.StatusUnknown, "Resolver fails for canary names too"},
	VPNDown:         {event.LayerVPN, event.SideYours, event.StatusPaused, "Required VPN interface missing or down"},
	DNSNXDomain:     {event.LayerDNS, event.SideTheirs, event.StatusDown, "Name does not exist"},
	DNSServFail:     {event.LayerDNS, event.SideTheirs, event.StatusDown, "Authoritative or resolver failure"},
	DNSTimeout:      {event.LayerDNS, event.SideUnknown, event.StatusDown, "No DNS answer in time"},
	DNSNoAddress:    {event.LayerDNS, event.SideTheirs, event.StatusDown, "Name exists but has no A/AAAA record"},
	TCPRefused:      {event.LayerTCP, event.SideTheirs, event.StatusDown, "Host reachable, nothing listening on the port"},
	TCPTimeout:      {event.LayerTCP, event.SideTheirs, event.StatusDown, "No answer, host down or filtered"},
	TCPUnreachable:  {event.LayerTCP, event.SideUnknown, event.StatusDown, "Network or host unreachable"},
	TLSExpired:      {event.LayerTLS, event.SideTheirs, event.StatusDown, "Certificate expired"},
	TLSNotYetValid:  {event.LayerTLS, event.SideUnknown, event.StatusDown, "Certificate not yet valid (check local clock)"},
	TLSHostMismatch: {event.LayerTLS, event.SideTheirs, event.StatusDown, "Certificate does not cover the hostname"},
	TLSUntrusted:    {event.LayerTLS, event.SideTheirs, event.StatusDown, "Unknown CA or self-signed"},
	TLSHandshake:    {event.LayerTLS, event.SideTheirs, event.StatusDown, "Protocol or cipher failure"},
	TLSExpiringSoon: {event.LayerTLS, event.SideTheirs, event.StatusDegraded, "Certificate expires soon"},
	HTTPBadGateway:  {event.LayerHTTP, event.SideTheirs, event.StatusDown, "502 or 504 from a proxy, upstream likely down"},
	HTTPUnavailable: {event.LayerHTTP, event.SideTheirs, event.StatusDown, "503, overloaded or maintenance"},
	HTTPServerError: {event.LayerHTTP, event.SideTheirs, event.StatusDown, "Other 5xx"},
	HTTPAuth:        {event.LayerHTTP, event.SideYours, event.StatusDown, "401 or 403, credentials rejected"},
	HTTPClientError: {event.LayerHTTP, event.SideTheirs, event.StatusDown, "Other unexpected 4xx"},
	HTTPRateLimited: {event.LayerHTTP, event.SideTheirs, event.StatusDegraded, "429 rate limited"},
	HTTPTimeout:     {event.LayerHTTP, event.SideTheirs, event.StatusDown, "Connected, but no response in time"},
	HTTPSlow:        {event.LayerHTTP, event.SideTheirs, event.StatusDegraded, "Response above latency threshold"},
	HTTPUnexpStatus: {event.LayerHTTP, event.SideTheirs, event.StatusDown, "Status not in expect_status"},
	HTTPUnexpRedir:  {event.LayerHTTP, event.SideTheirs, event.StatusDown, "Redirected to an unexpected host"},
	HTTPRedirLoop:   {event.LayerHTTP, event.SideTheirs, event.StatusDown, "More than 10 redirects"},
}

// LayerOf returns the layer of a code.
func LayerOf(c Code) event.Layer { return Taxonomy[c].Layer }

// IsLocal reports whether a code means the local side is broken.
func IsLocal(c Code) bool { return Taxonomy[c].Layer == event.LayerLocal }
