# Routing System

## - Handler Wrapper Order

The variadic `handlerWrappers ...web.HandlerWrapper` accepted by `Handle`,
`HandleFunc`, and `Group` are applied **outer-to-inner from left to right**.
The earlier argument wraps the result of the later arguments.

```go
router.Handle(pattern, handler, w1, w2)
```

builds:

```
w1( w2( handler ) )
```

On each request, `w1`'s `Wrap` body runs first, then calls into `w2`'s `Wrap`
body, then into `handler`. Response unwinding goes in reverse order.

When mixed with `Group` middleware, group middlewares are outer to per-route
middlewares:

```go
router.Group(prefix, batch, gw1, gw2)
//   inside the group: g.Handle(p, h, rw1, rw2)
```

builds:

```
gw1( gw2( rw1( rw2( handler ) ) ) )
```

i.e. group wrappers run before per-route wrappers on each request.

**Practical implication:** put pre-checks that should reject early (e.g. body
limit, auth, session validation) **earlier** in the vararg list (and at the
group level if they apply group-wide). Put inner concerns (response
decoration, body parsing helpers) later.

## - A Router and Its Hosts

A router is a routing table: it matches a request's method and path, never
its Host, and it does not say which host it serves. That is said in one
place, the web service's handler table (`web.HostHandlers`): each host has one
router there, and a router may stand under one host or under several.

```go
router := &routing.BaseRouter{ServeMux: http.NewServeMux()}
// … declare its routes …
handlers := web.HostHandlers{hostA: router, hostB: router} // or a router per host
```

When that table is built, the router is told each host it stands under
(`web.HostBinder`) and passes it on to every wrapper mounted on it that needs
to know, on `Handle` and on `Group` at any depth. Such a wrapper serves each
request as the host the request is for, so on a router under two hosts it
guards each with that host's own. A wrapper that cannot serve one of the
router's hosts fails the boot, naming the host and the route.

A router of your own that embeds `BaseRouter` passes hosts on the same way.
Something placed in front of a router in the handler table must pass
`BindHost` on to it, or the wrappers behind it are never told their host and
refuse the requests they would have checked.

## - Just Using Bundled BaseRouter and RouteGroup

```
type Router = routing.BaseRouter
type RouteGroup = routing.RouteGroup
```

## - Base Middleware to Apply Entire Requests
```
server := &http.Server{
    Addr:    env.Listen,
    Handler: HttpHandlerWrapper(router),
}
```
where `HttpHandlerWrapper` is a `func(http.Handler) http.Handler`

## - Overriding Methods by Embedding
You can write your own router.

e.g.
```
type Router struct {
	routing.BaseRouter
}

func (router *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ...
    ctx := foo.WithFooConf(r.Context(), bar)
    router.ServeMux.ServeHTTP(w, r.WithContext(ctx))
    ...
}

// still, your RouteGroup can be an alias with type instantiation:
type RouteGroup = routing.RouteGroup
```
