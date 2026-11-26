package handlerwrappers

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/x64c/pola/base/errs"
	"github.com/x64c/pola/base/web"
	"github.com/x64c/pola/base/web/responses"
)

// RecoverPanic recovers a panic raised by inner, logs it with a stack trace,
// and writes a 500 JSON error. Its shape is a wrap step: apply it directly, or
// call it from a HandlerWrapper's Wrap.
//
// Placed in front of a whole router, it passes on what the router must be
// told: the handler it returns is a web.HostBinder when inner is one.
func RecoverPanic(inner http.Handler) http.Handler {
	rec := recoverPanic{inner: inner}
	if b, ok := inner.(web.HostBinder); ok {
		return recoverPanicHostBinder{recoverPanic: rec, binder: b}
	}
	return rec
}

type recoverPanic struct {
	inner http.Handler
}

func (h recoverPanic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[PANIC] recovered: %v\n%s", rec, debug.Stack())
			responses.WriteErrorJSON(w, http.StatusInternalServerError, errs.InternalError)
		}
	}()
	h.inner.ServeHTTP(w, r)
}

// recoverPanicHostBinder is recoverPanic in front of a handler that must be
// told its hosts.
type recoverPanicHostBinder struct {
	recoverPanic
	binder web.HostBinder
}

func (h recoverPanicHostBinder) BindHost(host *web.Host) error { return h.binder.BindHost(host) }
