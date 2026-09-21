# Authentication and permissions

Authentication establishes who is calling. Permission policy decides what they can do. Query scope decides which records they can access. You need all three for a secure application.

## Register only the account features you use

| Feature | Schemas | Migrations |
| --- | --- | --- |
| Users, groups and permissions | `auth.Schemas()` plus content-type schemas | `contenttypes.Migrations()`, `auth.Migrations()` |
| Password reset | `auth.PasswordResetSchemas()` | `auth.PasswordResetMigrations()` |
| Opaque API tokens | `auth.TokenSchemas()` | `auth.TokenMigrations()` |

Register these contributions through your app/project setup, apply migrations explicitly, then construct the account services with an ORM store and the required authorizers. Importing `core/auth` does none of these steps automatically.

## User and Group IDs

New User and Group tables use database-generated integer IDs starting at `1`, independently for each table. Leave the ID unset when creating an account. Sequences can have gaps after failed inserts or deletions; IDs are not row counts or access credentials.

{{snippet docs/examples/account_models_test.go account-integer-ids}}

The ordinary `auth.Schemas()` and `auth.Migrations()` helpers use this same default. For larger ranges, choose 64-bit integers **before the first migration**:

{{snippet docs/examples/account_models_test.go account-long-ids}}

Register `identity.Schemas()` and apply `identity.Migrations()` instead of the default auth helpers. Keep content-type, password-reset and token registrations as shown above. Pass the same choice to the account service (or `admin.AccountStoreConfig.Accounts`):

{{snippet docs/examples/account_models_test.go account-service-ids}}

Direct ORM reads also need that choice. Use these factories instead of constructing a default `&auth.User{}` or `&auth.Group{}`:

{{snippet docs/examples/account_models_test.go account-query-ids}}

Use `identity.Group` for groups. Public auth IDs remain Go `string` values for compatibility: an integer account's `user.ID` is `"1"`, while its database column is an integer. Sessions, reset secrets and API credentials remain random.

This is the User/Group configuration, not a global rewrite of every model. Application models choose `AutoField`, `BigAutoField` or another explicit primary-key field in their schema; other contributed models retain their declared ID types.

### Existing UUID databases

Earlier Gogo versions used UUID User/Group IDs. Retain their exact schema and migration checksum by explicitly selecting UUIDs everywhere above:

{{snippet docs/examples/account_models_test.go account-uuid-ids}}

Do not apply the new default initial migration over an existing UUID database, clear migration history, or delete data to bypass a checksum error. Choosing another ID type does not convert existing tables or foreign keys. A deliberate data migration is a separate application operation. No extra environment variable is needed.

## Users, groups and policies

### Understand password handling

This complete test demonstrates the low-level hashing API without storing or printing a credential. Run `go test ./docs/examples -run Example_password -v` from the framework checkout:

**Hash a password**

{{snippet docs/examples/auth_test.go password-hash}}

**Verify submitted input**

{{snippet docs/examples/auth_test.go password-verify}}

Result: correct password `true`, wrong password `false`. `password` is the submitted value; never log it.

<details>
<summary>Complete runnable example, including imports</summary>

{{code docs/examples/auth_test.go}}

</details>

Use the account service's create/change-password methods for actual users so validators, authorization and account security versions are applied. Do not take this low-level example as permission to directly overwrite a user's password column.

The package includes password handling, account services, group/permission grants, current principals, model-action policy, identity edits and session login integration. Account flag/grant changes are security operations, not generic unauthenticated ORM edits.

Use an explicit policy at trusted boundaries. `auth.ModelPolicy` provides model-grant checks; `ConstrainPolicy` preserves token scope ceilings around custom policy. A superuser or permissive custom policy must not widen a restricted token beyond its ceiling.

## Browser login

For a complete integration, follow [Set up a working Admin](admin-wiring.md): it includes schema/migration registration, `auth.NewAuthenticator`, login/logout/password-change handlers, the account store, Redis sessions, and exact middleware order. The showcase uses that same integration at `/admin/login/`; a fresh `startproject` does not mount it automatically.

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
