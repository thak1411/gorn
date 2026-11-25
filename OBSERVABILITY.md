# HTTP lifecycle and observability

Requires Go 1.27.4. Existing route registration and `Run(port)` calls are supported.

`Router.Handler()` / `ServeHTTP` expose a standard HTTP handler. Register routes
and `Use(func(http.Handler) http.Handler)` middleware before the first request.
Middleware includes mux-generated 404s, redirects and CORS preflight requests.
`RouterOptions.ExposedHeaders` exposes response identifiers to browser clients.
The optional `ResponseObserver` callback can record application result codes.

`Run` handles SIGINT and SIGTERM, stops accepting connections and drains active
requests for 25 seconds. `RunContext` and `ServeContext` support custom lifecycles
and listeners. Hijacked connections must be closed by the application.

The optional `observe` package writes one JSON object per stdout line. Configure
`GORN_ENV`, `GORN_VERSION`, and `LOG_LEVEL` (default INFO), then call
`observe.Configure("service-name")`. It does not open files or send notifications.

Install `observe.HTTP` to record status, route, duration, bytes and request ID.
The middleware preserves streaming and WebSocket interfaces, recovers ordinary
handler panics, and preserves `http.ErrAbortHandler`. Successful configured health
paths are excluded. Request and response bodies, cookies, IPs and query strings
are not logged. Incoming request IDs are syntax/length checked, never trusted as
authentication or authorization. Trace IDs are correlation metadata only.

Call `observe.ReportError` at the owning boundary with the original error and
request context. A wrapped context.Canceled is informational only when that
context is also cancelled; deadlines and unrelated errors stay ERROR. Background
jobs must use their own lifecycle and report final failures. Preserve `%w` when
wrapping errors. `SetActor` accepts a minimal internal identifier, not a token.
`SetResult` plus HTTPOptions.SuccessCode classifies application-level responses.

Legacy Printf messages receive credential/URL redaction and length limits, but
applications must never pass bodies, source code, passwords or arbitrary payloads
to a logger. This redaction is a last defense, not a complete privacy filter.

Validation: `go test -race ./...` covers cancellation classification, request ID
correlation, redaction, panic/404/probes, streaming, CORS and draining requests.
