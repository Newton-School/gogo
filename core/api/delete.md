# Explicit resource deletion

Register `Resource.DeleteHandler(keyDecoder, DeleteOptions)` explicitly in an
app's `urls.go`. `Resource.Routes` stays read-only. The decoder returns exactly
the model's primary-key components. `ValidateDelete` and transactional `Audit`
are mandatory; registering a model never makes it deletable through HTTP.

| Stage | Behavior |
| --- | --- |
| Request | DELETE only; verified active authentication, token scope ceilings, JSON response negotiation and cookie CSRF/origin checks. No body, query, operation key or unsupported conditional headers. |
| Identity | Decode and validate a detached key, then lock the current scoped root. Missing/hidden rows return 404. Invalid decoders return `ErrInvalidKey`; operational errors remain failures. |
| Precondition | Optional strong `If-Match` compares the authorized representation under lock. Stale tags return 412; `RequireMatch` needs `EntityTags` and returns 428 when absent. `*` checks scoped existence, not a revision. |
| Collection | Recollect and lock the complete scoped deletion graph inside the transaction. Bound object count and graph work. Protected/restricted graphs or exceeded limits fail without deletion. |
| Graph authority | Require delete permission on every deleted object, change permission on each relation-updated object, and scoped view permission/lock on each non-null default target. An automatic join derives authority from its deleted endpoint; the hidden opposite endpoint is not traversed or deleted. |
| Policy | Call `ValidateDelete` with a fresh detached read-only graph, including cascades, updates and automatic join removals. Current-view mutations reject the transaction; retained old views cannot retarget later checks. |
| Effects | Run the normal ORM delete hooks and relation operations. The coordinator uses disposable internal records, never clears the caller's application model before an outer commit. |
| Audit | Run `Audit` in the same outer transaction with action `delete` and a detached root identity. Hook, policy or audit failure rolls back all effects and audit writes together. |
| Final checks | Reauthorize the original graph after audit, then prepare every remaining row scope. Exact private reads verify deleted objects/join rows remain absent, surviving relations have the intended value and their authorized targets are unchanged. |
| Commit | Return 204 with no body only after confirmed durable commit. No callbacks or automatic retries are added after the final effect checks. |

The handler snapshots its store backend, complete registered model inventory and
delete-hook slices when registered. Later edits to the original store/registry
do not redirect an in-flight deletion. Policies are trusted application services;
they must not perform unrelated writes. Scope callbacks **and values in their
returned predicates** must be pure/read-only, including custom SQL value or JSON
encoding methods. The effect checks are not a sandbox for application code.

The second `ValidateDelete` receives the original graph after its rows may have
been deleted. It must authorize those snapshots without requiring an already
deleted root to reload. Each call receives a new detached view. Audit/outbox SQL
must use the supplied transaction context; external delivery is not atomic with
the database and belongs after commit or in an explicitly configured outbox.

This generic path supports concrete managed models with built-in stored-field
codecs, including ordinary relations and automatic many-to-many intermediaries.
Custom codecs (including nested array element codecs), inheritance and
proxy/unmanaged/abstract deletion graphs require an explicit domain coordinator.
The database remains authoritative for hidden referencing rows and DO_NOTHING
constraints: the handler must not broaden scope to delete a hidden dependency.
Unconstrained relations retain the ORM's explicit-policy refusal.

Default limits are 30 seconds, 1,000 objects and 32 work units per configured
object. Allowed bounds are 1 ms–5 minutes, 1–10,000 objects, and work at least
the object limit and at most 1,000,000. Cancellation is cooperative; callbacks
and providers must honor their context. Unknown/chunked or middleware-owned
bodies are rejected without probing a reader; an absent HTTP body is represented
by nil or `http.NoBody`. Cookie clients send their CSRF token in the header.

`X-Gogo-Mutation` distinguishes `unchanged`, `committed` and `unknown` once
mutation processing begins. A pre-commit failure does not report success;
after-commit callback failures can return an error with `committed`. Lost commit
acknowledgement returns 503/`MUTATION_UNKNOWN`, not a fabricated success or
rollback. Mixed operational and permission errors cannot become completed
object-denial decisions. Responses are private/no-store and never expose
deleted data, hidden dependency counts, provider text or audit payloads.

DELETE receipt replay is deliberately **not enabled**. `Idempotency-Key` is
rejected because an already deleted object cannot use ordinary current-row
replay authorization. A later DELETE may return 404; it does not prove which
request deleted that object. Reconcile unknown outcomes with the application's
authorized audit/domain evidence; do not blindly retry under a replacement key.
This is one explicit resource operation, not full generic-view/API parity.
