package web

import "net/http"

// HandlerWrapper has Wrap method which acts as a middleware by wrapping an http.Handler
// prepending and appending some additinonal logic wrapping the handler's ServeHTTP(w,r)
// and then returns a new http.Handler which can wrap another or can be wrapped by another
//
// Wrap runs at BOOT, once per route, while the route tree is built — not per
// request. Its error reports that the wrapper could not be built: a required
// component was never prepared, a field it was given is unusable. On failure
// return (nil, err), as any constructor does; the router stops there, so the
// wrappers outside this one never run against half-built state.
//
// The handler it returns is what serves requests, so the chain composes as
// before: http.Handler in, http.Handler out, to any depth.
//
// A wrapper that passes the next handler a ResponseWriter of its own must give
// that writer Unwrap() http.ResponseWriter: http.ResponseController reaches
// Flush, write deadlines and Hijack through it, so without it they stop
// working for every handler behind the wrapper — a stream among them.
type HandlerWrapper interface {
	Wrap(http.Handler) (http.Handler, error)
}

// HostBinder is implemented by a handler that must know which host it is
// serving, because it holds something per host. A HostDispatcher tells it: when the dispatcher is built, every handler in its
// table that is a HostBinder is told each host it was given for (BindHost),
// once per host, before any request is served.
//
// A handler listed under several hosts is bound to each of them. It then
// serves every request as the host the dispatcher chose for that request
// (RequestHost), so one handler value is right for all of its hosts, each
// with its own.
//
// BindHost is where a handler finds out whether it can serve the host at
// all: what it needs for that host is looked up here, and an error fails the
// boot, naming the host. A request for a host the handler was never bound to
// cannot be served correctly and must be refused, not guessed at.
//
// A handler that stands in front of another — a router in front of its
// routes' wrappers, a recovery in front of a router — passes BindHost on to
// what it fronts, or what is behind it is never bound.
type HostBinder interface {
	BindHost(host *Host) error
}
