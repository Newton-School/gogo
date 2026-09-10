# Authentication and permissions

Authentication establishes who is calling. Permission policy decides what they can do. Query scope decides which records they can access. You need all three for a secure application.

## Register only the account features you use

| Feature | Schemas | Migrations |
| --- | --- | --- |
| Users, groups and permissions | `auth.Schemas()` plus content-type schemas | `contenttypes.Migrations()`, `auth.Migrations()` |
| Password reset | `auth.PasswordResetSchemas()` | `auth.PasswordResetMigrations()` |
| Opaque API tokens | `auth.TokenSchemas()` | `auth.TokenMigrations()` |

Register these contributions through your app/project setup, apply migrations explicitly, then construct the account services with an ORM store and the required authorizers. Importing `core/auth` does none of these steps automatically.

## Users, groups and policies

The package includes password handling, account services, group/permission grants, current principals, model-action policy, identity edits and session login integration. Account flag/grant changes are security operations, not generic unauthenticated ORM edits.

Use an explicit policy at trusted boundaries. `auth.ModelPolicy` provides model-grant checks; `ConstrainPolicy` preserves token scope ceilings around custom policy. A superuser or permissive custom policy must not widen a restricted token beyond its ceiling.

## Browser login

`core/auth/views` contains login, logout, self-service password change and password-reset request/confirmation handlers. Mount them explicitly with shared CSRF settings, session middleware, rate limits and safe local redirect destinations.

Session persistence after password change is opt-in. Password reset does not automatically log the user in. Persisted account state must be checked so disabling a user or changing grants cannot be bypassed by stale in-memory identity data.

## API tokens

`auth.NewTokens` needs an account service and explicit mutation authorizer. `Issue` returns a secret only after a confirmed successful commit. Store/distribute it securely; never put it in a URL or ordinary logs. The database stores a digest, not the raw secret.

Use `BearerMiddleware` on non-cookie-authenticated API routes. Only the Authorization header carries the token. Scopes are exact `app.codename` ceilings, not standalone grants. They do not replace current account permissions, tenant scope or object policy.

Rotation means issue a replacement, hand it off safely, then revoke the old identity. Those are separate operations. A missing response or uncertain commit must not trigger blind issuance of another secret.

## Password reset

Resolve a verified recipient, commit the single-use token digest, then deliver sensitive mail through the explicit delivery path. Reset links keep the secret in the fragment. Mount the provided confirmation script so the browser moves the fragment into the POST form and clears it from history; a manual-paste fallback is available.

Do not put password reset secrets into ordinary Async arguments. Encrypted durable reset delivery is not implemented in this release.

## Limits

There is no built-in JWT/OIDC provider, complete legacy password-hasher compatibility catalog or universal credential-maintenance CLI. Custom `TokenBackend` implementations own their issuer/audience/algorithm validation and must return a constrained current identity.

For a runnable staff integration, read [Admin](admin.md) and the showcase's account wiring. Its single-organization superuser policy is not a ready-made multi-tenant authorization model.
