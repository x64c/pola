package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Host is one host this app serves: one entry under "hosts" of a ServerConf.
// The entry's key is the host's Name, the word code picks the host by
// (ServerConf.Host); everything else about the host — where it is reached,
// how long its requests may take — is stated in the conf, so the same build
// serves another deployment's names untouched.
//
// A Host is used in one place at boot: it is the key of its handler in the
// table a HostDispatcher is built from (HostHandlers), which says which
// handler serves the Host's requests. A host has one handler; a handler may
// be given to several hosts. A handler that must know the hosts it serves is
// told them from that table (HostBinder).
//
// Both deadlines are REQUIRED-explicit, for the reason every ServerConf field
// is: a zero would mean "no limit". StartPaused is the one optional member:
// leaving it out has a single meaning, the host serves from boot.
type Host struct {
	Name string `json:"-"` // The entry's key under "hosts". Not read from the entry: whoever fills a ServerConf sets it to the key, and Validate refuses a conf where the two differ.

	Origin           string `json:"origin"`             // REQUIRED. The host's public origin as a browser sends it: scheme://host[:port], lowercase, no path, no default port (e.g. "https://example.com"). Its host name is what a request's Host is matched against.
	ReadTimeoutSecs  int    `json:"read_timeout_secs"`  // REQUIRED (> 0). Deadline for reading a request's BODY, counted from the moment the host is chosen (the headers are already read, under read_header_timeout_secs). Must exceed the slowest legitimate upload on the slowest supported connection. A request with no body gets no read deadline.
	WriteTimeoutSecs int    `json:"write_timeout_secs"` // REQUIRED (> 0). Deadline from the moment the host is chosen to the last response byte, so it bounds HANDLER time too. Must exceed the slowest legitimate response (report/PDF generation, long polls, streamed output).

	// StartPaused says the host does not serve when the app boots. Optional;
	// absent or false, the host serves from boot. True, it starts PAUSED: its
	// routes are declared and what is its own is prepared as for any host, but
	// its requests are answered 503 HostPaused until it is resumed
	// (HostDispatcher.Resume) — to bring hosts up one at a time, or to hold one
	// back until the others are checked. It is the host's state AT BOOT and
	// nothing more: after that the host is paused or resumed by whoever holds
	// the dispatcher, and a restart returns the host to what is written here.
	StartPaused bool `json:"start_paused"`
}

// HostHandlers is the table a HostDispatcher is built from
// (NewHostDispatcher): for each Host, the handler that serves its requests —
// usually a router, bare or inside something that wraps the whole host (a
// panic recovery). It is the one place that says which handler serves which
// host: each host has exactly one, and the same handler may stand under
// several hosts, which then serve the same routes. It must name every host of
// the conf, and only those; the keys are the conf's own Hosts
// (ServerConf.Host), not copies.
type HostHandlers map[*Host]http.Handler

// validate checks one "hosts" entry against the key it sits under.
//
//   - Returns: the host name requests are matched against; an error naming the
//     entry when a field is unset or cannot describe what it names.
//   - Uses: originHostname.
func (h *Host) validate(key string) (string, error) {
	if h == nil {
		return "", fmt.Errorf("hosts.%s is null", key)
	}
	if h.Name != key {
		return "", fmt.Errorf("hosts.%s carries the name %q: set each Host's Name to its key before the conf is used", key, h.Name)
	}
	hostname, err := originHostname(h.Origin)
	if err != nil {
		return "", fmt.Errorf("hosts.%s.origin: %w", key, err)
	}
	if h.ReadTimeoutSecs <= 0 {
		return "", fmt.Errorf("hosts.%s.read_timeout_secs must be set (seconds > 0): got %d", key, h.ReadTimeoutSecs)
	}
	if h.WriteTimeoutSecs <= 0 {
		return "", fmt.Errorf("hosts.%s.write_timeout_secs must be set (seconds > 0): got %d", key, h.WriteTimeoutSecs)
	}
	return hostname, nil
}

// validHostName reports whether name can name a host: lowercase letters,
// digits, '-' and '_', at least one. A host's name is written into the keys
// of what is stored for that host, between ':' separators, so it is held to
// characters that mean nothing there.
func validHostName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// originHostname returns the host name of an origin, refusing anything a
// browser would never send as one: a browser serializes an origin as
// scheme://host[:port] — lowercase, no path, no default port — and the origin
// check compares that string exactly, so any other spelling would never match.
func originHostname(o string) (string, error) {
	if o == "" {
		return "", fmt.Errorf(`must be set (e.g. "https://example.com")`)
	}
	u, err := url.Parse(o)
	if err != nil {
		return "", fmt.Errorf("%q is not an origin: %w", o, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("%q is not an origin: want scheme://host[:port] with scheme http or https and nothing after", o)
	}
	if o != strings.ToLower(u.Scheme+"://"+u.Host) {
		return "", fmt.Errorf("%q is not an origin as a browser sends it: lowercase it", o)
	}
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return "", fmt.Errorf("%q names its scheme's default port, which a browser leaves out: drop it", o)
	}
	hostname := u.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("%q has no host name", o)
	}
	if strings.HasSuffix(hostname, ".") {
		return "", fmt.Errorf("%q ends its host name with a dot: drop it", o)
	}
	return hostname, nil
}

// requestHostname is the host name a request's Host header names, in the form
// the "hosts" table is keyed by: the port dropped, lowercase, one trailing dot
// dropped. It reads what the client (or the proxy in front) sent as Host and
// nothing else — never X-Forwarded-Host.
//
// It runs on every request, so it is written to allocate nothing for a Host
// as a browser or a proxy sends it (lowercase, with or without a port): the
// port is cut by hand rather than through net.SplitHostPort, which builds an
// error value for every Host that has no port. A Host that is not well formed
// is returned as it cannot match — it then names no listed host.
func requestHostname(hostport string) string {
	h := hostport
	if strings.HasPrefix(h, "[") {
		// an IPv6 literal: "[::1]" or "[::1]:port"; anything else after the bracket is not a Host
		end := strings.IndexByte(h, ']')
		if end < 0 || (end+1 < len(h) && h[end+1] != ':') {
			return h
		}
		h = h[1:end]
	} else if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i] // "host:port": a host name has no colon of its own
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
