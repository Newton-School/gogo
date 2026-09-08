# Serializer output schemas

`Serializer.OutputSchema` builds a bounded, deterministic JSON Schema 2020-12 document from the serializer's **public output**. It does not read a database, generate a complete OpenAPI document, register routes, or execute application callbacks.

```go
document, err := serializer.OutputSchema(ctx, api.OutputSchemaOptions{})
```

Only declared public field names appear. Hidden and write-only fields are excluded; private `Source` names, model defaults, choices, validators, database constraints and model registration are not a field-discovery mechanism.

## Match the wire, not the input form

- Successful ordinary representations include required fields; optional missing fields may be omitted. Set `Redactable: true` when a surrounding resource can remove fields, such as `ResourceConfig.AllowField`. This removes only the root required-field promises, not nested serializers' own contracts.
- Output fields permit JSON null because the serializer accepts nil before running a field codec. This is independent of input `AllowNull`.
- Decimal, date, time, datetime and Go duration representations are strings. Integer values stay JSON integers without conversion through floating point. Date/time/duration format assertions are not inferred from their names.
- A model field with `Blank` can return the empty string before scalar conversion, including a normally numeric or boolean field. The generated schema represents this exception without changing runtime serialization.
- Lists and dictionaries carry their output size limits. Nested serializers expose only their own declared public fields. JSON fields explicitly permit arbitrary JSON values.

## Declare ambiguous custom output

An arbitrary representation callback cannot be inferred safely. Declare its output shape through `Field.OutputSchema`:

```go
field := api.ComputedField("display", computeDisplay)
field.OutputSchema = &api.WireSchema{Type: "string"}
```

The declaration describes the callback's non-nil output; the outer nil path remains permitted. It is an application contract, not runtime validation of the callback. Generation never calls `Compute`, `Represent`, defaults, validators, codecs or marshal methods to guess a schema.

`WireSchema` supports explicit JSON scalar types, objects, arrays, null and deliberate `Type: "any"`. Objects declare properties and required names. A nil `AdditionalProperties` closes an object; an explicit additional schema describes dictionary values. Arrays require an item schema. Unknown outputs do not silently become `any`.

Custom/unsupported output requires a declaration or generation fails. Overrides of structurally derivable output are rejected rather than weakening or contradicting the built-in contract. Optional declarations are copied and validated by `api.New`; changing the caller's original maps, slices or pointers later does not alter the registered declaration.

Generation has depth, node, string and document-size limits, observes cancellation, rejects recursive/malformed metadata and returns no partial document on failure. Returned bytes belong to the caller. No references are fetched, no executable schema hooks run, and no private sample values are collected.

This is the serializer-output foundation. Input/write schemas, route and authentication metadata, complete OpenAPI generation, management commands, typed client generation and browsable documentation remain separate work. It is not a Django/DRF compatibility or release claim.
