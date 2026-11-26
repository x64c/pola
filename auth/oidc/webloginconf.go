package oidc

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
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

// Validate reports the first missing required field.
func (c *WebLoginConf) Validate() error {
	if err := c.Provider.Validate(); err != nil {
		return err
	}
	if c.RedirectURI == "" {
		return errors.New("oidc web login conf: redirect_uri required")
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
// members are rejected and each entry validated, so a login nothing asks for
// yet cannot sit there broken. Of host's own logins one thing more is
// required: each redirect_uri is on host's origin — a provider that returned
// the browser to another host would return it to where neither the flow
// cookie nor the session it ends in exists.
//
//   - Returns: host's logins; an error naming the file when it cannot be read
//     or parsed, when an entry is invalid, when host has no logins in it, or
//     when one of host's redirect URIs is not on host's origin.
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
	for _, id := range slices.Sorted(maps.Keys(confs)) {
		if !strings.HasPrefix(confs[id].RedirectURI, host.Origin+"/") {
			return nil, fmt.Errorf("oidc web login confs %s: %q/%q: redirect_uri %q is not on the host's origin %s — the provider must return the browser to the host the login left from", path, host.Name, id, confs[id].RedirectURI, host.Origin)
		}
	}
	return confs, nil
}
