package handlerwrappers

import (
	"fmt"
	"net/http"

	"github.com/x64c/pola/base/errs"
	"github.com/x64c/pola/base/framework"
	"github.com/x64c/pola/base/web"
	"github.com/x64c/pola/base/web/responses"
	"github.com/x64c/pola/base/web/session"
	"github.com/x64c/pola/base/web/session/cookie"
)

// CookieSession gates an endpoint on the cookie session protocol being in
// service. It rejects with 503 SessionServiceUnavailable when the protocol
// isn't serving — the session service is not admitted for use (its framework
// handle answers the lifecycle question), or the cookie protocol is disabled
// (the manager's own switch) — and otherwise passes the request through
// unchanged.
//
// It produces no SessionData. CookieUserSession is this gate plus user-identity
// resolution; use CookieSession on routes that depend on the cookie subsystem
// but carry no user identity — so a disabled/stopped protocol closes them up
// front instead of doing work that can't complete.
type CookieSession struct {
	AppProvider framework.AppProviderFunc
}

func (m *CookieSession) Wrap(inner http.Handler) (http.Handler, error) {
	appCore := m.AppProvider().AppCore()
	return &cookieSessionHandler{appCore: appCore, sessHandle: appCore.SessionHandle(), inner: inner}, nil
}

// cookieSessionHandler is one mounted CookieSession: the cookie session
// manager of each host it serves, and the handler behind it.
type cookieSessionHandler struct {
	appCore    *framework.Core
	sessHandle framework.ServiceHandle[*session.Service]
	inner      http.Handler
	mgrs       perHost[*cookie.SessionManager]
}

var _ web.HostBinder = (*cookieSessionHandler)(nil)

// BindHost takes host's cookie session manager: on a router that serves two
// hosts, each host's requests are gated on that host's manager.
func (h *cookieSessionHandler) BindHost(host *web.Host) error {
	mgr, err := cookieSessionManagerOfHost(h.appCore, host, "CookieSession")
	if err != nil {
		return err
	}
	h.mgrs.bind(host, mgr)
	return nil
}

func (h *cookieSessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mgr, ok := h.mgrs.forRequest(r)
	if !ok {
		refuseUnbound(w, r, "CookieSession")
		return
	}
	if _, ok := h.sessHandle.Get(); !ok || !mgr.Enabled() {
		responses.WriteErrorJSON(w, http.StatusServiceUnavailable, errs.SessionServiceUnavailable.WithDetail("cookie session"))
		return
	}
	h.inner.ServeHTTP(w, r)
}

// cookieSessionManagerOfHost returns the cookie session manager of host, for
// the wrapper named who: the manager PrepareCookieSessions attached for that
// host. It is what binds the cookie wrappers to a host — the sessions a
// wrapper serves on a request are those of the host the request is for,
// never another's.
//
// It runs at boot, before StartServices admits anything, so the manager is
// reached on the NODE plane — the handle would refuse Get() here. The
// per-request gate still asks the handle.
//
//   - Returns: the manager; an error naming who when the session service was
//     never prepared, or when host has no cookie sessions (no entry in
//     .web-cookie-session.json, or never prepared).
func cookieSessionManagerOfHost(appCore *framework.Core, host *web.Host, who string) (*cookie.SessionManager, error) {
	sessSvc, _ := appCore.SessionHandle().Node().Service().(*session.Service)
	if sessSvc == nil {
		return nil, fmt.Errorf("%s: session service missing — prepare it before wiring this middleware", who)
	}
	mgr := sessSvc.CookieSessionManagers[host.Name]
	if mgr == nil {
		return nil, fmt.Errorf("%s: host %q has no cookie sessions — it needs an entry in .web-cookie-session.json and a PrepareCookieSessions call, or this route must not be served on that host", who, host.Name)
	}
	return mgr, nil
}
