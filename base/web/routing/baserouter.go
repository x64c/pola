package routing

import (
	"fmt"
	"net/http"

	"github.com/x64c/pola/base/web"
)

// BaseRouter is the reference implementation of Router: routing through the
// embedded http.ServeMux, with wrapper composition on Handle, HandleFunc and
// Group. It recovers no panic itself; a recovery placed in the chain catches
// one.
//
// A router is a routing table and nothing more: it matches a request's method
// and path, never its Host, and it does not say which host it serves. That is
// said in one place, the web service's handler table (web.HostHandlers), where
// a router may be listed under one host or under several.
//
// What a router does for hosts is pass the word on (web.HostBinder): when the
// handler table is built, the router is told each host it is listed under,
// and tells every wrapper mounted on it that needs to know.
type BaseRouter struct {
	*http.ServeMux
	hostBinders []routeHostBinder // the wrappers mounted on this router that must be told its hosts, with the route each is on
}

// routeHostBinder is one mounted wrapper that must be told the router's hosts,
// and the route it is mounted on, for the error it may return.
type routeHostBinder struct {
	route  string
	binder web.HostBinder
}

var _ Router = (*BaseRouter)(nil)
var _ web.HostBinder = (*BaseRouter)(nil)

func (r *BaseRouter) Handle(pattern string, handler http.Handler, handlerWrappers ...web.HandlerWrapper) {
	// Built inner-first; a failure stops the chain there — and PANICS, by
	// design. See RouteGroup.Handle for the reasoning (stdlib parity,
	// boot-time fail-fast).
	wrappedHandler := handler
	// inline slices.Backward
	for i := len(handlerWrappers) - 1; i >= 0; i-- {
		wrapped, err := handlerWrappers[i].Wrap(wrappedHandler)
		if err != nil {
			panic(fmt.Sprintf("routing: route %q: %v", pattern, err))
		}
		if b, ok := wrapped.(web.HostBinder); ok {
			r.AddHostBinder(pattern, b)
		}
		wrappedHandler = wrapped
	}
	r.ServeMux.Handle(pattern, wrappedHandler)
}

func (r *BaseRouter) HandleFunc(pattern string, handleFunc func(http.ResponseWriter, *http.Request), handlerWrappers ...web.HandlerWrapper) {
	r.Handle(pattern, http.HandlerFunc(handleFunc), handlerWrappers...)
}

func (r *BaseRouter) Group(prefix string, batch func(*RouteGroup), handlerWrappers ...web.HandlerWrapper) *RouteGroup {
	g := &RouteGroup{
		Router: r,
		Prefix: prefix,
		// The group owns its wrapper list (see RouteGroup.Group) — a caller
		// spreading a shared slice must not alias it into the group.
		HandlerWrappers: append(make([]web.HandlerWrapper, 0, len(handlerWrappers)), handlerWrappers...),
	}

	batch(g)

	return g
}

// AddHostBinder records b, a wrapper mounted on the route named route, as one
// that must be told this router's hosts. Handle does it for the wrappers it
// applies; a RouteGroup does it for the ones it applies before handing the
// route to its router.
func (r *BaseRouter) AddHostBinder(route string, b web.HostBinder) {
	r.hostBinders = append(r.hostBinders, routeHostBinder{route: route, binder: b})
}

// BindHost tells every wrapper mounted on this router that needs it that the
// router serves host. Called once per host the router is listed under, when
// the web service's handler table is built.
//
//   - Returns: nil; the first wrapper's error, naming its route, when one
//     cannot serve host.
func (r *BaseRouter) BindHost(host *web.Host) error {
	for _, rb := range r.hostBinders {
		if err := rb.binder.BindHost(host); err != nil {
			return fmt.Errorf("route %q: %w", rb.route, err)
		}
	}
	return nil
}
