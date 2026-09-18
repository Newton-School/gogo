# Create, update, and delete through an API

The [product tutorial](tutorial-api.md) deliberately exposes reads only. Add mutation handlers separately, after choosing authentication, row ownership, writable fields, and an audit model. Do not turn on anonymous writes to bypass missing account integration.

## Choose the operation

| Operation | Public factory | Success | Your required integration |
| --- | --- | --- | --- |
| POST | `Resource.CreateHandler` | 201 and Location | Input serializer, fresh model factory, write policy, atomic audit, local URL |
| PUT/PATCH | `Resource.UpdateHandler` | 200 | Key decoder, input serializer, factory, write policy, atomic audit |
| DELETE | `Resource.DeleteHandler` | 204 | Key decoder, freshly collected deletion-graph policy, atomic audit |

All three also depend on the resource's `Policy` and `Scope`. Scoping only the read endpoint does not authorize a write. See [authentication](auth.md) and [security](security.md) before mounting these endpoints.

## Register create explicitly

This wiring fragment belongs in the app after constructing an authenticated resource. `writePolicy` and `writeAudit` are application functions with the signatures below; the audit must persist with the supplied transaction context and the resource's backend, not emit a log or return a placeholder nil.

```go
input, err := api.New(api.Definition{Fields: []api.Field{
    api.StringField("name"),
    api.DecimalField("price", 12, 2),
}})
if err != nil { return nil, err }

create, err := resource.CreateHandler(api.CreateOptions{
    Input: input,
    Factory: func() models.Model { return &Product{} },
    Prepare: func(_ context.Context, row models.Record) error {
        // Publication is a server-owned decision, not an accepted input field.
        return row.Set("published", false)
    },
    ValidateWrite: writePolicy,
    Audit: writeAudit,
    Location: func(_ context.Context, key api.Values) (string, error) {
        return fmt.Sprintf("/api/products/%v/", key["id"]), nil
    },
})
if err != nil { return nil, err }
routes = append(routes, urls.Path("products/", create, "product-create", "POST"))
```

`writePolicy` has type `func(context.Context, auth.Principal, models.Record) error`; it checks current authority on the fully prepared proposed row without mutating it. `writeAudit` has type `func(context.Context, api.MutationEvent) error`. Your mutation scope must include rows the caller can create; reusing the public `published=true` scope while preparing a draft will fail the final scope check. Keep public and management resources separate when their scopes differ.

Use an explicit local-development `CSRFConfig` only for HTTP development. The default secure cookie setting is intentional. Cookie/custom identities require CSRF; an arbitrary Authorization header does not create a verified bearer identity or exemption.

## Updates preserve presence information

PUT validates full input; PATCH validates supplied fields without applying defaults to omitted fields. `false`, `0`, `""`, `null`, and omission are different. Do not decode into a Go value that loses the distinction before handing input to the serializer.

Enable resource entity tags and `UpdateOptions.RequireMatch` when callers must provide a current `If-Match`. A missing required condition returns 428; a stale condition returns 412 without a write. Fetch the detail again after a successful update to obtain a fresh validator.

## Deletion authorizes the current graph

Deletion may affect children, relation updates, and join rows. The handler recollects and locks that graph before applying its policy. Do not authorize only the root record or reuse an earlier preview as permission for a later request. Deletion does not accept the create/update idempotency receipt contract in this release.

## Retries and idempotency

Register `api.IdempotencyMigrations()` and configure the create/update idempotency options before accepting operation keys. Use a stable operation version, trusted tenant/resource scope, and removal-only replay redaction. The same key with different arguments returns 409; current authorization still applies to a replay.

Once mutation processing begins, inspect `X-Gogo-Mutation`:

| Value | Meaning | Caller action |
| --- | --- | --- |
| `unchanged` | No confirmed mutation from this path | Correct the failure before retrying |
| `committed` | Database work committed, even if later reporting failed | Do not blindly create another operation |
| `unknown` | Commit outcome could not be confirmed | Reconcile; preserve the same operation key and arguments |

Do not translate all errors to “nothing happened.” A database/provider timeout can leave an unknown outcome.

## Read working boundary tests

These repository tests show complete store, policy, audit, and HTTP wiring against disposable PostgreSQL:

- [Create and CSRF](../../tests/integration/api_create_test.go).
- [PUT/PATCH, conditions, receipts and rollback](../../tests/integration/api_update_test.go).
- [Delete and graph effects](../../tests/integration/api_delete_test.go).

Run them from the framework checkout with `GOGO_TEST_REQUIRE_SERVICES=1 go test ./tests/integration -run 'TestPostgresAPI(JSONCreate|HTTPUpdate|HTTPDelete)'`. They require supported local PostgreSQL tooling and refuse arbitrary application databases.

Detailed contracts: [create](detail-core-api-create.md), [update](detail-core-api-update.md), [delete](detail-core-api-delete.md), and [idempotency](detail-core-api-idempotency.md).
