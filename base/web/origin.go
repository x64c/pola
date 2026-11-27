package web

import (
	"fmt"
	"net/url"
	"strings"
)

// An ORIGIN is a scheme, a hostname and a port — "https://example.com",
// "http://localhost:8080" (RFC 6454; RFC 9110 §4.3.1). A browser sends the
// origin of the page a request comes from in the request's Origin header,
// serialized as scheme://hostname[:port]: lowercase, no path, and no port
// when it is the scheme's default. It says where a request comes FROM, never
// where it goes: dispatching does not read it (see hostname.go).

// ValidateOrigin refuses o unless it is an origin as a browser sends one in an
// Origin header: scheme://hostname[:port] with scheme http or https,
// lowercase, nothing after it, no default port. A list of origins a
// request's Origin is compared against exactly holds only what passes, since
// anything else could never be equal to what a browser sends.
//
//   - Returns: nil; an error saying what is wrong with o.
func ValidateOrigin(o string) error {
	if o == "" {
		return fmt.Errorf(`must be set (e.g. "https://example.com")`)
	}
	u, err := url.Parse(o)
	if err != nil {
		return fmt.Errorf("%q is not an origin: %w", o, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("%q is not an origin: want scheme://hostname[:port] with scheme http or https and nothing after", o)
	}
	if o != strings.ToLower(u.Scheme+"://"+u.Host) {
		return fmt.Errorf("%q is not an origin as a browser sends it: lowercase it", o)
	}
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return fmt.Errorf("%q names its scheme's default port, which a browser leaves out: drop it", o)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%q has no hostname", o)
	}
	return nil
}
