package oidc

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/x64c/pola/base/web"
)

// WebLoginConf is one identity provider's browser login on the browser-facing
// side (redirect out to the provider, callback in): the Provider the initiate
// and verify halves run against, and the redirect URI the provider returns
// the browser to (the app's callback endpoint).
type WebLoginConf struct {
	Provider    Provider `json:"provider"`
	RedirectURI string   `json:"redirect_uri"`
}

// Validate reports the first missing required field, or a redirect_uri that
// does not have the form of one: an absolute http or https URL with no
// userinfo (RFC 9110 §4.2.4) and no fragment (RFC 6749 §3.1.2).
func (c *WebLoginConf) Validate() error {
	if err := c.Provider.Validate(); err != nil {
		return err
	}
	if c.RedirectURI == "" {
		return errors.New("oidc web login conf: redirect_uri required")
	}
	u, err := url.Parse(c.RedirectURI)
	if err != nil {
		return fmt.Errorf("oidc web login conf: redirect_uri %q is not a URL: %w", c.RedirectURI, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(c.RedirectURI, "#") {
		return fmt.Errorf("oidc web login conf: redirect_uri %q: want an absolute http or https URL with no userinfo and no fragment", c.RedirectURI)
	}
	return nil
}

// WebLoginConfs is ONE HOST's browser logins, keyed by the identity provider
// id the app chose (the key its routes select a login by). A browser login is
// a host's: the flow leaves from a host and the provider returns the browser
// to that same host, where the flow cookie and the session cookie it ends in
// are both host-only.
type WebLoginConfs map[string]*WebLoginConf

// webLoginConfsFileShape is said in every error about the file's shape: the
// file had another one before logins were a host's.
const webLoginConfsFileShape = `the file is keyed by host name, then identity provider id: {"<host>": {"<idp id>": {"provider": {…}, "redirect_uri": "…"}}}`

// LoadWebLoginConfs reads <appRoot>/config/.web-login-oidc.json — {"<host>":
// {"<idp id>": {"provider": {…}, "redirect_uri": "…"}, …}, …} — and returns
// the logins of host, the Host framework.Core.WebHost returned. Call at boot,
// once per host that offers a browser login; a file that doesn't load must
// not serve.
//
// The whole file is judged, every host's logins and not only host's: unknown
// members are rejected and each entry validated (WebLoginConf.Validate, the
// form of its redirect_uri included), so a login nothing asks for yet cannot
// sit there broken.
//
//   - Returns: host's logins; an error naming the file when it cannot be read
//     or parsed, when an entry is invalid, or when host has no logins in it.
func LoadWebLoginConfs(appRoot string, host *web.Host) (WebLoginConfs, error) {
	if host == nil {
		return nil, errors.New("oidc web login confs: host required — take it from Core.WebHost")
	}
	path := filepath.Join(appRoot, "config", ".web-login-oidc.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var byHost map[string]WebLoginConfs
	if err = json.Unmarshal(b, &byHost, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("oidc web login confs %s: %w (%s)", path, err, webLoginConfsFileShape)
	}
	if len(byHost) == 0 {
		return nil, fmt.Errorf("oidc web login confs %s: no host (%s)", path, webLoginConfsFileShape)
	}
	for _, hostName := range slices.Sorted(maps.Keys(byHost)) {
		confs := byHost[hostName]
		if len(confs) == 0 {
			return nil, fmt.Errorf("oidc web login confs %s: host %q has no identity provider", path, hostName)
		}
		for _, id := range slices.Sorted(maps.Keys(confs)) {
			c := confs[id]
			if c == nil {
				return nil, fmt.Errorf("oidc web login confs %s: %q/%q is null", path, hostName, id)
			}
			if err = c.Validate(); err != nil {
				return nil, fmt.Errorf("oidc web login confs %s: %q/%q: %w", path, hostName, id, err)
			}
		}
	}
	confs, ok := byHost[host.Name]
	if !ok {
		return nil, fmt.Errorf("oidc web login confs %s: host %q has no logins (the file has: %s)", path, host.Name, strings.Join(slices.Sorted(maps.Keys(byHost)), ", "))
	}
	return confs, nil
}
