package svccmds

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/x64c/pola/base/framework"
	"github.com/x64c/pola/base/web"
)

// Web is the svc-cmd for the web (HTTP) service: the lifecycle verbs, and
// pause/resume of one of the hosts it serves.
//
//	svc pause web <host> [--retry-after <dur>]
//	svc resume web <host>
//	svc status web
//
// A host is PAUSED, not stopped or disabled: it serves from boot, a pause
// takes it out of service for now — its requests are answered 503 before
// anything of the host is reached, and the streams open on it end — and
// resume has it serving again at once, everything it held intact. The pause
// is this process's: it survives a stop and start of the web service, and a
// restart returns every host to how its conf says it starts — serving, or
// paused ("start_paused" in .web-server.json; resume is what ends that too).
//
// --retry-after is what the 503 tells clients in Retry-After. It is only
// said — the host stays paused until resume — and without it the header is
// not sent: how long a pause will last is the operator's to say.
//
// status lists every host with its origin and whether it is paused.
type Web struct {
	AppProvider framework.AppProviderFunc
	name        string // optional override; defaults to "web"
}

func (h *Web) Name() string {
	if h.name == "" {
		return "web"
	}
	return h.name
}
func (h *Web) Rename(name string) { h.name = name }
func (h *Web) Usage() string {
	return lifecycleUsage + " | pause <host> [--retry-after <dur>] | resume <host>"
}

func (h *Web) Handle(subcmd string, args []string, w io.Writer) error {
	appCore := h.AppProvider().AppCore()
	n := appCore.WebHandle().Node()
	// Typed reach on the NODE plane, as session's svc-cmd does: an operator
	// pauses or inspects a host whether or not the service is admitted.
	s, ok := n.Service().(*web.Service)
	if !ok {
		// the absent-handle node: this app never prepared the service
		return fmt.Errorf("%s service not configured in this app", h.Name())
	}
	switch subcmd {
	case "start", "stop":
		if err := doLifecycle(appCore, n, subcmd, args); err != nil {
			return err
		}
	case "pause":
		host, retryAfter, err := pauseArgs(s, args)
		if err != nil {
			return err
		}
		if err = s.PauseHost(host, retryAfter); err != nil {
			return err
		}
	case "resume":
		host, err := hostArg(s, args)
		if err != nil {
			return err
		}
		if len(args) > 1 {
			return fmt.Errorf("resume takes a host and nothing else (got %q)", strings.Join(args[1:], " "))
		}
		if err = s.ResumeHost(host); err != nil {
			return err
		}
	case "status":
		// fall through to status print
	default:
		return fmt.Errorf("unknown subcommand %q (supported: start, stop, status, pause <host> [--retry-after <dur>], resume <host>)", subcmd)
	}
	printWebStatus(n, s, w)
	return nil
}

// hostArg is the host name a pause or resume names: its first argument.
func hostArg(s *web.Service, args []string) (string, error) {
	if len(args) < 1 || strings.HasPrefix(args[0], "--") {
		return "", fmt.Errorf("missing host (hosts: %s)", strings.Join(webHostNames(s), ", "))
	}
	return args[0], nil
}

// pauseArgs reads "<host> [--retry-after <dur>]". The duration is what a
// paused host's 503 tells clients in Retry-After, and it is checked rather
// than skipped when unusable: a value the operator typed must not be dropped
// in silence.
func pauseArgs(s *web.Service, args []string) (string, time.Duration, error) {
	host, err := hostArg(s, args)
	if err != nil {
		return "", 0, err
	}
	rest := args[1:]
	if len(rest) == 0 {
		return host, 0, nil
	}
	if rest[0] != "--retry-after" || len(rest) != 2 {
		return "", 0, fmt.Errorf("pause takes <host> [--retry-after <dur>] (got %q)", strings.Join(args, " "))
	}
	d, err := time.ParseDuration(rest[1])
	if err != nil {
		return "", 0, fmt.Errorf("--retry-after %q is not a duration (e.g. 90s, 10m)", rest[1])
	}
	if d < time.Second {
		return "", 0, fmt.Errorf("--retry-after must be 1s or more: got %v (leave it out to tell clients nothing)", d)
	}
	return host, d, nil
}

func webHostNames(s *web.Service) []string {
	states := s.HostStates()
	names := make([]string, 0, len(states))
	for _, st := range states {
		names = append(names, st.Name)
	}
	return names
}

// printWebStatus prints the service's phase and, per host, whether it is
// paused beside the verdict a request would actually get: a host serves when
// the service is admitted and the host is not paused.
func printWebStatus(n *framework.ServiceNode, s *web.Service, w io.Writer) {
	printLifecycleStatus(n, w)
	for _, st := range s.HostStates() {
		state := "running"
		if st.Paused {
			state = "paused"
			if st.RetryAfter > 0 {
				state += fmt.Sprintf(", retry-after %v", st.RetryAfter)
			}
		}
		serving := "not-serving"
		if n.Admitted() && !st.Paused {
			serving = "serving"
		}
		_, _ = fmt.Fprintf(w, "  host[%s] %s: %s (%s)\n", st.Name, st.Origin, state, serving)
	}
}
