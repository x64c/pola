package web

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/x64c/pola/base/web/requests"
)

// ServerConf is the HTTP server's own configuration.
//
// Every field is REQUIRED-explicit. Go's zero value for each of these means
// "no limit", or an address nobody chose — the very defect they close — so the
// framework rejects boot rather than decide on the operator's behalf. The
// server is assumed to stand alone: behind a proxy these deadlines are defence
// in depth, directly exposed they are the only defence, and the conf takes no
// position on which deployment it is in.
//
// The fields are split by what they can be known for. What applies before a
// request's Host is read, or to the connection or the process, is the
// server's and stands here. What describes one site — where it is reached,
// how long its requests may take — stands under that host in Hosts.
type ServerConf struct {
	Listen string `json:"listen"` // REQUIRED. Address the server binds, "ip:port".

	ReadHeaderTimeoutSecs int `json:"read_header_timeout_secs"` // REQUIRED (> 0). Deadline for reading the request headers. Bounds header-stall (slowloris) connections, which hold a socket without ever reaching a handler. The server's, not a host's: it runs while the headers — the Host among them — are still being read.
	IdleTimeoutSecs       int `json:"idle_timeout_secs"`        // REQUIRED (> 0). How long an idle keep-alive connection is kept open between requests. Shorter reclaims sockets from idle peers sooner; longer avoids a connection-pooling peer (browser, SDK, proxy upstream) racing to reuse a connection this server just closed. The server's: one connection can carry requests for several hosts.
	DrainTimeoutSecs      int `json:"drain_timeout_secs"`       // REQUIRED (> 0). Must be shorter than the time the service is given to terminate, or a full drain could never finish inside it. Graceful-drain window for Server.Shutdown on stop — the grace period in-flight requests get to finish. A request may outlive it (its host's write_timeout is the longer bound); when it closes, request contexts are canceled so handlers can unwind instead of being hard-killed.
	MaxHeaderBytes        int `json:"max_header_bytes"`         // REQUIRED (> 0). Upper bound on the request line plus all headers, per request (net/http enforces it with a small internal read slack, ~4 KiB). Unset, Go silently applies its own 1 MiB — a limit nobody chose and no conf states; the deployment states its number here (Go's 1048576 is a reasonable one).

	// TrustedProxyCIDRs lists the proxies allowed to state who the caller is.
	// Optional; absent or empty means trust nothing, and the client address is
	// then the connection's peer — honest, but behind an undeclared proxy every
	// request resolves to that proxy. Entries are CIDRs with the prefix length
	// written out ("127.0.0.1/32", "10.0.0.0/24", "::1/128"); a bare address is
	// rejected at boot rather than assumed. See web.ClientIPResolver for why the
	// rightmost forwarded entry is the trustworthy one.
	TrustedProxyCIDRs []string `json:"trusted_proxy_cidrs"`

	// Hosts is the hosts this app serves, keyed by the name code picks each one
	// by (Host). REQUIRED, at least one: an app serving a single host is the
	// one-entry case, not a different mode. A host is matched by its hostname
	// and its extra hostnames, and no hostname may lead to two hosts. A key is
	// lowercase letters, digits, '-' and '_': it is written into what is stored
	// per host, storage keys among it, so it may hold nothing a key gives
	// meaning to, and renaming a host orphans what was stored under the old
	// key.
	//
	// The table is also the app's allowlist of hostnames, and there is no way
	// around it: a request whose hostname is none of these is refused with 421
	// Misdirected Request. Nothing catches the rest. A hostname the app is to
	// answer to — a loopback address a caller on the same machine uses,
	// localhost on a developer's machine — is listed, in one of two ways: as an
	// extra hostname of the host it is to reach (Host.ExtraHostnames), when it
	// is to be served exactly as that host; or as the hostname of a host of its
	// own, with the handler that serves it, when it is to be served
	// differently.
	Hosts map[string]*Host `json:"hosts"`
}

// Validate checks this conf's own invariants. A relation to something outside
// it — the drain window against the time the service is given to terminate —
// belongs to the caller, the only place where both are known.
func (c ServerConf) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf(`listen must be set ("ip:port") in .web-server.json`)
	}
	if c.ReadHeaderTimeoutSecs <= 0 {
		return fmt.Errorf("read_header_timeout_secs must be set (seconds > 0) in .web-server.json: got %d", c.ReadHeaderTimeoutSecs)
	}
	if c.IdleTimeoutSecs <= 0 {
		return fmt.Errorf("idle_timeout_secs must be set (seconds > 0) in .web-server.json: got %d", c.IdleTimeoutSecs)
	}
	if c.DrainTimeoutSecs <= 0 {
		return fmt.Errorf("drain_timeout_secs must be set (seconds > 0) in .web-server.json: got %d", c.DrainTimeoutSecs)
	}
	if c.MaxHeaderBytes <= 0 {
		return fmt.Errorf("max_header_bytes must be set (bytes > 0) in .web-server.json: got %d — unset, Go would silently apply its own 1 MiB", c.MaxHeaderBytes)
	}
	if _, err := requests.NewClientIPResolver(c.TrustedProxyCIDRs); err != nil {
		return fmt.Errorf("trusted_proxy_cidrs in .web-server.json: %w (write the prefix length, e.g. 127.0.0.1/32)", err)
	}
	if _, err := c.hostnames(); err != nil {
		return fmt.Errorf("%w in .web-server.json", err)
	}
	return nil
}

// Host returns the host named name: the entry under "hosts" with that key.
// What code calls at boot to pick the host a router serves; a name the conf
// does not have is an error naming the ones it does.
func (c ServerConf) Host(name string) (*Host, error) {
	h, ok := c.Hosts[name]
	if !ok || h == nil {
		return nil, fmt.Errorf("no host %q in .web-server.json (hosts: %s)", name, c.hostNameList())
	}
	return h, nil
}

// hostnames validates every "hosts" entry and returns what a request's
// hostname is matched on: hostname → the entry it selects, for every host's
// hostname and extra hostnames. Hosts are walked in key order, so the first
// problem reported is the same on every run.
//
// A hostname may lead to one host only. Within a host validate has already
// refused a repeat; across hosts a shared hostname is refused here, naming
// both places it is written.
func (c ServerConf) hostnames() (map[string]*Host, error) {
	if len(c.Hosts) == 0 {
		return nil, fmt.Errorf(`hosts must be set: at least one host, e.g. {"main": {"hostname": "example.com", …}}`)
	}
	byHostname := make(map[string]*Host, len(c.Hosts))
	where := make(map[string]string, len(c.Hosts)) // hostname → the conf field it is written in
	for _, key := range slices.Sorted(maps.Keys(c.Hosts)) {
		if !validHostKey(key) {
			return nil, fmt.Errorf("hosts has the key %q: a host's key is lowercase letters, digits, '-' and '_', and not empty", key)
		}
		h := c.Hosts[key]
		hostnames, err := h.validate(key)
		if err != nil {
			return nil, err
		}
		for _, n := range hostnames {
			if other, dup := byHostname[n.hostname]; dup {
				return nil, fmt.Errorf("%s and %s are the same hostname %q: it could lead to only one of hosts %q and %q", where[n.hostname], n.field, n.hostname, other.Name, key)
			}
			byHostname[n.hostname] = h
			where[n.hostname] = n.field
		}
	}
	return byHostname, nil
}

// hostNameList is the configured host names for an error message.
func (c ServerConf) hostNameList() string {
	if len(c.Hosts) == 0 {
		return "none"
	}
	return strings.Join(slices.Sorted(maps.Keys(c.Hosts)), ", ")
}

// ClientIPResolver builds the resolver this conf describes. Validate has
// already rejected malformed entries, so this cannot fail.
func (c ServerConf) ClientIPResolver() requests.ClientIPResolver {
	rs, _ := requests.NewClientIPResolver(c.TrustedProxyCIDRs)
	return rs
}

// newHTTPServer builds the *http.Server this conf describes. One place maps
// conf fields to server fields; the service only runs what it is given.
//
// The server carries no ReadTimeout and no WriteTimeout: those are a host's,
// and the HostDispatcher sets them on each request once it has chosen the
// host. The handler given here is therefore always a HostDispatcher (Service
// takes no other) — behind any other handler a request would run with no read
// or write deadline at all.
//
// baseCtx becomes the parent of every request context. Its lifetime — not any
// conf value — decides when in-flight handlers are told to give up.
func (c ServerConf) newHTTPServer(addr string, handler http.Handler, baseCtx context.Context) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: secs(c.ReadHeaderTimeoutSecs),
		IdleTimeout:       secs(c.IdleTimeoutSecs),
		MaxHeaderBytes:    c.MaxHeaderBytes,
	}
}

// drainTimeout is the Server.Shutdown budget this conf describes.
func (c ServerConf) drainTimeout() time.Duration {
	return secs(c.DrainTimeoutSecs)
}

func secs(n int) time.Duration {
	return time.Duration(n) * time.Second
}
