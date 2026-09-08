# Typed WebSocket handlers

`NewWebSocket(WebSocketOptions)` creates an explicitly mounted JSON-message
handler. Its public API is independent of the internally pinned transport.
It adds no broker, channel layer, authentication provider, automatic replay or
cross-process registry. Applications supply current authorization and domain
handlers; ordinary authenticated HTTP setup still runs before the route.

## Registration and wire contract

Declare each input/output pair using
`WebSocketMessage[I, O](name, version, validate, handle)`. Both top-level types
must be concrete structs. Every field needs a unique explicit JSON tag; missing
fields are permitted, so mandatory `validate` owns required/domain rules.
Nested concrete scalar, pointer, struct, array and slice values are supported.
Validation and execution receive separate decoded input values.

Maps, interfaces, embedded/unexported/recursive fields, custom JSON/text codecs,
`[]byte`, `json.Number`, coercion tags such as `,string`, and `omitzero` are
refused. `omitempty` is supported. Tags use bounded ASCII identifiers and cannot
collide case-insensitively. Null is accepted only for pointers/slices; fixed
arrays require exactly their declared length. Integer syntax/ranges retain
precision; floating-point fields use ordinary finite Go float precision.

Clients must request the case-sensitive `gogo.json.v1` subprotocol and send text
messages with exactly these keys:

```json
{"type":"echo","version":1,"payload":{"text":"hello"}}
```

Replies use the same registered type/version and the declared output struct.
Unknown envelope/payload fields, case-folded aliases, duplicate keys (including
escaped duplicates), invalid UTF-8, unpaired surrogates and trailing JSON are
rejected. Input depth is at most 32 and node/key work at most 65,536. Before each
typed decode, schema-guided storage accounting bounds fixed fields, pointer
targets and slice backing storage, including omitted fixed fields. Returned
output is preflighted without allocation, including fixed composite and slice
memory, then copied and encoded under byte/node bounds. Static typed input and
output sizes cannot exceed the configured message limit.

## Origin and permissions

`Origins` contains exact HTTP(S) origins, such as `https://app.example.test`;
there are no wildcard patterns or request-Host fallback. A trailing path,
opaque origin, `null`, multiple origins or unapproved origin is denied.
Missing Origin is denied unless `AllowMissingOrigin` explicitly admits native
clients. This option does not bypass either mandatory authorization callback.

`Authorize` runs at handshake and on each message grant. It must recheck current
session expiry/revocation; an inherited principal is connection-time context,
not proof of ongoing session validity.
`AuthorizeMessage(request, action, envelope)` receives `WebSocketReceive` before
effects and `WebSocketSend` before enqueue and again immediately before
writing. All grants receive fresh request metadata and copied envelopes.
Mutation cannot retarget execution or queued output. Current object/tenant/token
policy belongs in these callbacks; an open connection is not a permanent grant.

Handshake metadata is bounded/reconstructed before callbacks. Body, form caches,
TLS data and net/http's private PathValue storage are not exposed. Gogo
`urls.Param` and other trusted context services remain available. These services
are trusted application-owned values, not a deep-copied context sandbox. HTTP/1.1
GET upgrades only are supported; compression is disabled.

## Bounds and connection ownership

| Option | Default | Hard maximum |
| --- | --- | --- |
| `MaxMessageBytes` | 1 MiB | 8 MiB; minimum 128 bytes |
| `MaxConnections` | 128 | 10,000 |
| `OutboundQueue` | 4 | 32 replies |
| `MessagesPerSecond` / `Burst` | 20 / 40 | 10,000 each |
| `IdleTimeout` | 1 minute | 10 minutes |
| `HandlerTimeout` / `WriteTimeout` | 10 seconds | 1 minute |
| `PingInterval` / `PongTimeout` | 20 / 5 seconds | 1 minute / 30 seconds |
| `Lifetime` | 1 hour | 24 hours |
| `CloseTimeout` | 1 second | 5 seconds |

Timeouts must be at least one millisecond. At most 128 message declarations and
128 configured origins are allowed. Message names are 1–64 ASCII characters;
versions are nonzero uint32 values. Typed schema traversal is bounded as well.

One reader continues receiving while one sequential application handler runs.
The inbound queue holds one message; full inbound/outbound queues close with 1013
instead of spawning workers. A token bucket counts complete data messages and
ping/pong control frames. It is not a per-continuation-frame rate guarantee.
Idle timeout bounds a complete message read, including unfinished fragments;
control traffic does not indefinitely extend that interval.

The connection owns separate application and transport cancellation. Disconnect,
deadline or shutdown cancels work. The writer/control pumps maintain ping/pong
and close handling; control callbacks only signal, never close under a read
lock. The raw hijacked connection bounds close-handshake duration. A slot remains
held until the main handler and every owned pump actually exit, even after the
socket is closed. Noncooperative callbacks cannot be killed and continue to
consume their bounded capacity.

## Server and application shutdown

With `NewServer`, select the WebSocket route through `ServerConfig.Streaming`.
The ordinary timeout wrapper cannot hijack; the handler fails before 101 when a
real underlying Hijacker or write-deadline support is unavailable. No existing
Server behavior is automatically changed.

Call `Shutdown(ctx)` explicitly during application drain. It permanently stops
admission, cancels application work, closes accepted transports and waits under
the caller's context. A timed-out shutdown does not mean stuck callbacks exited;
a later call can wait again. It never reopens admission. net/http shutdown does
not itself manage hijacked sockets. An `app.Resource` can return the socket's
Shutdown method as its closer; register it after the services used by handlers
so sockets drain before those services close. The executable example shows this
registration without starting a network service.

## Outcomes and delivery

Malformed HTTP requests return 400, unsupported methods 405, handshake policy
failures 403, unauthenticated session 401, and operational/capacity/stopped-admission
failures 503 before upgrade. These HTTP responses are private/no-store. After 101
there is never a second HTTP body; transfer failures abort the exchange.

Framework-generated close reasons are fixed: invalid data 1007, policy/domain
denial 1008, oversize 1009, unavailable handler 1011 and saturation 1013. Returning
exactly `ErrWebSocketClose` from a handler requests normal 1000 closure. Application
panics/errors never become close text. The transport may generate protocol
reasons or echo a peer close reason; these are not application error projection.

A handler may have committed an effect before a subsequent send is denied or the
connection disappears. No reply, close code or retry proves rollback. The
framework does not retry messages or add implicit transactions/idempotency;
durable effects must use an explicit application-owned domain boundary.
