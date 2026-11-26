# Session Service

A **host** for the per-protocol session managers (cookie, bearer, future
flavors). Owns the shared state those managers coordinate against —
the `SessionLocks` store and the `KVDB` reference — and runs nothing in
the background: the lock store manages its own memory (entries exist only
while held or waited for; see `base/locking`), so there is nothing left for
this service to tick over.

This service is not independent infrastructure that does its own work —
it's a container that becomes useful only when at least one session
manager is attached, plus the admission gate the session middleware
checks per request.

What it holds:

- `SessionLocks` (`locking.Store`) — the per-session critical-section
  lock store, shared with every attached manager. Which implementation
  fills the seat is the app's, given when the Service is constructed.
- The `KVDB` reference the managers read and write sessions through.
- The managers of each session protocol: `BearerSessionManager`, one for
  the app, and `CookieSessionManagers`, one per host that has cookie
  sessions, keyed by the host's name. Bearer sessions are the app's — a
  token is a client's, whatever host it is presented to. Cookie sessions
  are a host's: a browser keeps a session cookie for one host only, and
  each host has its own conf and keyring. Each manager is a **concrete type
  specific to its protocol** — no shared interface. All of this is
  available from the moment it is attached and stays available for the
  lifetime of the process.

## Construct

`NewService(kvdb, lockingManager)` (`NewServiceAs(name, …)` to name the
instance):
- Takes the KVDB the managers read and write sessions through, and the lock
  store they share. The service picks neither.

Once the Service is constructed, the app attaches the session managers for
whichever protocols it uses:
- `CookieSessionManagers[<host name>]` — the cookie manager of one host; one
  entry per host that has cookie sessions
- `BearerSessionManager` — the app's one bearer manager

An app uses one, the other, or both. Unwired fields stay `nil` and the
middleware paths for those protocols are simply never reached.

A cookie manager serves one host's sessions, so a session cookie from one
host opens nothing on another — the host's name is in every row key
(`<app>:<host>:cu:<sid>`) and in the cipher context every cookie value is
sealed under.

## Start() / Stop()

Pure state transitions — there is no background work to begin or halt.
The states still matter: they are what the admission gate answers with,
so stopping the service makes the session middleware refuse, exactly as
for any other service. The session *data* is independent of this
lifecycle: KVDB rows keep expiring on their TTLs, and the lock store
keeps draining itself, whatever state the service is in.

## Terminate()

Terminal: state stays `TERMINATING`; the service cannot be `Start`ed
again in this process lifetime. Fires its `Terminated()` channel, so
whoever waits on it can count this service as done.
There is no stop activity to wait for, so it completes immediately and
always cleanly.

No in-process resources to release; the lock store and the session
managers are reclaimed by GC at process exit. KVDB-side cleanup remains
the KVDB's responsibility (TTL).

## Operator note

If you need to drain or invalidate sessions, do it through the manager
APIs (e.g. a cookie manager's `DestroyUserSession` or by deleting the
relevant KVDB keys). Stopping the service gates the middleware; it has no
effect on session validity or session-row presence.
