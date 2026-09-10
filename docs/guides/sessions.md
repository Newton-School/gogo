# Sessions and flash messages

Sessions store per-browser application state. Flash messages are small, consume-once notices such as “Changes saved.” Neither should become a place for unrestricted customer data or credentials.

## Session stores

`core/sessions.Store` is backend-neutral. PostgreSQL and Redis connectors provide store implementations. The Core settings vocabulary also accepts `signed-cookie` and `cached-db`, but this release does not supply complete session-store implementations for those choices. Treat them as declared settings, not ready-to-use backends.

Configure store, cookie options, expiry and middleware in the project. Access the current session through `sessions.FromContext`. Changes are persisted at the middleware's response boundary, so handlers must not continue mutating the session after response completion.

Use the account service's login/logout integration to rotate and invalidate identities correctly. A session ID is not authorization to access a model. Version checks, expiry and provider failure must remain distinguishable from a genuinely missing session.

## Cookie boundaries

Use secure cookies under HTTPS and the appropriate SameSite/HTTP-only settings. Keep signing keys outside source and rotate them deliberately. Signed values are tamper-evident, not necessarily confidential. Do not claim immediate server-side revocation for a purely client-held value without a current server-side authority check.

## Flash message modes

| Mode | Storage |
| --- | --- |
| `messages.Session` | Session-backed notices |
| `messages.Cookie` | Signed, explicitly public cookie messages |
| `messages.Fallback` | Configured cookie/session fallback behavior |

Levels are Debug, Info, Success, Warning and Error. Configure minimum level, message count, total bytes and cookie bytes. Private messages require session storage; they must not silently spill into a cookie.

Install message middleware inside session middleware when the selected mode needs sessions. Add messages after the underlying operation has a confirmed outcome. Consuming a notice is not an audit record or proof that a background task completed.
