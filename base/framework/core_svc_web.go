package framework

import (
	"encoding/json/v2"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/x64c/pola/base/svc"
	"github.com/x64c/pola/base/web"
)

// LoadWebServerConf reads and validates config/.web-server.json into
// c.WebServerConf. Separate from PrepareWebService because the conf is needed
// before the service is constructed: what is a host's own is prepared from
// its Host (WebHost), and the handler table is keyed by the conf's hosts.
func (c *Core) LoadWebServerConf() error {
	confFilePath := filepath.Join(c.AppRoot, "config", ".web-server.json")
	confBytes, err := os.ReadFile(confFilePath)
	if err != nil {
		return err
	}
	// Unknown members are refused: a member this conf does not have — a typo,
	// or one an earlier version had — would otherwise be dropped in silence,
	// and the app would run on something other than what the file says.
	if err = json.Unmarshal(confBytes, &c.WebServerConf, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("web server conf %s: %w", confFilePath, err)
	}
	// A host's name is its key under "hosts": the file states it once.
	for name, h := range c.WebServerConf.Hosts {
		if h != nil {
			h.Name = name
		}
	}
	return c.WebServerConf.Validate()
}

// WebHost returns the host named name from the loaded web server conf: the
// entry under "hosts" in .web-server.json with that key. Everything about the
// host comes from the conf; the name is the one thing code states, because it
// is code that knows which router serves which host.
//
// Call it at boot, after LoadWebServerConf, once per host the app serves, and
// use the Host as the key of its handler in the web.HostHandlers the
// PrepareWebService builder returns — the one place that says which handler
// serves which host. It returns the same *web.Host every time for one name.
//
// A name the conf does not have is an error naming the ones it does.
func (c *Core) WebHost(name string) (*web.Host, error) {
	return c.WebServerConf.Host(name)
}

// webServiceName is the name the web service registers under.
const webServiceName = "WebService"

// PrepareWebService validates the given conf — its own invariants, then the one
// relation it has to the already-settled core conf — and registers the web
// service. The conf is taken as an argument rather than read from the Core so
// the value the server runs on is the value validated here.
//
// The HTTP handlers are built by buildHandlers, which runs during the
// registration and receives the web service's Deps. It returns one handler
// per host of the conf (web.HostHandlers, keyed by the Hosts WebHost
// returned); the web service serves them through a web.HostDispatcher built
// here, and a table that does not match the conf's hosts one to one fails
// the boot. The same handler may stand under several hosts. A handler that
// must know its hosts is told them as the dispatcher is built
// (web.HostBinder), and one that cannot serve a host it stands under fails
// the boot. Handlers hold no graph edges of their own: what they use, the
// web service depends on. So a handler that uses a service gets its handle
// from these Deps, and that service is declared in deps — then the web
// service is started after it and torn down before it. Handlers that use no
// service ignore the Deps:
//
//	func(framework.Deps) (web.HostHandlers, error) {
//		return web.HostHandlers{host: router}, nil
//	}
//
// A host the conf starts paused (start_paused) is named in a [WARN] log line
// here: from outside it looks exactly like a host that is broken.
//
// Call this when all the required services are prepared.
func (c *Core) PrepareWebService(conf web.ServerConf, buildHandlers func(d Deps) (web.HostHandlers, error), deps ...ServiceDep) (*web.Service, error) {
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	// Every host .web-cookie-session.json gives cookie sessions must have been
	// prepared by now: this is the last Prepare, and the routes are about to
	// be served.
	if err := c.checkCookieSessionHostsPrepared(); err != nil {
		return nil, err
	}
	if conf.DrainTimeoutSecs >= c.SvcTerminateTimeoutSecs {
		return nil, fmt.Errorf("drain_timeout_secs (%d) must be less than svc_terminate_timeout_secs (%d): a full drain could never finish inside the shutdown budget", conf.DrainTimeoutSecs, c.SvcTerminateTimeoutSecs)
	}
	// Client-address resolution belongs to the web server — logging, audit and
	// rate limiting all ask for it — so it is prepared here. Trusting nothing is
	// a valid answer, and also the one that makes every request behind a proxy
	// look like the proxy, so it is said out loud.
	c.ClientIPResolver = conf.ClientIPResolver()
	if c.ClientIPResolver.TrustedCount() == 0 {
		log.Printf("[WARN][WebService] no trusted_proxy_cidrs: client address = connection peer, forwarding headers ignored.")
	}

	// A host that starts paused answers 503 from the first request, by the
	// conf's choice — and looks, from outside, exactly like a host that is
	// broken. So it is said out loud, with what ends it.
	for _, name := range slices.Sorted(maps.Keys(conf.Hosts)) {
		if conf.Hosts[name].StartPaused {
			log.Printf("[WARN][WebService] host %q starts paused (start_paused): it answers 503 until it is resumed.", name)
		}
	}

	c.WebServerConf = conf
	// The web subsystem ships middleware that may reach for these, and it
	// cannot know which of it the app actually mounted — the route trees arrive
	// as opaque handlers. So it declares the POSSIBILITY. Over-declaring
	// costs one ordering constraint; under-declaring is the defect this graph
	// exists to prevent, so the conservative direction is the correct one.
	//
	// Named from the registered instances rather than from string literals: a
	// service registered under a name of the app's choosing still matches, and
	// one the app never prepared contributes no dependency at all.
	own := make([]ServiceDep, 0, 4)
	if c.throttleNode != nil {
		own = append(own, MayUse(c.throttleNode.Name()))
	}
	if c.sessionService != nil {
		own = append(own, MayUse(c.sessionService.Name()))
	}
	if c.sseNode != nil {
		own = append(own, MayUse(c.sseNode.Name()))
	}
	if c.pubsubNode != nil {
		own = append(own, MayUse(c.pubsubNode.Name()))
	}
	var webService *web.Service
	node, err := c.RegisterServiceFunc(webServiceName, func(d Deps) (svc.Service, error) {
		handlers, err := buildHandlers(d)
		if err != nil {
			return nil, err
		}
		dispatcher, err := web.NewHostDispatcher(conf, handlers)
		if err != nil {
			return nil, err
		}
		webService = web.NewServiceAs(webServiceName, dispatcher, conf)
		return webService, nil
	}, append(own, deps...)...)
	if err != nil {
		return nil, err
	}
	c.webService = webService
	c.webNode = node
	return c.webService, nil
}
