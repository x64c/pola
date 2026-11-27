# Web Service

HTTP server wrapper around `net/http`. The conf supplies a listen address
and the hosts the app serves; the app supplies one `http.Handler` per host
(usually that host's router). The service spins up an `*http.Server` on
Start, drains gracefully on Stop, and rebuilds the server fresh on the next
Start cycle.

Unlike `throttle` / `session` / `jobsched`, this service has **no
lifecycle-independent data plane**. There is no API surface that does
useful work without an active server — the registered handler is just
configuration waiting to be served. So `Stop` here means *the HTTP
endpoint goes offline*; `Start` brings it back online.

This service consists of 2 things:

1. **HTTP configuration** — `addr` + `handler`, preserved across cycles
   on the Service struct. Survives `Stop`/`Start`/`Terminate`; not
   reconstructed unless the Service itself is recreated.
2. **The HTTP server** — the `*http.Server` instance, the port binding,
   the connection accept loop inside `Serve`, and the per-
   request goroutines stdlib spawns. This is what the lifecycle
   controls.

The framework lifecycle (`Start`/`Stop`/`Terminate`) controls **only #2**.
#1 stays put for the lifetime of the Service value.

## Construct

`NewHostDispatcher(conf, handlers)`, then `NewService(dispatcher, conf)`
(`NewServiceAs(name, dispatcher, conf)` to name the instance):
- `NewHostDispatcher` checks the conf's hosts and the handlers against them:
  `handlers` (`HostHandlers`) must name every host of the conf and only
  those, or it returns an error.
- `NewService` keeps the listen address, the dispatcher and the conf. It
  binds nothing and builds no server: that is `Start`'s.
- The conf is expected valid (`ServerConf.Validate`). One relation `Validate`
  cannot check is the caller's: `drain_timeout_secs` must be shorter than the
  time the service is given to terminate.
- A handler holds nothing of this service's lifecycle. What a handler needs
  running while it serves must be started before this service and stopped
  after it; ordering the two is the work of whoever runs both.

## Hosts

### Hostname, authority, origin

Three terms (RFC 9110):

| term | parts | example | where it shows |
|---|---|---|---|
| hostname | hostname | `example.com` | what the `HostDispatcher` matches |
| authority | hostname + port | `example.com:8443` | a request's `Host` header (`:authority` in HTTP/2 and 3): where the request goes TO |
| origin | scheme + hostname + port | `https://example.com:8443` | a request's `Origin` header: where the request comes FROM |

- The client builds the authority from the URL it requests: the hostname,
  and the port when it is not the scheme's default. A browser lets no page
  change it; any other client may send what it likes. So it chooses among
  the hosts the app lists and proves nothing more.
- The `HostDispatcher` dispatches on the hostname alone: it drops the port of
  the authority, and never sees the scheme. The port says how a request
  travelled (through a proxy's 443, or straight to the app's own port), not
  which host it is for, and behind a proxy that ends TLS every request
  reaches the app over plain HTTP.
- A host has no origin: several origins (`http` and `https`, other ports)
  can sit on one hostname. The origin is not used to dispatch.

### The hosts table

A `ServerConf` lists the hosts the app serves under `hosts`. Each entry is
keyed by the word code picks the host by, and states:

```json
"hosts": {
  "main": {
    "hostname": "example.com",
    "extra_hostnames": ["127.0.0.1", "localhost"],
    "read_timeout_secs": 30,
    "write_timeout_secs": 60
  }
}
```

- `hostname` (required): the name the host really has, as a request's
  authority gives it once the port is dropped. Lowercase (an international
  name in its punycode form); no scheme, no port, no trailing dot; an IPv6
  address without brackets.
- `extra_hostnames` (optional): other hostnames that reach the same host. A
  request under any of them is the host's request: the same handler, the
  same deadlines, paused and resumed with the host. Code names only the host;
  the extra hostnames come from the conf. They are extra ways in and nothing
  more.
- A hostname may be listed once, and may lead to one host only, or the boot
  fails naming both places it is written.

An app that serves one host has one entry.

At boot, code picks each host by its key and says which handler serves it:

```go
host, err := conf.Host("main") // the entry "main" under "hosts"

// <handler>: what serves this host's requests, usually the host's router
dispatcher, err := web.NewHostDispatcher(conf, web.HostHandlers{host: <handler>})
service := web.NewService(dispatcher, conf)
```

- `web.HostHandlers` is the one place that says which handler serves which
  host. Each host has exactly one handler, and it must name every host of the
  conf and only those, or the boot fails.
- The same handler may stand under several hosts, which then serve the same
  routes: `web.HostHandlers{hostA: <handler>, hostB: <handler>}`.
- A handler that must know which host it serves (`web.HostBinder`) is told
  from this table: when the dispatcher is built, it calls `BindHost` once for
  each host the handler stands under, and an error fails the boot, naming the
  host. Such a handler serves each request as the host the dispatcher chose
  for it (`web.RequestHost(r)`), so under several hosts it is right for each.
- A host's handler never matches on the hostname itself: the dispatcher has
  done it by the time the handler runs.

The `HostDispatcher` is what the server runs. Per request it takes the
hostname of the authority (the `Host` header; port dropped, lowercased, one
trailing dot dropped — never `X-Forwarded-Host`), picks the host whose
`hostname` or one of whose `extra_hostnames` that is, sets that host's
deadlines, and calls its handler. A hostname that is none of these is
answered 421 Misdirected Request: the hosts of the conf and their hostnames
are the app's allowlist, and nothing catches the rest. A hostname the app is
to answer to — a loopback address a local caller uses, say — is listed in one
of two ways:

- as an extra hostname of the host it is to reach
  (`"extra_hostnames": ["127.0.0.1"]`), when it is to be served exactly as
  that host;
- as the `hostname` of a host of its own, with its own handler, when it is
  to be served differently: other routes, or a value that must differ by
  hostname.

### Pausing a host

A host serves from boot, unless its conf says `"start_paused": true`. It can
be **paused** and **resumed** while the app runs, one host at a time, the
others untouched:

```go
err := service.PauseHost(name, retryAfter) // retryAfter 0: no Retry-After is sent
err := service.ResumeHost(name)
states := service.HostStates()
```

- While a host is paused the dispatcher answers its requests itself: 503
  `HostPaused`, right after the host is found and before anything of the
  host is reached. The host's handler is not called, so every path on the
  host answers 503 — one the handler would have answered 404 included.
- A paused host pauses everything that relies on it. An ordinary request
  already inside the host when the pause begins finishes; what would not
  end by itself is told to end: a long-lived handler takes the host's pause
  notice with `web.HostPauseNoticeReceiver(r)` and finishes when it closes.
  A stream that does so ends, and its client comes back to the 503.
- It is a pause, not a stop: the host's handler and what it holds stay as
  they are, and `ResumeHost` has it serving again at once.
- `retryAfter` is what the 503 tells clients in `Retry-After`. It is only
  said — the host stays paused until `ResumeHost` — and at zero the header
  is not sent: how long a pause will last is the caller's to say.
- Only the host's HTTP is paused. Whatever else the app runs keeps running.
- The pause is the process's: it survives a `Stop` and `Start` of the
  service, and a restart of the app returns every host to how its conf says
  it starts.
- `"start_paused": true` under a host makes the host START paused: its
  handler is in place and what is its own is prepared as for any host, but
  it answers 503 from the first request until `ResumeHost` — to bring hosts
  up one at a time, or to hold one back until the others are checked. It is
  the host's state at boot only. Optional: absent or `false`, the host
  serves from boot.

The read and write deadlines are the host's, so the `*http.Server` carries
neither: the dispatcher sets them once the host is known. The write deadline
is set on every request and bounds the handler. The read deadline is set
only while a body remains to be read, and bounds the upload: on a request
with no body it would cancel the request's context mid-handler.

The `*http.Server` itself is **not** built with the Service — it's built
in `Start` (and rebuilt each cycle, because `http.Server` is one-shot
after `Shutdown`).

## Start()

- Builds a fresh `*http.Server` via `ServerConf.newHTTPServer` (the header
  and idle timeouts and `BaseContext` come from conf; the read and write
  deadlines are each host's, set per request by the `HostDispatcher`). (Mandatory rebuild — the previous one is
  dead after `Shutdown`.)
- Makes a fresh **drain notice** for the cycle and stores its receive end in
  every request context, as a value. A handler gets it with
  `DrainNoticeReceiverFromContext(r.Context())`.
- **Binds the listener itself**: `net.Listen("tcp", addr)`. A bind failure
  cancels both per-cycle contexts and is returned from `Start`, so the
  service never reports RUNNING on a port it does not hold.
- Only then sets RUNNING and spawns the run goroutine, which:
  - Starts an inner goroutine running `s.Server.Serve(s.listener)` — not
    `ListenAndServe`, since the listener is already bound. It returns
    `http.ErrServerClosed` on graceful drain (mapped to nil), otherwise a
    serving error.
  - Itself sits on a `select` watching for either `<-s.Ctx.Done()` (the
    normal Stop path) or `<-serverErr` (server died on its own).

`Start` fails **synchronously** on a bind failure — the listener is verified
before RUNNING is reported. Only *serving* failures remain asynchronous,
arriving on `serverErr`.

## Stop()

Cancels the per-cycle ctx, which triggers the graceful-drain path inside
the run goroutine:

0. Closes the cycle's drain notice — the drain starts. It cancels nothing:
   ordinary handlers keep running and finish inside the drain window. A
   long-lived handler (a stream) that selects on the notice finishes now,
   so it does not hold `Shutdown` until the drain deadline.
1. `s.Server.SetKeepAlivesEnabled(false)` — idle keep-alive conns get
   closed at next request, so they don't hold up the drain.
2. `s.Server.Shutdown(gracefulCtx)` — stdlib stops accepting new conns,
   waits for in-flight handlers to return, then closes everything.
3. Wait for `<-serverErr` — the `Serve` goroutine returns
   `http.ErrServerClosed` (mapped to nil) on graceful drain.

After Stop:
- No new requests are accepted (port released).
- In-flight handlers ran to completion (or were cut off by the drain
  deadline).
- The `*http.Server` instance is dead and cannot be reused — `Start`
  rebuilds it.

**Two timeouts to keep distinct:**
- `Stop(ctx)` — the caller's deadline for how long Stop will *wait* for
  the run goroutine to actually exit. If `ctx` expires first, Stop
  returns its error, but the drain keeps running in the background until
  the next budget expires.
- **The conf-driven internal drain budget** for `Server.Shutdown` — the
  ceiling on how long stdlib waits for in-flight handlers. It comes from
  `ServerConf.DrainTimeoutSecs` (`conf.drainTimeout()`), not from Stop's
  ctx, and must be shorter than the time the service is given to terminate. Long-running request handlers can be force-cancelled by this
  deadline regardless of what ctx the operator gave to Stop.

**Two signals a handler can see:**

| signal | when | how a handler gets it | effect |
|---|---|---|---|
| drain notice | the drain **starts** | `DrainNoticeReceiverFromContext(r.Context())` — a channel that is closed | none by itself; a handler that selects on it may finish now |
| request context canceled | the drain window **closes** | `r.Context().Done()` | in-flight work is canceled and must unwind |

If the server died on its own (a serving error — a port conflict would have
failed `Start`) instead of
being asked to stop, the run goroutine's `<-serverErr` branch fires and
the goroutine exits without going through the drain path. The state
field stays at whatever it was (likely `RUNNING`) because the deferred
`transitionAfterRun()` only flips `STOPPING → READY`, not `RUNNING →
anything`. That's a known limitation — operators should watch the log
for `[ERROR]` lines from this service if `State()` reports `RUNNING` but
the port isn't responding.

## Terminate()

Same flow as `Stop()` for this service (cancel + graceful drain + wait),
plus:
- Terminal: state stays `TERMINATING`. The service cannot be `Start`ed
  again in this process lifetime.
- Fires its `Terminated()` channel, so whoever waits on it can count this
  service as done.

The `addr` and `handler` references stay on the struct until process
exit; nothing actively releases them.

## Operator note

"Stop web" = **the HTTP endpoint is offline**. The bound port is
released, in-flight requests are drained on a 15-second internal budget,
and new connections are refused until `Start()` rebinds.

There is no graceful "soft pause" — the listener closes and the port
goes free. If you want to keep the process alive while the HTTP surface
is offline (e.g., for a maintenance window where you do operator work
via UDS), `Stop` on this service is the right verb.
