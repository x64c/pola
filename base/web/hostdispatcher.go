package web

import (
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x64c/pola/base/errs"
	"github.com/x64c/pola/base/web/responses"
)

// HostDispatcher is the handler the web service serves. It reads a request's
// Host, picks the host of the conf it names, sets that host's deadlines on
// the request, and hands the request to that host's handler. It routes
// nothing itself: each host's handler has its own routing table, and nothing
// is shared between two hosts unless the app gave both the same handler.
//
// A request whose Host is no listed host goes nowhere: 421 Misdirected
// Request, the status for a host this server is not configured for. The
// conf's hosts are the whole of what the dispatcher answers to.
//
// A host can be PAUSED (Pause, Resume). While it is, everything that relies on
// the host is paused: the dispatcher answers its requests itself, 503, and
// the host's handler is not reached; and what was already inside the host
// and would not end by itself — an open stream — is told to end
// (HostPauseNoticeReceiver). It is a pause and nothing more — the host's
// handler, its routing table, whatever it holds all stay as they are, an
// ordinary request already inside it finishes, and Resume has it serving
// again at once. The pause is this process's and lasts no
// longer than it: a restart returns every host to how its conf says it
// starts — serving, or paused (Host.StartPaused).
//
// It is built once, at boot, from the conf and the handlers the app returned
// (NewHostDispatcher). After that its tables are read-only, and a host's
// pause is the one thing that changes — atomically — so it is safe for any
// number of concurrent requests.
type HostDispatcher struct {
	byHostname map[string]*hostRoute // what a request's Host is matched on (requestHostname)
	byName     map[string]*hostRoute // the same routes by host name (Host.Name), for Pause and Resume
}

// hostRoute is one host as the dispatcher uses it: where its requests go, the
// deadlines they run under, and whether the host is paused.
type hostRoute struct {
	host         *Host
	handler      http.Handler
	readTimeout  time.Duration
	writeTimeout time.Duration

	paused         atomic.Bool  // Pause / Resume
	retryAfterSecs atomic.Int64 // what a paused host's 503 says in Retry-After; 0 = says nothing

	// The pause notice: closed while the host is paused, open while it serves.
	// A pause closes the one in place; a resume puts a fresh one in place, so
	// what begins after the resume is not told of the pause before it.
	mu           sync.Mutex    // guards notice and noticeClosed, and orders Pause against Resume
	notice       chan struct{} // the notice in place
	noticeClosed bool
}

// pause and resume move the host between its two states, the flag requests
// are judged by and the notice long-lived work waits on together.
func (rt *hostRoute) pause(retryAfterSecs int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.retryAfterSecs.Store(retryAfterSecs)
	rt.paused.Store(true)
	if !rt.noticeClosed {
		close(rt.notice)
		rt.noticeClosed = true
	}
}

func (rt *hostRoute) resume() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.paused.Store(false)
	rt.retryAfterSecs.Store(0)
	if rt.noticeClosed {
		rt.notice = make(chan struct{})
		rt.noticeClosed = false
	}
}

// pauseNotice is the notice in place: closed already if the host is paused.
func (rt *hostRoute) pauseNotice() <-chan struct{} {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.notice
}

// HostState is one host as HostStates reports it: a snapshot, not a handle.
type HostState struct {
	Name       string
	Origin     string
	Paused     bool
	RetryAfter time.Duration // what a paused host tells clients (Retry-After); 0 = nothing
}

// NewHostDispatcher builds the dispatcher for conf's hosts, each served by its
// handler in handlers. The two must match one to one, and a mismatch is a
// boot failure naming both sides: a host with no handler would answer 421
// while the conf says it is served, and a handler for a host the conf does
// not have would never be reached.
//
//   - Returns: the dispatcher; an error when conf is invalid, when a key of
//     handlers is not a host of conf, when a handler is nil, or when a host of
//     conf has no handler.
//   - Uses: ServerConf.hostnames.
func NewHostDispatcher(conf ServerConf, handlers HostHandlers) (*HostDispatcher, error) {
	byHostname, err := conf.hostnames()
	if err != nil {
		return nil, err
	}
	for h, handler := range handlers {
		if h == nil {
			return nil, fmt.Errorf("web hosts: a handler was given for a nil Host")
		}
		if conf.Hosts[h.Name] != h {
			return nil, fmt.Errorf("web hosts: a handler was given for a Host named %q that is not a host of this conf (hosts: %s) — take each Host from the conf (ServerConf.Host)", h.Name, conf.hostNameList())
		}
		if handler == nil {
			return nil, fmt.Errorf("web hosts: host %q was given a nil handler", h.Name)
		}
	}
	var missing []string
	for name, h := range conf.Hosts {
		if _, ok := handlers[h]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("web hosts: no handler for host(s) %s — every host of the conf needs one in the HostHandlers", strings.Join(missing, ", "))
	}

	d := &HostDispatcher{
		byHostname: make(map[string]*hostRoute, len(byHostname)),
		byName:     make(map[string]*hostRoute, len(byHostname)),
	}
	for hostname, h := range byHostname {
		rt := &hostRoute{
			host:         h,
			handler:      handlers[h],
			readTimeout:  secs(h.ReadTimeoutSecs),
			writeTimeout: secs(h.WriteTimeoutSecs),
			notice:       make(chan struct{}),
		}
		if h.StartPaused { // the conf's word on how the host starts; Pause and Resume take it from there
			rt.pause(0)
		}
		d.byHostname[hostname] = rt
		d.byName[h.Name] = rt
	}
	// Each handler is told the hosts it serves, in host-name order so the
	// first failure reported is the same on every run. A handler that cannot
	// serve a host it was listed under fails the boot here.
	for _, name := range slices.Sorted(maps.Keys(d.byName)) {
		rt := d.byName[name]
		if b, ok := rt.handler.(HostBinder); ok {
			if err := b.BindHost(rt.host); err != nil {
				return nil, fmt.Errorf("web hosts: host %q: %w", name, err)
			}
		}
	}
	return d, nil
}

// Pause pauses the host named name: from now on its requests are answered
// 503 HostPaused by the dispatcher, before the host's handler — so no route
// is matched and no wrapper runs — and the host's pause notice is given
// (HostPauseNoticeReceiver), which ends the streams open on it. An ordinary
// request already inside the handler is not touched; it finishes. Pausing a
// paused host changes nothing but retryAfter.
//
// retryAfter is what the 503 tells clients in Retry-After, in whole seconds
// (rounded up). Zero tells them nothing: how long a pause will last is the
// operator's to say, and the dispatcher does not guess it. It is only said,
// never acted on — the host stays paused until Resume.
//
//   - Returns: nil; an error when the dispatcher has no host of that name, or
//     retryAfter is negative.
func (d *HostDispatcher) Pause(name string, retryAfter time.Duration) error {
	rt, ok := d.byName[name]
	if !ok {
		return fmt.Errorf("no host %q (hosts: %s)", name, d.hostNameList())
	}
	if retryAfter < 0 {
		return fmt.Errorf("retry-after must not be negative: got %v", retryAfter)
	}
	rt.pause(int64((retryAfter + time.Second - 1) / time.Second))
	return nil
}

// Resume ends the pause of the host named name: its requests go to its
// handler again, at once. Resuming a host that is not paused changes nothing.
//
//   - Returns: nil; an error when the dispatcher has no host of that name.
func (d *HostDispatcher) Resume(name string) error {
	rt, ok := d.byName[name]
	if !ok {
		return fmt.Errorf("no host %q (hosts: %s)", name, d.hostNameList())
	}
	rt.resume()
	return nil
}

// HostStates reports every host and whether it is paused, in host-name order.
func (d *HostDispatcher) HostStates() []HostState {
	states := make([]HostState, 0, len(d.byName))
	for _, name := range slices.Sorted(maps.Keys(d.byName)) {
		rt := d.byName[name]
		states = append(states, HostState{
			Name:       name,
			Origin:     rt.host.Origin,
			Paused:     rt.paused.Load(),
			RetryAfter: time.Duration(rt.retryAfterSecs.Load()) * time.Second,
		})
	}
	return states
}

// route is the host a request naming hostport as its Host is served by: the
// listed host of that name, or nil — no host is served under a name the conf
// does not list. The one place the choice is made, so the dispatch and the
// pause notice of a request can never come to name two hosts.
func (d *HostDispatcher) route(hostport string) *hostRoute {
	return d.byHostname[requestHostname(hostport)]
}

// hostNameList is the dispatcher's host names for an error message.
func (d *HostDispatcher) hostNameList() string {
	return strings.Join(slices.Sorted(maps.Keys(d.byName)), ", ")
}

// ServeHTTP picks the request's host and serves it under that host's
// deadlines.
//
// The deadlines are set here because here is the first moment the host is
// known; the server itself carries none (ServerConf.newHTTPServer). They are
// the request's alone — the server arms each request afresh — so one host's
// never reach the next request on the same connection.
//
// The WRITE deadline is set on every request: it bounds the handler.
//
// The READ deadline is set only while a body remains to be read. On a request
// with no body, net/http is already watching the connection for the client
// going away, and a read deadline would be read as exactly that — the
// request's context canceled mid-handler, at the deadline, on a healthy
// connection. With a body, net/http drops the deadline itself once the body
// has been read to its end, so the deadline bounds the upload and nothing
// after it.
//
// A request refused with 421, or answered 503 for a paused host, gets no
// deadline of its own: no handler runs and nothing is read, and the answer is
// a few bytes.
func (d *HostDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := d.route(r.Host)
	if rt == nil {
		responses.WriteErrorJSON(w, http.StatusMisdirectedRequest, errs.MisdirectedRequest.WithDetail("host: "+r.Host))
		return
	}

	// A paused host: answered here, before anything of the host is reached.
	if rt.paused.Load() {
		if secs := rt.retryAfterSecs.Load(); secs > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		}
		responses.WriteErrorJSON(w, http.StatusServiceUnavailable, errs.HostPaused)
		return
	}

	// A deadline that cannot be set is a request that would run unbounded, so
	// it is refused rather than served. The real connection always supports
	// both; this can only be a ResponseWriter that hides it — a wrapper around
	// the dispatcher that passes on a writer of its own without Unwrap.
	rc := http.NewResponseController(w)
	now := time.Now()
	if err := rc.SetWriteDeadline(now.Add(rt.writeTimeout)); err != nil {
		log.Printf("[ERROR][WebService] host %q: write deadline not set, request refused: %v", rt.host.Name, err)
		responses.WriteErrorJSON(w, http.StatusInternalServerError, errs.InternalError)
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		if err := rc.SetReadDeadline(now.Add(rt.readTimeout)); err != nil {
			log.Printf("[ERROR][WebService] host %q: read deadline not set, request refused: %v", rt.host.Name, err)
			responses.WriteErrorJSON(w, http.StatusInternalServerError, errs.InternalError)
			return
		}
	}

	rt.handler.ServeHTTP(w, r)
}
