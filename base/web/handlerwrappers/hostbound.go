package handlerwrappers

import (
	"log"
	"net/http"

	"github.com/x64c/pola/base/errs"
	"github.com/x64c/pola/base/web"
	"github.com/x64c/pola/base/web/responses"
)

// perHost is what a handler holds for each host it serves: filled at boot,
// one BindHost call per host, and only read once requests are served.
type perHost[T any] struct {
	of map[*web.Host]T
}

// bind stores what the handler holds for host.
func (p *perHost[T]) bind(host *web.Host, v T) {
	if p.of == nil {
		p.of = make(map[*web.Host]T, 1)
	}
	p.of[host] = v
}

// forRequest returns what the handler holds for the host serving r.
//
//   - Returns: the value and true; the zero value and false when r is served
//     by no host of a web Service, or by a host the handler was never bound to.
func (p *perHost[T]) forRequest(r *http.Request) (T, bool) {
	host, ok := web.RequestHost(r)
	if !ok {
		var zero T
		return zero, false
	}
	v, ok := p.of[host]
	return v, ok
}

// refuseUnbound answers a request that reached a host-bound handler for a
// host it was never bound to. It cannot be served as that host, and serving
// it as another would apply the wrong host's origin or sessions, so it is
// refused. Only wiring can cause it: the handler table lists something in
// front of this handler's router that does not pass BindHost on.
func refuseUnbound(w http.ResponseWriter, r *http.Request, who string) {
	log.Printf("[ERROR][WebService] %s: request for Host %q reached it unbound: it was never told it serves that host (web.HostBinder) — the handler placed in front of its router must pass BindHost on", who, r.Host)
	responses.WriteErrorJSON(w, http.StatusInternalServerError, errs.InternalError)
}
