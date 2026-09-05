# Default account administration

Stock account and group titles use their scoped identifier/name. Their lookup,
edit-token and audit IDs remain separate. `Config.ActorLabel` optionally supplies
authorized header text for the current staff principal; the default is its ID.
The callback must be read-only and concurrency-safe; its output is escaped and bounded,
and failure or cancellation aborts rendering without a fallback identity.

Register `AccountStore.UserAdmin()` and `AccountStore.GroupAdmin()` on a Site.
The same ORM transaction contains the Accounts mutation and the Admin audit.
Site model/object permissions, row scope, and explicit `Accounts.Authorize`
approval all apply; staff status alone never authorizes credential or grant edits.

Stock account forms support:

- Users: identifier, active/staff/superuser flags, separate creation/password
  forms, explicit password-disabled creation, groups and direct user permissions.
- Groups: name creation/rename and permission selection. Grant changes invalidate
  member authentication versions through Accounts.
- `ReadonlyFields`, `GetReadonlyFields`, `Fields`, `Fieldsets`, and `Exclude` for
  `groups`, `user_permissions`, and group `permissions`. These are stock form
  names, not extra fields or migrations in the auth model schemas.

Selectors require target-model view permission before lookup and apply target
row scope plus object permission. Only visible choices appear in HTML or audit.
Existing hidden links are retained server-side, but the account authorizer must
still approve the complete resulting set. Every explicitly submitted identity
is revalidated after hooks, even when it was already selected. A signed visible
selection snapshot rejects stale edits. Readonly or omitted fields ignore
extra POST keys.

The current stock selector is bounded at 1000 choices/links and requires a
narrower application scope above that limit. Stock grant `FormOverrides` are
explicitly rejected: custom validators/widgets are not silently accepted.
Use the scoped `AccountGrantReader` / `AccountGrantEditor` ports for a custom
account UI; an editor requires an active transaction and exact domain authority.
These form-only names are not supported in list filters, ordering or columns.

Account/group deletion remains denied. This implemented slice is not complete
Django Admin parity. Unknown commit outcomes return a safe review-required
response, never a success redirect or an instruction to repeat automatically.
