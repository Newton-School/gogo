# REST APIs and serializers

An API has three independent decisions: which rows a caller may see, which fields may be represented, and which operations may change data. Gogo requires you to make these decisions explicitly.

## Define an output serializer

The showcase declares a public product representation without internal notes or account data:

{{code examples/showcase/apps/catalog/serializers.go}}

`api.FromModel` is available when you want to derive fields from a schema, but it still requires a field allowlist. A readonly field is still readable; omit a secret or use the correct write-only/hidden behavior.

## Expose scoped read routes

{{code examples/showcase/apps/catalog/urls.go}}

This complete app file receives an initialized store from project configuration. Its `Policy` permits only viewing, while its `Scope` restricts rows to published products. Include the returned routes under your chosen project prefix. The showcase uses `/api/v1/`.

Scope applies before filtering, counts, pagination and related-data loading. Object checks add protection but cannot repair an earlier unscoped count.

## Serializer field catalog

| Family | Constructors |
| --- | --- |
| Text | `StringField`, `EmailField`, `URLField`, `SlugField`, `IPAddressField`, `UUIDField` |
| Numbers and boolean | `IntegerField`, `FloatField`, `DecimalField`, `BooleanField` |
| Time | `DateField`, `DateTimeField`, `TimeField`, `DurationField` |
| Structured values | `JSONField`, `ListField`, `DictField`, `NestedField` |
| Choice and computed values | `ChoiceField`, `ComputedField` |
| Uploaded data | `FileField`, `ImageField` |
| Model scalar adapter | `Scalar` |

These are the 23 constructors exercised by the showcase recipes. Configure nullability, required input, source mapping, `Hidden`, `ReadOnly`, `WriteOnly`, validators and representation callbacks through the actual `api.Field` definition. There is no Django-style `MethodField` or `HiddenField` constructor; use `ComputedField` or the explicit `Hidden` option.

Call `Serializer.Validate(ctx, input, api.BindOptions{...})` to obtain cleaned values, and `Representation` for output. `Save` takes an explicit persistence policy. Partial input, defaults and hidden fields have defined behavior; partial validation is not permission to ignore authorization. Nested validation is not automatic nested database persistence.

## Validate and represent a value

This complete test needs no database or server:

{{code docs/examples/api_test.go}}

## Create, update and delete

Use [Create, update, and delete](api-writes.md) for the registration example, required policy/audit signatures, status codes, conditional requests, and retry decisions. For a first read API with every project file included, follow the [database-backed tutorial](tutorial-api.md).

`NewResource` does not silently enable writes. Add the explicit create, PUT/PATCH and delete handlers with their required factories, row/graph policy, validation and transactional audit callbacks.

| Operation | Required design decision |
| --- | --- |
| Create | Fresh typed model, permitted input and atomic create/audit policy |
| PUT/PATCH | Scoped row lookup/lock, permitted changes, validation, audit; optional required ETag match |
| Delete | Recollect and authorize the current deletion graph; commit cascades and audit together |
| Durable create/update receipts | Explicit idempotency storage, operation key, replay authorization and response redaction |

Do not turn an unknown commit outcome into “nothing happened.” Preserve receipts/operation identities for reconciliation. Delete does not currently support the same receipt replay contract as create/update.

## Transport, filtering and schema

Parsers and renderers have explicit content-type, size and acceptance contracts. Configure allowlisted filters, search fields and ordering; unrecognized query parameters fail rather than becoming arbitrary ORM expressions. Pagination supports bounded page-number, limit/offset and forward cursor modes.

OpenAPI comes from declared route/schema metadata, not unrestricted reflection of every model. The read-route helper used above and the explicit schema/command factories generate that document. OpenAPI availability is not a claim of an interactive browsable API or metadata for every custom action.

The [API recipes](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/api) demonstrate field binding, representation, input denial and transport behavior against the pinned release. They are not a database-backed nested-write tutorial.
