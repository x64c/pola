package web

import (
	"fmt"
	"net"
	"strings"
)

// Three terms (RFC 9110):
//
//	hostname            hostname                  example.com        what the HostDispatcher matches
//	authority           hostname + port           example.com:8443   where a request goes TO: its Host header
//	origin     scheme + hostname + port   https://example.com:8443   where a request comes FROM (origin.go)
//
// The AUTHORITY is what a client puts in a request's Host header (HTTP/2 and
// HTTP/3: :authority), built from the URL it requests: the hostname, and the
// port when it is not the scheme's default. It is the client's own claim — a
// browser builds it from the URL and lets no page change it, any other client
// may send what it likes — so it chooses among the hosts the app lists and
// proves nothing more. It arrives with every request and is not configured
// anywhere: the dispatcher only reduces it to its hostname (AuthorityHostname).
//
// The HOSTNAME is the authority without its port. The HostDispatcher matches
// on it and on nothing else: a host is reached by its hostname
// (Host.Hostname) and its extra hostnames (Host.ExtraHostnames). The port is
// left out because it says how a request travelled — through a proxy's 443,
// or straight to the app's own port — not which host it is for; the scheme is
// not seen at all, since behind a proxy that ends TLS every request reaches the
// app over plain HTTP.

// AuthorityHostname is the hostname in an authority — the value of a Host
// header (r.Host, which Go fills from the Host header or :authority), or the
// part of an origin after its scheme — in the form hosts are matched by: the
// port dropped, lowercase, one trailing dot dropped, an IPv6 address without
// its brackets. For a request it reads what the client (or the proxy in
// front) sent as Host and nothing else — never X-Forwarded-Host.
//
// It runs on every request, so it is written to allocate nothing for an
// authority as a browser or a proxy sends it (lowercase, with or without a
// port): the port is cut by hand rather than through net.SplitHostPort, which
// builds an error value for every authority that has no port. An authority
// that is not well formed is returned as it is: it then equals no hostname.
func AuthorityHostname(authority string) string {
	h := authority
	if strings.HasPrefix(h, "[") {
		// an IPv6 literal: "[::1]" or "[::1]:port"; anything else after the bracket is not an authority
		end := strings.IndexByte(h, ']')
		if end < 0 || (end+1 < len(h) && h[end+1] != ':') {
			return h
		}
		h = h[1:end]
	} else if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i] // "hostname:port": a hostname has no colon of its own
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// validateHostname refuses n unless it is a hostname as AuthorityHostname gives
// one: a DNS name or an IPv4 address — lowercase letters, digits, '-', '_' and
// '.', no empty label, no trailing dot, an international name in its punycode
// form — or an IPv6 address in its canonical form, without brackets. No
// scheme and no port: a hostname is neither an origin nor an authority.
// Anything else could never equal the hostname of a request.
//
//   - Returns: nil; an error saying what is wrong with n.
func validateHostname(n string) error {
	if n == "" {
		return fmt.Errorf(`must be set (e.g. "example.com")`)
	}
	if strings.Contains(n, "://") {
		return fmt.Errorf("%q is an origin or a URL: write the hostname alone, without scheme, port or path", n)
	}
	if strings.HasPrefix(n, "[") {
		return fmt.Errorf("%q: write an IPv6 address without brackets and without a port", n)
	}
	if strings.Contains(n, ":") {
		ip := net.ParseIP(n)
		if ip == nil {
			return fmt.Errorf("%q is an authority, not a hostname: drop the port — a request's port is not matched", n)
		}
		if ip.String() != n {
			return fmt.Errorf("%q: write the IPv6 address in its canonical form %q, as a client sends it", n, ip.String())
		}
		return nil
	}
	if n != strings.ToLower(n) {
		return fmt.Errorf("%q: lowercase it — a request's hostname is matched lowercased", n)
	}
	if strings.HasSuffix(n, ".") {
		return fmt.Errorf("%q ends with a dot: drop it — one trailing dot of a request's hostname is dropped before it is matched", n)
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("%q has %q, which no hostname has: letters, digits, '-', '_' and '.' (an international name in its punycode form)", n, c)
		}
	}
	for _, label := range strings.Split(n, ".") {
		if label == "" {
			return fmt.Errorf("%q has an empty label", n)
		}
	}
	return nil
}
