package webhook

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxWebhookURLLen      = 2048
	resolveTimeout        = 5 * time.Second
	connectTimeout        = 5 * time.Second
	tlsHandshakeTimeout   = 5 * time.Second
	responseHeaderTimeout = 10 * time.Second
	headerCaptureLimit    = 8 * 1024
	bodyCaptureLimit      = 64 * 1024
	maxRetryAfterDelay    = 24 * time.Hour
	truncationMarker      = "…[truncated at 65536 bytes]"
	webhookUserAgent      = "rukie-webhooks/1.0"
	webhookContentType    = "application/json"
)

// Policy gates webhook egress. The zero value denies private targets and
// insecure HTTP. A nil *Policy behaves as the zero value.
type Policy struct {
	Resolve             func(ctx context.Context, host string) ([]netip.Addr, error)
	ExtraDenyCIDRs      []string
	AllowInsecureHTTP   bool
	AllowPrivateTargets bool
}

type policyError struct {
	reason string
}

func (e *policyError) Error() string { return "denied by egress policy: " + e.reason }

var denyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// testBypassPrefixes are the only ranges AllowPrivateTargets bypasses:
// loopback and RFC 1918. Every other denied range (link-local, CGNAT, ULA,
// multicast, reserved, documentation, NAT64, ...) still denies under the flag,
// so hermetic tests can use loopback/RFC 1918 without opening the metadata
// or multicast surface.
var testBypassPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// isTestBypassable reports whether ip is in the narrow bypass set above.
// Callers normalize zones and IPv4-mapped forms first; this normalizes again
// defensively so both forms behave identically.
func isTestBypassable(ip netip.Addr) bool {
	addr := ip.WithZone("")
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	for _, prefix := range testBypassPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// CheckURL validates rawURL and returns its hostname for resolution and SNI.
func (p *Policy) CheckURL(rawURL string) (string, error) {
	if len(rawURL) == 0 || len(rawURL) > maxWebhookURLLen {
		return "", fmt.Errorf("CheckURL: invalid webhook url: length must be 1..%d", maxWebhookURLLen)
	}
	if strings.IndexFunc(rawURL, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("CheckURL: invalid webhook url: control characters rejected")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("CheckURL: invalid webhook url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if p == nil || !p.AllowInsecureHTTP {
			return "", fmt.Errorf("CheckURL: invalid webhook url: http endpoints disabled")
		}
	default:
		return "", fmt.Errorf("CheckURL: invalid webhook url: scheme must be https")
	}
	if u.User != nil {
		return "", fmt.Errorf("CheckURL: invalid webhook url: userinfo rejected")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("CheckURL: invalid webhook url: empty host")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("CheckURL: invalid webhook url: bad port")
		}
	}
	return host, nil
}

// ResolveAndCheck resolves every address of host and denies the delivery
// unless each address passes the deny list. IP literals skip resolution.
func (p *Policy) ResolveAndCheck(ctx context.Context, host string) ([]netip.Addr, error) {
	trimmed := host
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		trimmed = trimmed[1 : len(trimmed)-1]
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(trimmed); err == nil {
		addrs = []netip.Addr{ip.WithZone("")}
	} else {
		resolve := p.resolveFunc()
		resolved, err := resolve(ctx, trimmed)
		if err != nil {
			return nil, &policyError{reason: "resolve " + trimmed + ": " + err.Error()}
		}
		addrs = resolved
	}
	if len(addrs) == 0 {
		return nil, &policyError{reason: "no addresses for " + trimmed}
	}
	extra, err := p.extraPrefixes()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		ip := addr.WithZone("")
		if ip.Is4In6() {
			ip = ip.Unmap()
		}
		if p != nil && p.AllowPrivateTargets && isTestBypassable(ip) {
			continue
		}
		for _, prefix := range denyPrefixes {
			if prefix.Contains(ip) {
				return nil, &policyError{reason: "address " + ip.String() + " is denied"}
			}
		}
		for _, prefix := range extra {
			if prefix.Contains(ip) {
				return nil, &policyError{reason: "address " + ip.String() + " is denied"}
			}
		}
	}
	return addrs, nil
}

// DialValidated dials the validated IP literal after a connect-time deny
// recheck. serverName carries the original hostname; the TLS caller applies
// it as SNI while the socket stays pinned to ip.
func (p *Policy) DialValidated(ctx context.Context, network string, ip netip.Addr, port string, serverName string) (net.Conn, error) {
	_ = serverName
	if err := checkDenied(p, ip); err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("DialValidated: invalid webhook url: bad port")
	}
	dialer := &net.Dialer{Timeout: connectTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}

// NewClient returns an HTTP client that resolves once per request, dials
// the validated IP, sends no proxy traffic, and never follows redirects.
func (p *Policy) NewClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           p.dialContext,
			DialTLSContext:        p.dialTLSContext,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Policy) resolveFunc() func(ctx context.Context, host string) ([]netip.Addr, error) {
	if p != nil && p.Resolve != nil {
		return p.Resolve
	}
	return defaultResolve
}

func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	found, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("defaultResolve: %w", err)
	}
	addrs := make([]netip.Addr, 0, len(found))
	for _, ip := range found {
		if addr, ok := netip.AddrFromSlice(ip.IP); ok {
			addrs = append(addrs, addr.WithZone(""))
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("defaultResolve: no addresses for %s", host)
	}
	return addrs, nil
}

func (p *Policy) extraPrefixes() ([]netip.Prefix, error) {
	if p == nil {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(p.ExtraDenyCIDRs))
	for _, cidr := range p.ExtraDenyCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("extraPrefixes: bad extra deny cidr %q", cidr)
		}
		out = append(out, prefix)
	}
	return out, nil
}

func checkDenied(p *Policy, ip netip.Addr) error {
	extra, err := p.extraPrefixes()
	if err != nil {
		return err
	}
	addr := ip.WithZone("")
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if p != nil && p.AllowPrivateTargets && isTestBypassable(addr) {
		return nil
	}
	for _, prefix := range denyPrefixes {
		if prefix.Contains(addr) {
			return &policyError{reason: "address " + addr.String() + " is denied"}
		}
	}
	for _, prefix := range extra {
		if prefix.Contains(addr) {
			return &policyError{reason: "address " + addr.String() + " is denied"}
		}
	}
	return nil
}

func (p *Policy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, &policyError{reason: "bad dial address " + addr}
	}
	addrs, err := p.ResolveAndCheck(ctx, host)
	if err != nil {
		return nil, err
	}
	return p.DialValidated(ctx, network, addrs[0], port, host)
}

func (p *Policy) dialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := p.dialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		_ = raw.Close()
		return nil, &policyError{reason: "bad dial address " + addr}
	}
	conn := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS13})
	handshake, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	if err := conn.HandshakeContext(handshake); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
