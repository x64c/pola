package framework

import (
	"errors"
	"fmt"

	"github.com/x64c/pola/base/authn"
	"github.com/x64c/pola/base/security"
	"github.com/x64c/pola/base/web"
)

// PrepareAuthnFlowManager prepares the authn flow-ticket manager of ONE HOST.
// A flow leaves from a host and returns to that same host, so its tickets are
// that host's: call this once per host that offers a browser-mediated login,
// with the Host WebHost returned. The cipher is derived from that host's
// cookie keyring under authn.FlowCipherPurpose — a flow ticket is a cookie
// and lives under cookie custody. Prerequisite: PrepareCookieSessions for the
// same host (the keyring is read there).
func (c *Core) PrepareAuthnFlowManager(host *web.Host) (*authn.FlowManager, error) {
	if host == nil {
		return nil, errors.New("authn flow: host required — take it from WebHost")
	}
	if c.sessionService == nil || c.sessionService.CookieSessionManagers[host.Name] == nil {
		return nil, fmt.Errorf("authn flow: cookie sessions of host %q not prepared — call PrepareCookieSessions for it first", host.Name)
	}
	keyring := c.sessionService.CookieSessionManagers[host.Name].Conf.Keyring
	if keyring == nil {
		return nil, fmt.Errorf("authn flow: no \"keyring\" for host %q in .web-cookie-session.json", host.Name)
	}
	cipher, err := security.NewKeyringCipher(keyring, c.appName, authn.FlowCipherPurpose)
	if err != nil {
		return nil, fmt.Errorf("authn flow cipher: %v", err)
	}
	return authn.NewFlowManager(c.appName, host.Name, cipher)
}
