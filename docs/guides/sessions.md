# Sessions and flash messages

Sessions store per-browser application state. Flash messages are small, consume-once notices such as “Changes saved.” Neither should become a place for unrestricted customer data or credentials.

## Session stores

### Read and write a session value

This standalone example shows the context and state API:

**Set a session value**

{{snippet docs/examples/sessions_test.go session-write}}

**Read a session value**

{{snippet docs/examples/sessions_test.go session-read}}

Result: `true dark true`. `ctx` must come from session middleware; persistence requires its configured store.

<details>
<summary>Complete runnable example, including imports</summary>

{{code docs/examples/sessions_test.go}}

</details>

Run `go test ./docs/examples -run Example_session -v` from the framework checkout. In a handler use `r.Context()` instead of creating a new session. Treat a missing session as a wiring/error condition, not an authenticated empty session.

### Mount real persistence

In a project handler factory, after opening a Redis `SessionRole` connection and constructing a purpose-bound signer:

```go
withSessions, err := sessions.Middleware(sessions.MiddlewareConfig{
    Store: &redis.Sessions{Connection: sessionConnection},
    Signer: sessionSigner,
    CookieName: "gogo_session",
    Secure: settings.String("GOGO_ENV") == "production",
    TTL: 14 * 24 * time.Hour,
})
if err != nil { return nil, err }
return withSessions(router), nil
```

Alias `connectors/redis` as `redis`; `sessionConnection`, `sessionSigner`, `settings`, and `router` are the project-owned dependencies. The [full Admin wiring](admin-wiring.md) constructs all of them and places current account identity **inside** session middleware.

`core/sessions.Store` is backend-neutral. PostgreSQL and Redis connectors provide store implementations. The Core settings vocabulary also accepts `signed-cookie` and `cached-db`, but this release does not supply complete session-store implementations for those choices. Treat them as declared settings, not ready-to-use backends.

Configure store, cookie options, expiry and middleware in the project. Access the current session through `sessions.FromContext`. Changes are persisted at the middleware's response boundary, so handlers must not continue mutating the session after response completion.

Use the account service's login/logout integration to rotate and invalidate identities correctly. A session ID is not authorization to access a model. Version checks, expiry and provider failure must remain distinguishable from a genuinely missing session.

## Cookie boundaries

Use secure cookies under HTTPS and the appropriate SameSite/HTTP-only settings. Keep signing keys outside source and rotate them deliberately. Signed values are tamper-evident, not necessarily confidential. Do not claim immediate server-side revocation for a purely client-held value without a current server-side authority check.

## Flash message modes

With `messages.Middleware(messages.Config{Mode: messages.Session})` inside the session middleware, a handler can add a notice after a confirmed save:

```go
if err := messages.Add(r, messages.Success, "Changes saved"); err != nil {
    return err
}
```

On the next page request, `messages.Consume(r)` returns the notices and marks them consumed. Render their text with normal HTML escaping. Do not call Add before a database transaction has committed and then present the message as proof of success.

| Mode | Storage |
| --- | --- |
| `messages.Session` | Session-backed notices |
| `messages.Cookie` | Signed, explicitly public cookie messages |
| `messages.Fallback` | Configured cookie/session fallback behavior |

Levels are Debug, Info, Success, Warning and Error. Configure minimum level, message count, total bytes and cookie bytes. Private messages require session storage; they must not silently spill into a cookie.

Install message middleware inside session middleware when the selected mode needs sessions. Add messages after the underlying operation has a confirmed outcome. Consuming a notice is not an audit record or proof that a background task completed.
