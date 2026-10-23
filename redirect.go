package requests

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// sensitiveHeaders are headers that should be stripped when redirecting across hosts or
// downgrading from HTTPS to HTTP.
var sensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Cookie2",
	"Proxy-Authorization",
	"Www-Authenticate",
}

// RedirectPolicy applies redirect behavior to outgoing requests.
type RedirectPolicy interface {
	// Apply applies the redirect policy to req based on the prior requests in via.
	Apply(req *http.Request, via []*http.Request) error
}

// ProhibitRedirectPolicy is a redirect policy that does not allow any redirects.
type ProhibitRedirectPolicy struct {
}

// NewProhibitRedirectPolicy creates a new ProhibitRedirectPolicy that prevents any redirects.
func NewProhibitRedirectPolicy() *ProhibitRedirectPolicy {
	return &ProhibitRedirectPolicy{}
}

// Apply rejects all redirects by returning ErrAutoRedirectDisabled.
func (p *ProhibitRedirectPolicy) Apply(_ *http.Request, _ []*http.Request) error {
	return ErrAutoRedirectDisabled
}

// AllowRedirectPolicy is a redirect policy that allows a flexible number of redirects.
type AllowRedirectPolicy struct {
	maxRedirects int
}

// NewAllowRedirectPolicy creates a new AllowRedirectPolicy that allows up to the specified maximum number of redirects.
func NewAllowRedirectPolicy(maxRedirects int) *AllowRedirectPolicy {
	return &AllowRedirectPolicy{maxRedirects: maxRedirects}
}

// Apply allows redirects up to the configured limit, returning ErrTooManyRedirects if exceeded.
func (a *AllowRedirectPolicy) Apply(req *http.Request, via []*http.Request) error {
	if len(via) >= a.maxRedirects {
		return fmt.Errorf("stopped after %d redirects: %w", a.maxRedirects, ErrTooManyRedirects)
	}
	stripSensitiveHeadersOnRedirect(req, via[0])
	return nil
}

// hostname extracts the hostname from a host string, removing any port number.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// HostRedirectPolicy allows redirects only to explicitly listed hosts.
type HostRedirectPolicy struct {
	allowedHosts map[string]bool
}

// NewHostRedirectPolicy creates an exact host allowlist; subdomains are not implied.
func NewHostRedirectPolicy(hosts ...string) *HostRedirectPolicy {
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		allowed[strings.ToLower(host)] = true
	}
	return &HostRedirectPolicy{allowedHosts: allowed}
}

// Apply checks whether the redirect target matches an allowed host.
func (s *HostRedirectPolicy) Apply(req *http.Request, _ []*http.Request) error {
	if !s.allowedHosts[hostname(req.URL.Host)] {
		return ErrRedirectNotAllowed
	}
	return nil
}

// stripSensitiveHeaders removes sensitive headers from the given header map.
func stripSensitiveHeaders(h http.Header) {
	for _, header := range sensitiveHeaders {
		h.Del(header)
	}
}

func stripSensitiveHeadersOnRedirect(cur *http.Request, pre *http.Request) {
	if sameOrigin(cur.URL, pre.URL) {
		return
	}

	stripSensitiveHeaders(cur.Header)
}

func sameOrigin(left, right *url.URL) bool {
	leftHostname, leftOK := canonicalHostname(left)
	rightHostname, rightOK := canonicalHostname(right)

	return strings.EqualFold(left.Scheme, right.Scheme) &&
		leftOK && rightOK &&
		leftHostname == rightHostname &&
		effectivePort(left) == effectivePort(right)
}

func canonicalHostname(u *url.URL) (string, bool) {
	hostname := u.Hostname()
	if hostname == "" {
		return "", false
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		return addr.String(), true
	}

	hostname, err := idna.Lookup.ToASCII(hostname)
	if err != nil {
		return "", false
	}
	return strings.ToLower(hostname), true
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}

	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}
