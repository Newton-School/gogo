# Authentication

Register models and migrations explicitly. Importing this package opens no
connections, creates no tables, registers no routes and starts no background work.

| Capability | Models | Migrations |
| --- | --- | --- |
| Default users/groups/permissions | `auth.Schemas()` plus content types | `contenttypes.Migrations()`, `auth.Migrations()` |
| Password reset | Add `auth.PasswordResetSchemas()` | Add `auth.PasswordResetMigrations()` |
| Opaque API tokens | Add `auth.TokenSchemas()` | Add `auth.TokenMigrations()` |

## API tokens

`auth.NewTokens` takes the existing `Accounts` service, an explicit mutation
authorizer, and a TTL (default 24 hours). A missing mutation authorizer denies
issuance and revocation. Its policy receives the exact token ID, subject, scopes
and expiry; it must authorize the current actor and subject, not merely a
client-supplied ID. Token lifetime can be passed from `GOGO_AUTH_TOKEN_TTL`.

```go
tokens, err := auth.NewTokens(auth.TokensConfig{
    Accounts: accounts,
    Authorize: authorizeTokenChange,
})
// Handle err before mounting or issuing credentials.
```

`Issue(ctx, userID, []string{"catalog.view_product"})` requires explicit current
subject grants. It returns `TokenIssue.Secret` only after a confirmed successful
commit. `Secret.Reveal()` is an intentional one-time handoff to the trusted client:
never log it or put it in a URL. Ordinary JSON and formatting cannot reveal it.
The token row stores only a purpose/identity-bound digest of a 256-bit secret.

Mount `auth.BearerMiddleware(tokens)` on non-cookie-authenticated routes and use
`auth.ModelPolicy` or `auth.ConstrainPolicy(projectPolicy)` for resource checks.
Production transport must enforce HTTPS at the trusted server/proxy boundary.
Only the Authorization header is consumed; query/form/cookie credentials never
provide a fallback. Invalid credentials produce 401, provider failure 503.

Scopes are exact `app.codename` ceilings, not grants. A token cannot bypass the
current subject's permissions, object policy or tenant scope. Superuser/custom
policy checks still respect the ceiling. Account and token service mutations
also apply their corresponding model-action ceiling before their explicit
delta authorizer. A token identity cannot be serialized as a verified principal
or promoted through Login/RefreshLogin into an unrestricted session.

Rotation is explicit: issue a replacement, hand it off, then `Revoke` the old ID.
These are separate commits, not an atomic credential exchange. Revocation is
idempotent and invalidates subsequent authentication, not an already-running
request. Password/grant changes invalidate previous tokens through `auth_version`.
An unusable password does not prevent a separately authorized API credential.

Changed/unknown commit failures never expose a secret and must not be retried
automatically. After-commit errors and uncertainty on a confirmed no-op revoke
remain `TokenUnchanged`; they are not reported as new revocations.

## Browser credential flows

`core/auth/views` provides login/logout, self-service password change and reset
request/confirmation handlers. Configure shared CSRF, rate limits and safe local
destinations. Retaining a session after a password change is explicitly opt-in;
reset confirmation never logs in automatically.

Reset mail resolves an explicitly verified recipient. The synchronous path
commits a single-use digest before attempting Sensitive mail. Reset links carry
the secret in the fragment, not a server-visible query/path. Mount
`PasswordResetConfirmScript()` at `PasswordResetConfirmScriptPath()` so the
browser moves it into the POST form and removes it from history. Without
JavaScript the form supports manual token paste. No reset token belongs in
ordinary Async payloads.

The current implementation does not yet include encrypted durable reset mail,
credential maintenance commands, a built-in JWT/OIDC provider, the complete
legacy password-hasher catalog or a completed security-audit catalog. Custom
token verifiers implement `TokenBackend` and must enforce issuer, audience,
algorithm allowlists and a current constrained identity themselves.
