package handlerwrappers

import (
	"fmt"
	"net/http"
	"strings"

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
// What it allows:
//
//	hostname in Origin header == hostname in Host header   → the request's own page: allowed
//	Origin header in AlsoAllow (exact)                     → allowed
//	otherwise, or no Origin header                         → 403
//
// The two hostnames are compared as web.AuthorityHostname gives them (port
// dropped, lowercase, one trailing dot dropped); neither the scheme nor the
// port is compared. The hostname is the boundary that matters here: a cookie
// is scoped by hostname, not by port or scheme. The check needs nothing but
// the request itself, so it holds the same under any hostname a request comes
// in under, on any router.
//
// AlsoAllow: origins allowed IN ADDITION to the request's own hostname —
// pages on other hostnames that may send these requests (e.g. a form on
// another of the app's hosts, or on a sibling site, that posts here). Each
// entry is an origin as a browser sends it (web.ValidateOrigin) and is
// compared exactly, scheme and port included; anything else, or an entry
// listed twice, fails the route's registration, since it could never match.
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
// CSRFToken proves the request body was rendered by our own page,
// CheckOriginHeader proves the request was initiated from our own hostname.
//
// Scope it to cookie-session route groups (where CSRF applies). Bearer-token
// JSON APIs don't need it — tokens aren't auto-attached cross-origin.
type CheckOriginHeader struct {
	AlsoAllow []string // origins allowed in addition to the request's own hostname; see AlsoAllow above
	Strict    bool     // true = check all methods incl. GET/HEAD/OPTIONS. Default false = skip safe methods.
}

func (m *CheckOriginHeader) Wrap(inner http.Handler) (http.Handler, error) {
	seen := make(map[string]int, len(m.AlsoAllow))
	for i, o := range m.AlsoAllow {
		if err := web.ValidateOrigin(o); err != nil {
			return nil, fmt.Errorf("CheckOriginHeader: AlsoAllow[%d]: %w", i, err)
		}
		if first, dup := seen[o]; dup {
			return nil, fmt.Errorf("CheckOriginHeader: AlsoAllow[%d] repeats %q, already AlsoAllow[%d]: list each origin once", i, o, first)
		}
		seen[o] = i
	}
	allowed := make(map[string]struct{}, len(m.AlsoAllow))
	for _, o := range m.AlsoAllow {
		allowed[o] = struct{}{}
	}
	return &checkOriginHandler{m: m, inner: inner, alsoAllowed: allowed}, nil
}

// checkOriginHandler is one mounted CheckOriginHeader and the handler behind
// it.
type checkOriginHandler struct {
	m           *CheckOriginHeader
	inner       http.Handler
	alsoAllowed map[string]struct{} // AlsoAllow, as a set
}

func (h *checkOriginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.m.Strict && requests.IsSafeMethod(r) {
		h.inner.ServeHTTP(w, r)
		return
	}
	origin := r.Header.Get("Origin")
	// Missing Origin always fails closed — independent of AlsoAllow.
	if origin == "" {
		responses.WriteErrorJSON(w, http.StatusForbidden, errs.OriginNotAllowed.WithDetail("missing Origin header"))
		return
	}
	// Origin only — no Referer fallback.
	if _, also := h.alsoAllowed[origin]; !also && !ownPage(origin, r) {
		responses.WriteErrorJSON(w, http.StatusForbidden, errs.OriginNotAllowed.WithDetail("origin: "+origin))
		return
	}
	h.inner.ServeHTTP(w, r)
}

// ownPage reports whether origin, a request's Origin header, names a page on
// the hostname the request was sent to: hostname in Origin header == hostname
// in Host header. An Origin that is not scheme://authority with scheme http or
// https and nothing after — "null" among them — names no page of ours.
func ownPage(origin string, r *http.Request) bool {
	scheme, authority, ok := strings.Cut(origin, "://")
	if !ok || (scheme != "http" && scheme != "https") || authority == "" || strings.ContainsAny(authority, "/?#@") {
		return false
	}
	hostname := web.AuthorityHostname(authority)
	return hostname != "" && hostname == web.AuthorityHostname(r.Host)
}
