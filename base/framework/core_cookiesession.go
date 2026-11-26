package framework

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/x64c/pola/base/security"
	"github.com/x64c/pola/base/web"
	"github.com/x64c/pola/base/web/session/cookie"
)

// PrepareCookieSessions prepares the cookie session manager of ONE HOST,
// covering both user-bound and anonymous shapes. Cookie sessions are a
// host's: .web-cookie-session.json is keyed by host name, and under each
// host stand its own "keyring", "user" (user shape) and "" (anonymous shape)
// — either shape or both. Call it once per host that has an entry, with the
// Host WebHost returned. The manager is attached to the session service
// under the host's name (SessionService.CookieSessionManagers).
// Prerequisite: MainKVDB, SessionService, LoadWebServerConf.
//
// A host with no entry in the file has no cookie sessions, and asking for
// them is an error. The reverse is an error too: a host the file lists but
// no call ever prepared fails the boot at PrepareWebService, since the file
// would otherwise promise sessions nothing serves.
//
// Pass useFWUpstream=true if the host's cookie sessions store upstream
// tokens; the manager then takes c.FWUpstream, and this fails at boot if it
// isn't prepared yet (call PrepareFWUpstream first). Pass false if they have
// no upstream.
func (c *Core) PrepareCookieSessions(host *web.Host, useFWUpstream bool) (*cookie.SessionManager, error) {
	if c.sessionService == nil {
		return nil, errors.New("session service not ready")
	}
	if host == nil || c.WebServerConf.Hosts[host.Name] != host {
		return nil, errors.New("cookie sessions: host must be a Host of the loaded web server conf — take it from WebHost")
	}
	if err := c.loadCookieSessionConfs(); err != nil {
		return nil, err
	}
	sessionConf, ok := c.cookieSessionConfs[host.Name]
	if !ok {
		return nil, fmt.Errorf("cookie sessions: host %q has no entry in .web-cookie-session.json (it has: %s)", host.Name, strings.Join(slices.Sorted(maps.Keys(c.cookieSessionConfs)), ", "))
	}
	if _, prepared := c.sessionService.CookieSessionManagers[host.Name]; prepared {
		return nil, fmt.Errorf("cookie sessions: host %q is already prepared", host.Name)
	}

	mgr, err := cookie.NewSessionManager(c.appName, host.Name, c.MainKVDB, c.sessionService.LockingManager())
	if err != nil {
		return nil, err
	}
	mgr.Conf = sessionConf

	// Wire the upstream subsystem only when the app declares cookie sessions
	// store upstream tokens — and verify it was prepared, failing loud at boot.
	if useFWUpstream {
		if c.FWUpstream == nil {
			return nil, errors.New("cookie sessions: useFWUpstream=true but FWUpstream not prepared — call PrepareFWUpstream first")
		}
		mgr.FWUpstream = c.FWUpstream
	}

	// One keyring serves both shapes; each shape's cipher is derived under its
	// own purpose label. Construction validates the whole keyring (active
	// among keys, key ids, algs, key material) — misconfiguration is a boot
	// failure here, never a per-request surprise. Each key's "enckey" is
	// base64-encoded 32 random master bytes: openssl rand -base64 32
	//
	// The keyring is the host's own. Two hosts given the same keyring still
	// cannot open each other's cookies: the host's name is in the cipher
	// context every cookie value is sealed under.
	if sessionConf.UserSession != nil {
		cipher, err := security.NewKeyringCipher(sessionConf.Keyring, c.appName, cookie.UserCookieCipherPurpose)
		if err != nil {
			return nil, fmt.Errorf("host %q user cookie cipher: %v", host.Name, err)
		}
		mgr.UserCookieCipher = cipher
	}
	if sessionConf.AnonymousSession != nil {
		cipher, err := security.NewKeyringCipher(sessionConf.Keyring, c.appName, cookie.AnonymousCookieCipherPurpose)
		if err != nil {
			return nil, fmt.Errorf("host %q anonymous cookie cipher: %v", host.Name, err)
		}
		mgr.AnonymousCookieCipher = cipher
	}

	if c.sessionService.CookieSessionManagers == nil {
		c.sessionService.CookieSessionManagers = make(map[string]*cookie.SessionManager, len(c.cookieSessionConfs))
	}
	c.sessionService.CookieSessionManagers[host.Name] = mgr
	mgr.Enable() // cookie protocol wired on this host → serving
	return mgr, nil
}

// loadCookieSessionConfs reads and validates config/.web-cookie-session.json
// into c.cookieSessionConfs, once: host name → that host's conf. The whole
// file is judged here, every host's entry and not only the one being
// prepared, so an entry nothing asks for yet cannot sit there broken.
//
//   - Returns: nil when the confs are loaded (now or by an earlier call); an
//     error naming the file when it cannot be read or parsed, when a top-level
//     key is not a host of the web server conf, or when a host's entry is
//     unusable.
//   - Uses: os.ReadFile, json.Unmarshal, parseCookieSessionConf.
func (c *Core) loadCookieSessionConfs() error {
	if c.cookieSessionConfs != nil {
		return nil
	}
	const file = ".web-cookie-session.json"
	confBytes, err := os.ReadFile(filepath.Join(c.AppRoot, "config", file))
	if err != nil {
		return err
	}
	var raw map[string]jsontext.Value
	if err = json.Unmarshal(confBytes, &raw); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if len(raw) == 0 {
		return fmt.Errorf(`%s: no host — the file is keyed by host name: {"<host>": {"keyring": …, "user": …}}`, file)
	}
	confs := make(map[string]*cookie.SessionConf, len(raw))
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		if _, ok := c.WebServerConf.Hosts[name]; !ok {
			return fmt.Errorf(`%s: %q is not a host in .web-server.json (hosts: %s) — the file is keyed by host name: {"<host>": {"keyring": …, "user": …}}`, file, name, strings.Join(slices.Sorted(maps.Keys(c.WebServerConf.Hosts)), ", "))
		}
		conf, err := parseCookieSessionConf(raw[name])
		if err != nil {
			return fmt.Errorf("%s: host %q: %w", file, name, err)
		}
		confs[name] = conf
	}
	c.cookieSessionConfs = confs
	return nil
}

// parseCookieSessionConf reads one host's entry of .web-cookie-session.json:
// "keyring", "user" (the user shape) and "" (the anonymous shape). A member
// with any other name is refused — under a host there is nothing else, so
// another name is a member put in the wrong place.
func parseCookieSessionConf(entry jsontext.Value) (*cookie.SessionConf, error) {
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(entry, &raw); err != nil {
		return nil, err
	}
	conf := &cookie.SessionConf{}
	for _, member := range slices.Sorted(maps.Keys(raw)) {
		switch member {
		case "keyring":
			conf.Keyring = &security.KeyringConf{}
			if err := json.Unmarshal(raw[member], conf.Keyring); err != nil {
				return nil, fmt.Errorf("unmarshal keyring: %v", err)
			}
		case "user":
			conf.UserSession = &cookie.UserSessionConf{}
			if err := json.Unmarshal(raw[member], conf.UserSession); err != nil {
				return nil, fmt.Errorf("unmarshal user shape: %v", err)
			}
		case "":
			conf.AnonymousSession = &cookie.AnonymousSessionConf{}
			if err := json.Unmarshal(raw[member], conf.AnonymousSession); err != nil {
				return nil, fmt.Errorf("unmarshal anonymous shape: %v", err)
			}
		default:
			return nil, fmt.Errorf(`unknown member %q (a host has "keyring", "user" and "")`, member)
		}
	}
	if conf.UserSession == nil && conf.AnonymousSession == nil {
		return nil, errors.New(`neither "user" nor "" (anonymous) shape — a host with no cookie sessions has no entry`)
	}
	if conf.Keyring == nil {
		return nil, errors.New(`no "keyring"`)
	}
	if conf.UserSession != nil {
		if err := conf.UserSession.Validate(); err != nil {
			return nil, err
		}
	}
	if conf.AnonymousSession != nil {
		if err := conf.AnonymousSession.Validate(); err != nil {
			return nil, err
		}
	}
	return conf, nil
}

// checkCookieSessionHostsPrepared refuses a .web-cookie-session.json that
// lists a host no PrepareCookieSessions call prepared: the file says that
// host has cookie sessions, and nothing would serve them.
func (c *Core) checkCookieSessionHostsPrepared() error {
	for _, name := range slices.Sorted(maps.Keys(c.cookieSessionConfs)) {
		if c.sessionService == nil || c.sessionService.CookieSessionManagers[name] == nil {
			return fmt.Errorf(".web-cookie-session.json has cookie sessions for host %q, but PrepareCookieSessions was never called for it", name)
		}
	}
	return nil
}
