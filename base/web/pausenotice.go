package web

import "net/http"

// hostDispatcherKey is the private context key under which a serving Service
// stores its HostDispatcher, for HostPauseNoticeReceiver.
type hostDispatcherKey struct{}

// HostPauseNoticeReceiver returns the receive end of the pause notice of the
// host serving r: a channel closed when that host is paused
// (Service.PauseHost). ok is false outside a request a web Service serves.
//
// A paused host pauses everything that relies on it. The dispatcher sees to
// the requests that arrive; this is for what is already inside the host and
// would not end by itself. An ordinary handler ignores it and finishes as
// before. A long-lived handler (a stream) selects on it to finish now, beside
// the drain notice (DrainNoticeReceiverFromContext): its client then comes
// back to the 503 every other request to the host gets, until the host is
// resumed.
//
// The channel is the one in place when it is asked for: asked while the host
// is paused, it is closed already; asked after a resume, it is a fresh one,
// which the pause before it does not close. It cancels nothing by itself.
//
// It takes the request, not its context, because the host is found the way
// the dispatcher found it — from the request's Host. That is one lookup,
// made by the handler that wants the notice, instead of a value carried on
// the context of every request.
func HostPauseNoticeReceiver(r *http.Request) (<-chan struct{}, bool) {
	rt := requestRoute(r)
	if rt == nil {
		return nil, false
	}
	return rt.pauseNotice(), true
}

// RequestHost returns the host serving r: the one the dispatcher chose for it
// from its Host. ok is false outside a request a web Service serves.
//
// It is how a handler listed under several hosts knows which of them a
// request is for. Like the pause notice, it is one lookup made by the handler
// that asks, not a value carried on every request's context.
func RequestHost(r *http.Request) (*Host, bool) {
	rt := requestRoute(r)
	if rt == nil {
		return nil, false
	}
	return rt.host, true
}

// requestRoute is the dispatcher's route for r, found the way the dispatcher
// found it; nil outside a request a web Service serves.
func requestRoute(r *http.Request) *hostRoute {
	d, ok := r.Context().Value(hostDispatcherKey{}).(*HostDispatcher)
	if !ok || d == nil {
		return nil
	}
	return d.route(r.Host)
}
