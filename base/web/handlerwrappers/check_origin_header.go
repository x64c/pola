package handlerwrappers

import (
	"fmt"
	"net/http"

	"github.com/x64c/pola/base/errs"
	"github.com/x64c/pola/base/web"
	"github.com/x64c/pola/base/web/requests"
	"github.com/x64c/pola/base/web/responses"
)

// CheckOriginHeader checks the request's Origin header against an allowlist and
// rejects non-matching requests with 403. It is a CSRF defense layer: the
// Origin header is browser-set and not forgeable by cross-origin JavaScript
// (it's in the Fetch spec's forbidden-header list), so for browser-driven
// requests it's a reliable "which origin initiated this" signal.
//
// The name is literal: it checks the Origin HEADER specifically — not an
// abstract "same-origin" guarantee. There is no Referer fallback (see below).
//
// Allowlist: the origin of the host serving the request — the "origin" of
// that host's entry in .web-server.json. It is told, at boot, every host its
// router is listed under (web.HostBinder), and each request is checked
// against the origin of the host the request is for: on a router that serves
// two hosts it allows each host's own origin on that host's requests, and
// never the other's. ExtraOrigins adds additional allowed origins on every
// host (e.g. legacy hostnames during a migration, or another of the app's
// own hosts that posts here).
//
// Method scope:
//   - Default (Strict=false): safe methods (GET, HEAD, OPTIONS) pass through
//     unchecked. Safe methods shouldn't have side effects (so no CSRF risk),
//     and GETs are meant to be reachable cross-origin (links, bookmarks,
//     search engines) — enforcing origin on them would break normal web
//     navigation. Cross-origin READ of a GET response is already blocked by
//     the browser's Same-Origin Policy, so origin-checking GETs adds nothing.
//   - Strict (Strict=true): every method is checked, including safe ones.
//     Purpose: deliberately apply this middleware to endpoints you have
//     explicitly determined ALWAYS receive an Origin header — i.e. AJAX/
//     programmatic-only endpoints (fetch/XHR from your own SPA, JSON APIs)
//     that are never reached by direct browser navigation. For those, even
//     GETs carry Origin (the browser sets it on fetch/XHR), so enforcing on
//     all methods is both safe and tighter.
//     The dev makes this "always has Origin" determination per endpoint; the
//     middleware does not guess. DANGER: enabling Strict on a route reachable
//     by direct URL navigation (typed URL, bookmark, external link) blocks
//     legitimate visits — direct navigation sends no Origin, so it 403s.
//
// Single signal — Origin only:
// There is intentionally NO Referer fallback. Origin and Referer can carry
// different information (Origin goes "null" on cross-origin redirects and for
// opaque origins; Referer is subject to Referrer-Policy stripping). When both
// are present and non-null from a real browser, they agree — so Referer adds
// nothing. When they disagree, it's a forged/non-browser client or the browser
// deliberately signaling "untrusted" via Origin: null — consulting Referer
// there would override the browser's signal. So a missing or mismatched Origin
// fails closed (rejected); we do not look at Referer to rescue it.
//
// What it does NOT defend: non-browser clients (curl, HTTP libraries, attack
// tools) can forge the Origin header — this middleware only constrains
// browser-driven requests. That's exactly the CSRF threat model (the browser
// is the unwitting attack vehicle). Pair with CSRFToken for defense-in-depth:
// CSRFToken proves the request body was rendered by our origin's DOM,
// CheckOriginHeader proves the request was initiated from our origin.
//
// Scope it to cookie-session route groups (where CSRF applies). Bearer-token
// JSON APIs don't need it — tokens aren't auto-attached cross-origin.
//
// A host's "origin" is required by .web-server.json, so a missing one is a
// boot failure before this middleware is ever wired.
type CheckOriginHeader struct {
	ExtraOrigins []string
	Strict       bool // true = check all methods incl. GET/HEAD/OPTIONS. Default false = skip safe methods.
}

func (m *CheckOriginHeader) Wrap(inner http.Handler) (http.Handler, error) {
	for _, o := range m.ExtraOrigins {
		if o == "" {
			return nil, fmt.Errorf(`CheckOriginHeader: "" is not an origin — remove it from ExtraOrigins`)
		}
	}
	return &checkOriginHandler{m: m, inner: inner}, nil
}

// checkOriginHandler is one mounted CheckOriginHeader: the allowlist of each
// host it serves, and the handler behind it.
type checkOriginHandler struct {
	m       *CheckOriginHeader
	inner   http.Handler
	allowed perHost[map[string]struct{}]
}

var _ web.HostBinder = (*checkOriginHandler)(nil)

// BindHost builds the allowlist for host: its own origin and ExtraOrigins.
// The origin needs no check here: .web-server.json requires it, so the conf
// has already refused the boot if it is unusable.
func (h *checkOriginHandler) BindHost(host *web.Host) error {
	set := map[string]struct{}{host.Origin: {}}
	for _, o := range h.m.ExtraOrigins {
		set[o] = struct{}{}
	}
	h.allowed.bind(host, set)
	return nil
}

func (h *checkOriginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.m.Strict && requests.IsSafeMethod(r) {
		h.inner.ServeHTTP(w, r)
		return
	}
	set, ok := h.allowed.forRequest(r)
	if !ok {
		refuseUnbound(w, r, "CheckOriginHeader")
		return
	}
	origin := r.Header.Get("Origin")
	// Missing Origin always fails closed — independent of allowlist contents.
	if origin == "" {
		responses.WriteErrorJSON(w, http.StatusForbidden, errs.OriginNotAllowed.WithDetail("missing Origin header"))
		return
	}
	// Origin only — no Referer fallback.
	if _, ok := set[origin]; !ok {
		responses.WriteErrorJSON(w, http.StatusForbidden, errs.OriginNotAllowed.WithDetail("origin: "+origin))
		return
	}
	h.inner.ServeHTTP(w, r)
}
