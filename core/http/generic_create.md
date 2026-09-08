# Model-backed creation forms

`NewCreateView(CreateViewOptions)` is an opt-in HTML creation handler. It uses a
named application template, builtin ModelForm controls, mandatory request and
model `add` authorization, mandatory proposed-record authorization, builtin CSRF,
and an owned force-insert transaction. It does not implement update/delete,
relations, file uploads, custom persistence, custom widgets, or automatic Admin
registration. Register it with the ordinary named router.

## Declaration and ownership

Embed `TemplateViewOptions` for the template, bounded explicit context and
cooperative timeout (default five seconds, accepted one millisecond–one minute).
Provide `Store`, `Model`, `Factory`, `Policy`, `Scope`, `Fields`, `ValidateWrite`
and `SuccessURL`; `Authorize` remains required even for a deliberately public
form. Verified principals come from trusted authentication middleware.
`AllowAnonymous` is opt-in, not a permission grant. Token ceilings still constrain
the model policy.

`Fields` is an input allowlist, not a saved-object representation. Declare one
to 64 local intrinsic scalar/JSON fields on a concrete managed single-table
model with at most 64 fields. Relations, custom codecs, generated expressions,
file/file-path fields and opaque declaration values are refused. Primary keys,
noneditable fields and names in `ReadonlyFields` never become editable controls.
There must be at least one editable field. Any submitted unknown, readonly or
key field is rejected; the view never assigns it. Repeated scalar values are
rejected rather than selecting an arbitrary first or last value.

`Factory` returns a fresh matching, unpersisted model. `Initial` is optional,
bounded presentation data for editable controls on GET/HEAD only. It cannot set
ownership or fill omitted POST fields. `Prepare` optionally fills server-owned
values after binding, inside the transaction. Ordinary Store hooks/defaults and
auto-field preparation follow; final model/constraint validation and readonly
proposed-record grants occur before INSERT. A provided primary key remains a
force insert, never an upsert.

Configuration lists, plain declaration values, builtin template loaders and
store hook lists are captured at construction. Each authorization callback gets
a fresh request or detached bounded record, not the mutable form instance.
`ValidateWrite` runs for both proposed and persisted data. Auto/database-default
values can be nil in the proposed record until RETURNING resolves them; the
record's schema is not changed to nullable. Persisted primary keys must be valid.

Hooks, validators, model `Clean`, provider services, loaders and processors are
trusted application code. Authorization, Initial, Scope and template hooks must
be read-only. There is no goroutine per request or forced timeout of
noncooperative callbacks. The framework does not issue SQL for safe-method forms
or invalid local binding, but cannot prevent a custom `Clean`/validator from
issuing its own SQL. The first ModelForm pass deliberately defers database
uniqueness/constraint checks to final preparation in the owned transaction.

## Template contract

Reserved values override application context producers:

| Value | Content |
| --- | --- |
| `form.html` | Trusted HTML generated only by builtin `Form.Render("div")` |
| `form.is_bound` | Whether this is a POST form |
| `form.is_valid` | Local binding result |
| `form.errors` | Plain field/form message lists, with no provider cause |
| `csrf_token` | Builtin masked CSRF token |

No Form, BoundField, widget, model, principal, Store or saved-object projection is
passed into template context. Builtin controls escape labels/help/submitted
values/errors and include label/description/error associations. The application
owns the outer form, hidden CSRF input, submit button and surrounding layout:

```html
<form method="post">
  <input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">
  {{ form.html }}
  <button type="submit">Create</button>
</form>
```

There is no embedded fallback template. Generic context limits remain 8 MiB and
65,536 visited values/keys; output defaults to 8 MiB with a 16 MiB hard maximum.
One captured locale resolver selects form cleaning and template rendering.

## Request and transaction outcomes

GET/HEAD render an unbound form without opening a transaction or saving.
Authorized opt-in OPTIONS reports `GET, HEAD, POST, OPTIONS` and does not create
a form. Other methods return 405. POST accepts only UTF-8 URL-encoded forms,
with a default 1 MiB body limit and a 10 MiB hard maximum, at most 256 encoded
items and one value per accepted field. Multipart and JSON return 415;
malformed forms return 400, oversized bodies 413, and invalid local forms 422.

Builtin CSRF runs for every POST. There is no exemption option. Nil `CSRF` uses
Secure, HttpOnly, SameSite=Lax cookies. An explicit configuration may select
trusted origins/cookie behavior for the deployment; the view owns its body cap.
It ignores inherited mutable form caches and validates only its privately parsed
body and captured headers. The caller must configure TLS/trusted proxy handling
correctly. Every response is private/no-store and suppresses a HEAD body.

POST refuses an ambient same-alias transaction and opens `db.Atomic` with
`Durable: true`. It registers a private first OnCommit marker before application
callbacks, then uses `Store.Save(ForceInsert: true)`. Existing hooks may write
audit records using the same transaction context; there is no newly required
audit provider. The view captures the RETURNING record before user AfterSave
hooks, checks it under the frozen root scope, and rechecks grants. `SuccessURL`
receives only a detached identity map. Its bounded local URI is validated before
commit. After SuccessURL and final grants, the persisted identity and every
declared field must still match within the same scoped transaction; mutation,
deletion or loss of visibility rolls back. Scope values and provider callbacks
must honor their existing readonly ownership contracts. This is not general
serializable isolation for unrelated ACL/state owned by another system.

| Result | HTTP behavior |
| --- | --- |
| Confirmed durable success | 303 to the sealed local Location |
| Invalid prepared model or known constraint rejection | Safe 422, no Location |
| Exact authentication/permission denial | 401/403, no Location |
| Operational failure before commit | Safe 503, no Location; transaction rolls back |
| Uncertain commit acknowledgement | 503 instructing reconciliation before retry |
| Confirmed commit, failing after-commit callback | 503 explicitly saying committed; do not resubmit |

Late cancellation cannot undo a confirmed commit. A public model Persisted flag
or a callback returning `CommittedCallbackError` is not proof of commit. There is
no automatic retry/idempotency guarantee: use application reconciliation and
durable business identifiers where needed. Transfer failure aborts the HTTP
response rather than appending a second error document. No browser-level
verification or broader generic edit-view parity is claimed by this slice.
