# Models

A model is a Go struct plus a `Schema()` declaration. ORM, migrations, validation, forms and serializers reuse that schema; public APIs still need explicit field allowlists.

## Declare a model

**1. Define the Go struct.**

{{snippet docs/snippets/catalog/models.go model-struct}}

**2. Describe the stored fields.** Names such as `name` map to exported members such as `Name`.

{{snippet docs/snippets/catalog/models.go model-schema}}

<details>
<summary>Complete model file, including imports</summary>

{{code docs/snippets/catalog/models.go}}

</details>

Use the model's schema key, `catalog.Product`, when a service requests a model identifier. The default table name is lower-case app plus model, such as `catalog_product`. Set `Schema.Table` to override it.

## Integer IDs

Declare an ID member and its field explicitly; `models.Base` does not inject a primary key. The example above uses a 32-bit database sequence:

```go
models.AutoField("id", models.WithStructField("ID"))
```

For a 64-bit database sequence, use an `int64` Go member and choose:

```go
models.BigAutoField("id", models.WithStructField("ID"))
```

Both allocate `1`, then `2`, and so on on a fresh PostgreSQL table. Leave `ID` at zero on insert; the ORM reads the generated value back. Failed inserts and deletions can leave gaps. `AutoField` supports up to `2,147,483,647`; `BigAutoField` supports up to `9,223,372,036,854,775,807`. Changing an existing field needs an explicit migration, including dependent foreign keys.

Built-in User/Group models use integer IDs by default and support a shared [account ID configuration](auth.md#user-and-group-ids).

## Register and generate

```sh
go run manage.go generate
go run manage.go generate --check
```

The generator connects model declarations to the app registry and emits typed field references. A `models.Registry` must contain the schemas before an ORM store can use them. App registration and opening the database are separate steps.

## Model metadata

| Declaration | What it controls |
| --- | --- |
| `Fields`, `PrimaryKey` | Stored columns, generated identity and explicit/composite primary-key metadata |
| `Ordering` | Default ordering fields |
| `Indexes`, `Constraints` | Index and database-constraint declarations consumed by migrations |
| `Label`, `LabelPlural` | Human-facing model names |
| `Abstract`, `Proxy`, `Unmanaged` | Model/table ownership metadata; inspect the corresponding ORM and migration behavior before combining them |
| `Parent`, `ParentLink`, `Concrete` | Explicit inheritance metadata, not Python class introspection |
| `RequiredCapabilities` | Capabilities the selected backend must satisfy |

All exported metadata fields are listed in [models.Schema](api-core-models.md#schema). Backend capability checks can reject otherwise valid declarations; metadata presence is not universal backend support.

## Validate deliberately

`models.Bind` exposes a record view over your model. Field cleaning normalizes and validates values. Model/constraint validation can need database-aware checks.

{{snippet docs/examples/options_test.go model-text-options}}

{{snippet docs/examples/options_test.go model-clean}}

> `Store.Save` does **not** automatically run `FullClean`. If your workflow requires validation, call it explicitly or supply a `SaveOptions.Prepare` callback. Forms and API write services have their own validation paths; do not assume a direct ORM save goes through them.

`Blank` describes empty-input validation; `Null` describes null values. They are not interchangeable. Defaults and automatic timestamps have their own save behavior. `Editable: false` controls model-derived editing; it does not make a value confidential or authorize an operation.

{{snippet docs/examples/options_test.go model-null-blank}}

## Learn the field vocabulary

Use [Model field reference](model-fields.md) for every declared kind, common options and alpha boundaries. Use [Relationships](relations.md) for target schemas, reverse names, intermediary tables and delete policies. Read [Migrations](migrations.md) whenever a schema changes.
