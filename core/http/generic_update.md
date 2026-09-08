# Model-backed update forms

`NewUpdateView(UpdateViewOptions)` edits one existing, scoped local model using
an application-owned HTML template and builtin ModelForm controls. It requires
request authorization, model/object `change` permission, a proposed-record
policy, builtin CSRF, row locking and an owned durable transaction. It never
inserts a missing record. Register the handler on an ordinary named route.

This slice does not implement relations, files, arbitrary widgets/persistence,
DeleteView, version/ETag matching, automatic audit registration or automatic
replay. A row lock serializes current POST requests; it does not detect edits
made since a user opened an older browser form. Applications that require that
additional conflict protocol must use an explicitly versioned write boundary.

## Declaration and representation

Provide `Store`, `Model`, `Factory`, `Key`, `Policy`, `Scope`, `Fields`,
`ValidateWrite`, `SuccessURL` and the embedded `TemplateViewOptions.Authorize`.
The backend must advertise transactions and row locks. Verified principals
come from trusted authentication middleware; `AllowAnonymous` is an explicit
selection option, never a permission grant. Token ceilings constrain the policy.

The model must be concrete, managed and single-table, with at most 64 local
intrinsic scalar/JSON fields. Custom codecs, relation/file/file-path fields,
generated expressions, inheritance and opaque declaration metadata are refused.
The schema and input allowlist are bounded and captured at registration.
`ReadonlyFields`, primary keys, auto fields and noneditable descriptors cannot
be submitted or rendered as editable controls. At least one field must be
editable. Unknown fields and repeated scalar values are rejected, not ignored.

`Key` supplies every primary-key component by its exact schema name. It runs
after the root `Scope` and receives a fresh frozen request. Return
`ErrInvalidLookup` for a missing/invalid key; other errors are operational.
Intrinsic conversion supports composite keys and custom physical column names.
There is no query replacement hook or unscoped lookup fallback.

`Factory` returns a fresh matching, unpersisted model. The view hydrates every
stored field from the detached scoped row and marks the private instance as
persisted on the selected database. Factory defaults cannot replace existing
values. There is deliberately no `Initial` callback. POST fields omitted from
the body use ordinary bound-form empty/required rules, not the existing value as
an implicit fallback.

`Prepare` can set server-owned data after valid binding. The fixed update column
set contains all supported non-PK stored fields, including those omitted from
the input allowlist. This preserves the current locked values while permitting
explicit server preparation and ordinary auto-update timestamps; it does not
permit clients to submit readonly fields. Schema, identity, database alias and
persisted state cannot be changed by callbacks.

Configuration, hook lists, selected row values and allowed declaration metadata
are detached. Each policy call receives its own bounded record and identity.
`ValidateWrite` runs for the normalized proposal and stored result; object
`change` permission is checked on the original row and repeated on proposed and
stored data. The policy record is not the mutable model that will be saved.

## Template and JSON controls

The template receives only the same reserved form map as CreateView:

| Name | Value |
| --- | --- |
| `form.html` | Escaped, accessible builtin `Form.Render("div")` controls |
| `form.is_bound` | Whether this request submitted the form |
| `form.is_valid` | Local form validation result |
| `form.errors` | Plain field/form messages, without provider causes |
| `csrf_token` | Builtin masked CSRF token |

No `object`, model, Form, BoundField, Store, principal or widget is automatically
added to template context. Readonly and key values are not automatically
disclosed. Application context producers remain explicitly trusted output.
The application owns the named template and outer form; there is no fallback
template:

```html
<h1>Edit note</h1>
<form method="post">
  <input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">
  {{ form.html }}
  <button type="submit">Save changes</button>
</form>
```

Free-form JSON controls receive encoded JSON document text, so stored scalar
strings retain quotes and large numbers remain exact. The private Update
control distinguishes literal `null` (JSON null) from an empty control (SQL
NULL, subject to required/nullability rules). It retains the ordinary JSON
cleaner for other values and the declared validators. This is not a change to
global forms behavior. Each JSON document remains limited to 1 MiB; whole row
and template snapshots retain their 8 MiB / 65,536 visited value/key budgets.
Builtin JSON choices continue to use their declared choice controls.

## Request and persistence sequence

GET/HEAD performs a scoped, fully completed single-object read and renders
current editable values. It does not open a transaction, lock or save.
Authorized opt-in OPTIONS reports allowed methods without loading an object or
creating a form. Unsupported methods return 405 before operation hooks.

POST accepts only UTF-8 URL-encoded bodies: 1 MiB by default, at most 10 MiB,
at most 256 encoded items and one value per accepted field. The view uses
private parsed form caches; it does not trust caller-populated Form/PostForm.
Builtin CSRF checks every POST, with no exemption. Nil `CSRF` uses secure
defaults; explicit trusted-origin/cookie configuration must match deployment.
TLS and trusted proxy handling remain the application's responsibility.

After CSRF, POST refuses an ambient same-alias transaction and opens
`db.Atomic(Durable: true)`. It selects the original scoped row with a root-only
`FOR UPDATE` lock and drains/closes its result before using it. It binds and
validates a fresh hydrated instance, then saves using `ForceUpdate` and the
fixed explicit nonempty update-column list. There is no insert fallback.

Readonly grants run after final model/constraint normalization. A prewrite
stored-state fence detects a callback that changed the locked row using the
same transaction. A private first after-save hook captures the actual stored
row before application after-save hooks; database trigger normalization is
therefore part of the result that must be authorized.

The local root-relative `SuccessURL` is sealed before commit and receives only
detached original primary keys. Final authorization and write-policy calls are
followed by an exact scoped persisted-row/identity fence. No application URL,
grant or template hook is run after that fence. Existing Store hooks may write
same-transaction audit records; the view adds no new mandatory audit facility.

Invalid forms are materialized inside the owned transaction and then rolled
back, including any transaction-bound effects from trusted hooks. The response
is emitted as 422 only when that rollback returns the exact private invalid
sentinel without a joined cleanup/provider failure. Invalid-form OnCommit
callbacks never execute. Cleanup failure is operational, not user input error.

Hooks, model `Clean`, validators, authorization, Scope, templates and providers
are trusted application code. Read hooks and grants must be readonly. The view
does not claim it can prevent a custom callback from issuing its own SQL, or
that nontransactional side effects can be rolled back. It has a cooperative
five-second default timeout (one millisecond–one minute); there is no forced
goroutine timeout of noncooperative callbacks.

## Outcomes

Missing/scoped-out objects and object permission denials return 404. Request or
model-level permission denial returns 403 (unauthenticated access 401).
Malformed/unknown/duplicate form fields return 400, CSRF denial 403, unsupported
media 415, oversize 413 and invalid forms or known pure constraints 422.
A forced update that affects no row is 409, never a successful insert.
Provider, cancellation, mixed cleanup and malformed result errors return 503.

A private first OnCommit marker is the only proof of the owned outer commit.
Only confirmed commit plus prepared success returns 303. A known committed
callback failure returns an explicit committed 503 telling the caller not to
resubmit. An unconfirmed commit returns an unknown-outcome 503 requiring
reconciliation; it is not proof of rollback. A callback cannot forge commit
proof by returning a committed-error type. Late cancellation cannot undo an
already observed successful commit. No failure triggers automatic replay.

All responses are private/no-store with HEAD body suppression. Transfer errors
or short writes abort the HTTP exchange without retrying or appending another
response. A redirect does not itself authorize its destination; the destination
view must apply its own current scope and permissions.
