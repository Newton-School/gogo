# Models

A model is a Go struct with persistence state and an explicit `Schema()` method. That schema is shared by the ORM, migrations, validation and model-derived forms/serializers. A schema is not permission to expose every field publicly.

## Declare a model

The tutorial's complete model is deliberately small:

{{code docs/snippets/catalog/models.go}}

Use the model's schema key, `catalog.Product`, when a service requests a model identifier. The default table name is lower-case app plus model, such as `catalog_product`. Set `Schema.Table` to override it.

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

> `Store.Save` does **not** automatically run `FullClean`. If your workflow requires validation, call it explicitly or supply a `SaveOptions.Prepare` callback. Forms and API write services have their own validation paths; do not assume a direct ORM save goes through them.

`Blank` describes empty-input validation; `Null` describes null values. They are not interchangeable. Defaults and automatic timestamps have their own save behavior. `Editable: false` controls model-derived editing; it does not make a value confidential or authorize an operation.

## Learn the field vocabulary

Use [Model field reference](model-fields.md) for every declared kind, common options and alpha boundaries. Use [Relationships](relations.md) for target schemas, reverse names, intermediary tables and delete policies. Read [Migrations](migrations.md) whenever a schema changes.
