package web

import (
	"fmt"
	"net/http"
)

// Host is one host this app serves: one entry under "hosts" of a ServerConf.
// The entry's key is the host's Name, the word code picks the host by
// (ServerConf.Host); everything else about the host — the hostnames it is
// reached by, how long its requests may take — is stated in the conf, so the
// same build serves another deployment's names untouched.
//
// A request belongs to a host by its hostname alone: the HostDispatcher takes
// the hostname out of the request's authority (the Host header, the port
// dropped) and matches it against each host's Hostname and ExtraHostnames.
// A host has no origin: several origins — http and https, other ports — can
// sit on one hostname.
//
// A Host is used in one place at boot: it is the key of its handler in the
// table a HostDispatcher is built from (HostHandlers), which says which
// handler serves the Host's requests. A host has one handler; a handler may
// be given to several hosts. A handler that must know the hosts it serves is
// told them from that table (HostBinder).
//
// Both deadlines are REQUIRED-explicit, for the reason every ServerConf field
// is: a zero would mean "no limit". ExtraHostnames and StartPaused are the
// optional members: leaving either out has a single meaning — the host is
// reached by its hostname alone, and it serves from boot.
type Host struct {
	Name string `json:"-"` // The entry's key under "hosts". Not read from the entry: whoever fills a ServerConf sets it to the key, and Validate refuses a conf where the two differ.

	// Hostname is the name the host really has, as a request's Host header
	// carries it once the port is dropped: "example.com", "127.0.0.1",
	// "::1". REQUIRED. A request whose hostname is this one is this host's
	// request. Written as validateHostname requires: lowercase (an
	// international name in its punycode form), no scheme, no port, no
	// trailing dot, an IPv6 address without brackets.
	Hostname string `json:"hostname"`

	// ExtraHostnames are other hostnames a request may carry to reach the
	// same host. Optional. Each is written as Hostname is, and a request
	// whose hostname is one of them is this host's request — the same
	// handler, the same deadlines, paused and resumed with the host. They
	// are extra ways in and nothing more: Hostname stays the name the host
	// really has. A loopback address a caller on the same machine reaches the
	// host by, or localhost on a developer's machine, is listed here under the
	// host it is to reach.
	ExtraHostnames []string `json:"extra_hostnames"`

	ReadTimeoutSecs  int `json:"read_timeout_secs"`  // REQUIRED (> 0). Deadline for reading a request's BODY, counted from the moment the host is chosen (the headers are already read, under read_header_timeout_secs). Must exceed the slowest legitimate upload on the slowest supported connection. A request with no body gets no read deadline.
	WriteTimeoutSecs int `json:"write_timeout_secs"` // REQUIRED (> 0). Deadline from the moment the host is chosen to the last response byte, so it bounds HANDLER time too. Must exceed the slowest legitimate response (report/PDF generation, long polls, streamed output).

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

// hostHostname is one hostname a host is matched by, and where in the conf it
// is written.
type hostHostname struct {
	hostname string
	field    string // e.g. "hosts.main.hostname", "hosts.main.extra_hostnames[0]"
}

// validate checks one "hosts" entry against the key it sits under.
//
//   - Returns: the host's hostnames — Hostname first, then ExtraHostnames —
//     each with where it is written; an error naming the entry when a field is
//     unset or cannot describe what it names, or a hostname is written twice.
//   - Uses: validateHostname.
func (h *Host) validate(key string) ([]hostHostname, error) {
	if h == nil {
		return nil, fmt.Errorf("hosts.%s is null", key)
	}
	if h.Name != key {
		return nil, fmt.Errorf("hosts.%s carries the name %q: set each Host's Name to its key before the conf is used", key, h.Name)
	}
	if err := validateHostname(h.Hostname); err != nil {
		return nil, fmt.Errorf("hosts.%s.hostname: %w", key, err)
	}
	hostnames := []hostHostname{{hostname: h.Hostname, field: "hosts." + key + ".hostname"}}
	seen := map[string]string{h.Hostname: hostnames[0].field}
	for i, n := range h.ExtraHostnames {
		field := fmt.Sprintf("hosts.%s.extra_hostnames[%d]", key, i)
		if err := validateHostname(n); err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if first, dup := seen[n]; dup {
			return nil, fmt.Errorf("%s repeats %q, already %s: list each hostname once", field, n, first)
		}
		seen[n] = field
		hostnames = append(hostnames, hostHostname{hostname: n, field: field})
	}
	if h.ReadTimeoutSecs <= 0 {
		return nil, fmt.Errorf("hosts.%s.read_timeout_secs must be set (seconds > 0): got %d", key, h.ReadTimeoutSecs)
	}
	if h.WriteTimeoutSecs <= 0 {
		return nil, fmt.Errorf("hosts.%s.write_timeout_secs must be set (seconds > 0): got %d", key, h.WriteTimeoutSecs)
	}
	return hostnames, nil
}

// validHostKey reports whether key can be a host's key under "hosts" (its
// Name): lowercase letters, digits, '-' and '_', at least one. The key is
// written into the keys of what is stored for that host, between ':'
// separators, so it is held to characters that mean nothing there.
func validHostKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
