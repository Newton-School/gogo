# Read-route OpenAPI documents

`Resource.ReadOpenAPIRoutes` binds ordinary list/detail handlers and their output schemas to the same private resource snapshot. `api.OpenAPI` then returns deterministic OpenAPI 3.1.1 JSON for that API router. Neither operation executes a request, queries a database, samples a callback, fetches a schema or creates a file/server.

```go
routes, err := books.ReadOpenAPIRoutes("books/", "book", api.ReadOpenAPIOptions{
    Security: []api.OpenAPISecurity{{Type: "bearer"}},
})
if err != nil {
    return err
}
apiRouter, err := urls.New(urls.Include("v1/", "v1", routes...))
if err != nil {
    return err
}
document, err := api.OpenAPI(ctx, apiRouter, api.OpenAPIOptions{
    Title: "Books API", Version: "1.0.0",
})
```

Here `books` is an explicitly configured `Resource`: registered model, real store, serializer, policy and row scope are still required. Install authentication middleware separately before serving requests. The code emits `/v1/books/` and `/v1/books/{pk}/`, with operation IDs such as `v1:book_detail.get`. Include the same API router in the application; do not rebuild different handlers for documentation.

## Request and document contracts

| Surface | Generated contract |
| --- | --- |
| List GET | Public serializer results; exact configured count inclusion; optional next/previous links. Cursor pagination has no previous link. |
| Detail GET | The public output object. Optional ETag header and bodyless 304 when `EntityTags` is enabled. Reads and current authorization still precede conditional success. |
| HEAD | Same parameters and resource authorization as GET; no response content schemas. |
| OPTIONS | Router method discovery: bodyless 204 and Allow header. No resource query or policy callback. Outer middleware may still authenticate or deny it. |
| Filters | Only explicitly declared query names; typed text operands, repeated parameters for IN/range, exact true/false for isnull. These are not serializer input schemas. |
| Pagination | Actual normalized page, limit/offset or signed cursor configuration and defaults. The runtime also enforces compound page/offset and query-size constraints. |
| Search/order | Present only when enabled, with term and ordering limits. Public ordering names come from the explicit ordering allowlist. |
| Errors | Public JSON error envelope plus plain-text router errors. HEAD remains bodyless. External middleware error bodies are outside this API-router contract. |

The document uses JSON Schema 2020-12. It describes the serializer's actual output null/blank rules, public names, nested objects and read-only fields; it omits hidden and write-only fields. An `AllowField` policy removes root required-field promises because it may redact output. It does not weaken a nested serializer's own required contract. The date/schema names of model fields do not imply JSON format assertions or publish database constraints.

For arbitrary representation or unknown output types, supply `Field.OutputSchema` as described in `output_schema.md`. A typed field with a compute function still passes through its declared output conversion. Generation never executes that function to guess a type. Explicit custom declarations remain application assertions, not a runtime validator for arbitrary callback output.

## Authentication must be explicit

| Declaration | Meaning |
| --- | --- |
| `{Type: "public"}` | Anonymous access is configured on the bound resource. Policy and scope still execute for GET/HEAD. |
| `{Type: "bearer"}` | Application middleware verifies a bearer credential and supplies a trusted principal. |
| `{Type: "session", CookieName: "books_session"}` | Application middleware verifies the named session cookie and supplies a trusted principal. The actual cookie name is required. |

Multiple declarations mean alternatives (OR), not simultaneous requirements. At least one declaration is required; duplicates, invalid cookie names and public declarations on non-anonymous resources are rejected. Declarations do not install middleware, parse credentials or grant permissions. Never embed credentials in schema metadata. Protect the document's eventual delivery route independently if its public API description is not intended for anonymous readers.

## Snapshot and failure boundaries

Binding privately copies the resource, store handle, nested serializer handles and security declarations. Replacing those original exported handles afterward cannot change the bound document or handler. Backend, registry, policy, scope and other executable provider dependencies remain trusted read-only application dependencies; they are not database snapshots or sandboxed callbacks.

Generate from a selected API router constructed with `urls.New`. Literal Include prefixes and built-in single-key resource routes are supported. Every selected route must carry a bound read contract. Old unbound `Routes`, opaque wrappers, mutations, regex/custom converters, extra path parameters, altered methods/suffixes and duplicate or overlapping paths fail with `ErrOpenAPI`; nothing silently disappears from the document. `NewWithConverters` is deliberately unsupported even when passed a map named like the built-ins. Put authentication around the assembled router instead of replacing individual bound handlers.

Registration has the serializer graph's depth/node/size bounds. Generation permits at most 256 flattened routes, 256 parameters per operation and 8 MiB of schema data/encoded output. Prefixes, names, options and declarations have additional size/character limits. The complete primitive projection is frozen before context callbacks. Invalid/cyclic/unsupported metadata, callback panic or exceeded limits return no partial bytes; observed cancellation/deadline errors remain cancellation/deadline errors. Returned bytes belong to the caller.

This is the read-only OpenAPI slice. Input and mutation schemas, arbitrary route metadata, typed clients, a schema management command and a browsable documentation UI are not implemented by this API. It is not an exhaustive Django/DRF compatibility or release claim. OpenAPI semantics follow the [3.1.1 specification](https://spec.openapis.org/oas/v3.1.1.html); inline schemas inherit the document dialect without incorrectly adding standalone `$schema` declarations inside subschemas.
