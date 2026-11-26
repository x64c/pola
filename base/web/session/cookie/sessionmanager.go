package cookie

import (
	"errors"
	"sync/atomic"

	"github.com/x64c/pola/base/kvdbs"
	"github.com/x64c/pola/base/locking"
	"github.com/x64c/pola/base/security"
	"github.com/x64c/pola/base/web/fwupstream"
)

// SessionManager owns cookie-session operations across both user and anonymous shapes.
// Each shape's state (Conf sub-block, Cipher) is present iff that shape is configured.
//
// A SessionManager is ONE HOST's: an app has one per host that has cookie
// sessions, each with its own conf and keyring. Its sessions are that host's
// alone — every row key and every cookie cipher context carries the host's
// name, so a session cookie taken from one host of the app opens nothing on
// another.
type SessionManager struct {
	Conf *SessionConf

	UserCookieCipher      security.EncodedCipher // present iff Conf.UserSession != nil
	AnonymousCookieCipher security.EncodedCipher // present iff Conf.AnonymousSession != nil

	FWUpstream *fwupstream.Hub // upstream subsystem; token I/O delegates here. nil iff no upstream configured

	appName        string
	hostName       string          // the name of the web host this manager's sessions belong to
	KVDB           kvdbs.DB        // holds session rows
	lockingManager locking.Manager // set at construction; shared with whatever holds this manager; guards each session's upstream refresh

	enabled atomic.Bool // the cookie protocol's on/off switch (svc.Switchable)
}

// NewSessionManager builds the SessionManager of the host named hostName, for
// the app named appName, over kvdb, with lockingManager guarding each
// session's upstream refresh. The two names and the locking manager are
// sealed here: every row key and every cookie cipher context derives from the
// names, and every instance of the app must hold its refresh names on the
// same manager, so none may change after construction. The shape-specific
// fields — Conf, the two ciphers, FWUpstream — are the caller's to set. None
// of the four arguments may be empty.
func NewSessionManager(appName, hostName string, kvdb kvdbs.DB, lockingManager locking.Manager) (*SessionManager, error) {
	if appName == "" {
		return nil, errors.New("cookie.NewSessionManager: appName required")
	}
	if hostName == "" {
		return nil, errors.New("cookie.NewSessionManager: hostName required")
	}
	if kvdb == nil {
		return nil, errors.New("cookie.NewSessionManager: kvdb required")
	}
	if lockingManager == nil {
		return nil, errors.New("cookie.NewSessionManager: lockingManager required")
	}
	return &SessionManager{appName: appName, hostName: hostName, KVDB: kvdb, lockingManager: lockingManager}, nil
}

// HostName is the name of the web host this manager's sessions belong to.
func (m *SessionManager) HostName() string { return m.hostName }

// UserCookieCipherContext / AnonymousCookieCipherContext are the cipher
// contexts cookie values are bound to: cookie name + app + host. Cookie names
// are package constants shared by every app that uses this package, and by
// every host one app serves, so App is what keeps two apps' cookies from
// decrypting each other's and Host what keeps two hosts'. Every seal/open of
// a cookie value goes through these — never a hand-built context.
func (m *SessionManager) UserCookieCipherContext() security.CipherContext {
	return security.CipherContext{App: m.appName, Host: m.hostName, Location: UserCookieName}
}

func (m *SessionManager) AnonymousCookieCipherContext() security.CipherContext {
	return security.CipherContext{App: m.appName, Host: m.hostName, Location: AnonymousCookieName}
}

// Enable / Disable / Enabled implement svc.Switchable — the cookie protocol's
// own on/off switch. Enabled() reports only this switch; whether the SERVICE
// may be used is not the manager's to answer — the caller's framework handle
// already did (svc.Service: methods judge no availability). The manager keeps
// no lifecycle state and no back-pointer to its service on purpose: a Serving()
// self-verdict here was a second authority beside admission, and the two
// diverge under abandonment.
func (m *SessionManager) Enable()       { m.enabled.Store(true) }
func (m *SessionManager) Disable()      { m.enabled.Store(false) }
func (m *SessionManager) Enabled() bool { return m.enabled.Load() }
