package svccmds

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/x64c/pola/base/framework"
	"github.com/x64c/pola/base/svc"
	"github.com/x64c/pola/base/web/session"
)

// Session is the svc-cmd for the session service. Beyond the lifecycle verbs it
// exposes per-protocol enable/disable, since the session service hosts
// independently switchable protocols (cookie, bearer). Cookie sessions are a
// host's, so cookie has one switch per host: "enable cookie" and "disable
// cookie" act on every host's, and a host name after it on that host's alone.
type Session struct {
	AppProvider framework.AppProviderFunc
	name        string // optional override; defaults to "session"
}

func (h *Session) Name() string {
	if h.name == "" {
		return "session"
	}
	return h.name
}
func (h *Session) Rename(name string) { h.name = name }
func (h *Session) Usage() string {
	return "start | stop [--ttl <dur>] | status | enable <protocol> [host] | disable <protocol> [host]"
}

func (h *Session) Handle(subcmd string, args []string, w io.Writer) error {
	appCore := h.AppProvider().AppCore()
	node := appCore.SessionHandle().Node()
	// Typed reach on the NODE plane: operator commands act on services
	// regardless of admission (inspecting or switching a stopped service is
	// the point), so the type comes back from the node by assertion — the
	// node is already the operator's object, and an absent service fails the
	// assertion cleanly.
	s, ok := node.Service().(*session.Service)
	if !ok {
		return fmt.Errorf("%s service not configured in this app", h.Name())
	}
	switch subcmd {
	case "start", "stop":
		// Lifecycle goes through Core's operator ops so admission follows the
		// action; the session-specific status shape prints below.
		if err := doLifecycle(appCore, node, subcmd, args); err != nil {
			return err
		}
	case "enable":
		return setSessionProtocol(node, s, args, true, w)
	case "disable":
		return setSessionProtocol(node, s, args, false, w)
	case "status":
		// fall through to status print
	default:
		return fmt.Errorf("unknown subcommand %q (supported: start, stop, status, enable <protocol> [host], disable <protocol> [host])", subcmd)
	}
	printSessionStatus(node, s, w)
	return nil
}

func setSessionProtocol(n *framework.ServiceNode, s *session.Service, args []string, enable bool, w io.Writer) error {
	names := sessionProtocolNames(s)
	if len(args) < 1 {
		return fmt.Errorf("missing protocol name (configured: %s)", strings.Join(names, ", "))
	}
	host := ""
	if len(args) > 1 {
		host = args[1]
	}
	switches, err := sessionSwitches(s, args[0], host)
	if err != nil {
		return err
	}
	for _, sw := range switches {
		if enable {
			sw.Switchable.Enable()
		} else {
			sw.Switchable.Disable()
		}
	}
	printSessionStatus(n, s, w)
	return nil
}

// sessionSwitch is one on/off switch of the session service as the operator
// sees it: "bearer", or one host's cookie sessions — "cookie[<host>]".
type sessionSwitch struct {
	Label string
	svc.Switchable
}

// sessionSwitches returns the switches a protocol name selects. "bearer" is
// one switch — bearer sessions are the app's, not a host's — and takes no
// host. "cookie" is one switch per host that has cookie sessions: with no
// host, all of them (the whole protocol); with a host's name, that host's
// alone.
//
//   - Returns: the switches, in host-name order; an error when the protocol
//     is unknown or not configured, when a host is given to bearer, or when
//     the host has no cookie sessions.
func sessionSwitches(s *session.Service, protocol, host string) ([]sessionSwitch, error) {
	switch protocol {
	case "bearer":
		if s.BearerSessionManager == nil {
			break
		}
		if host != "" {
			return nil, fmt.Errorf("bearer sessions are not per host: no host argument (got %q)", host)
		}
		return []sessionSwitch{{"bearer", s.BearerSessionManager}}, nil
	case "cookie":
		if len(s.CookieSessionManagers) == 0 {
			break
		}
		hosts := slices.Sorted(maps.Keys(s.CookieSessionManagers))
		if host != "" {
			mgr, ok := s.CookieSessionManagers[host]
			if !ok {
				return nil, fmt.Errorf("host %q has no cookie sessions (hosts with cookie sessions: %s)", host, strings.Join(hosts, ", "))
			}
			return []sessionSwitch{{"cookie[" + host + "]", mgr}}, nil
		}
		switches := make([]sessionSwitch, 0, len(hosts))
		for _, h := range hosts {
			switches = append(switches, sessionSwitch{"cookie[" + h + "]", s.CookieSessionManagers[h]})
		}
		return switches, nil
	}
	return nil, fmt.Errorf("unknown or unconfigured protocol %q (configured: %s)", protocol, strings.Join(sessionProtocolNames(s), ", "))
}

func sessionProtocolNames(s *session.Service) []string {
	var names []string
	if len(s.CookieSessionManagers) > 0 {
		names = append(names, "cookie")
	}
	if s.BearerSessionManager != nil {
		names = append(names, "bearer")
	}
	return names
}

func printSessionStatus(n *framework.ServiceNode, s *session.Service, w io.Writer) {
	printLifecycleStatus(n, w)
	for _, name := range sessionProtocolNames(s) {
		switches, _ := sessionSwitches(s, name, "")
		for _, sw := range switches {
			enabled := "disabled"
			if sw.Enabled() {
				enabled = "enabled"
			}
			// "serving" is the wrapper gate's actual verdict: admitted && enabled.
			serving := "not-serving"
			if n.Admitted() && sw.Enabled() {
				serving = "serving"
			}
			_, _ = fmt.Fprintf(w, "  %s: %s (%s)\n", sw.Label, enabled, serving)
		}
	}
}
