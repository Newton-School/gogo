# Models Reference

Model metadata is the source of truth for fields, validation, migrations, content types, admin, permissions, and ORM query compilation.

## Public Packages

| Package | Purpose |
| --- | --- |
| `models` | Model metadata, registry, lifecycle state, field metadata, indexes, constraints, inheritance metadata, permissions, and validation hooks. |
| `models/fields` | Django-style model fields and field-level conversion/validation. |
| `models/constraints` | Index and constraint metadata helpers. |
| `contrib/postgres/vector` | PostgreSQL pgvector field metadata and HNSW/IVFFlat index helpers. |
| `models/hooks` | Model lifecycle hooks. |
| `models/validation` | Structured model validation errors. |

## Core Types

| Type | Purpose |
| --- | --- |
| `models.Model` | Interface exposing `ModelMeta() models.Metadata`. |
| `models.BaseModel` | Embeddable ID/timestamp/state base. |
| `models.Metadata` | App label, model name, table name, options, fields, indexes, constraints, permissions, managers, inheritance, and migration flags. |
| `models.FieldMeta` | Field-level metadata used by models, admin, serializers, migrations, and ORM. |
| `models.Registry` | Model registry for metadata, content types, and migration metadata. |
| `models.CompositePrimaryKey` | Composite primary key metadata. |
| `models.Permission` | Custom model permission metadata. |

## Field Types

`models/fields` exposes all primary Django-style field categories:

| Category | Constructors |
| --- | --- |
| Integer | `NewAutoField`, `NewBigAutoField`, `NewSmallAutoField`, `NewIntegerField`, `NewBigIntegerField`, `NewSmallIntegerField`, `NewPositiveIntegerField`, `NewPositiveBigIntegerField`, `NewPositiveSmallIntegerField` |
| Decimal and float | `NewDecimalField`, `NewFloatField` |
| Text and boolean | `NewBooleanField`, `NewCharField`, `NewTextField`, `NewEmailField`, `NewURLField`, `NewSlugField`, `NewUUIDField` |
| Temporal | `NewDateField`, `NewDateTimeField`, `NewTimeField`, `NewDurationField` |
| Binary and JSON | `NewBinaryField`, `NewJSONField` |
| Files | `NewFileField`, `NewImageField`, `NewFilePathField` |
| Network | `NewGenericIPAddressField` |
| Generated and custom | `NewGeneratedField`, `NewCustomField` |
| Relations | `NewForeignKey`, `NewOneToOneField`, `NewManyToManyField` |
| PostgreSQL | `NewArrayField`, `NewHStoreField`, `NewIntegerRangeField`, `NewBigIntegerRangeField`, `NewDecimalRangeField`, `NewDateRangeField`, `NewDateTimeRangeField` |
| GIS | `NewGeometryField`, `NewPointField`, `NewLineStringField`, `NewPolygonField`, `NewMultiPointField`, `NewMultiLineStringField`, `NewMultiPolygonField`, `NewGeometryCollectionField`, `NewRasterField` |

## Field Options

`fields.Options` covers names, columns, verbose names, help text, primary key, unique, null, blank, default, choices, validators, db index, editable, serialization, db comments, and form/admin metadata.

`fields.Choices` and `fields.NewChoices` define fixed value sets.

`fields.NewCustomField` is the public escape hatch for database-specific field
types. Provide a stable framework kind, dialect `ColumnTypes`, and optional
conversion/validation hooks. `fields.Metadata(field, dialect)` converts a field
object into `models.FieldMeta` for generated projects and migration metadata.

## Relations

`fields.RelationConfig` stores target model, through model, related name, related query name, `OnDelete`, reverse relation metadata, and self-reference behavior.

Supported delete behaviors include cascade, protect, restrict, set null, set default, set value, do nothing, and no constraint where implemented by the relation metadata.

Relation columns default to bigint-compatible storage. Use
`RelationConfig.ColumnTypes` or explicit relation `FieldMeta.Kind` for UUID,
text, or other target primary-key types. When a relation `FieldMeta.Kind` is
omitted, migration state infers the kind from the registered target model's
primary key.

## Database Defaults, Indexes, And Constraints

Use `models.DefaultValue(value)` for a quoted literal database default and
`models.DefaultSQL("trusted_expression()")` for a static SQL default
expression. Defaults are preserved in migration state and rendered by generated
schema SQL.

Database-generated primary keys should declare `PrimaryKey: true` and a
database default. ORM, API, and admin create paths omit those primary keys from
INSERT statements and read the generated value back with `RETURNING` on
supported dialects.

`models.Index`, `models.IndexField`, `models.Constraint`, and `models.Permission` appear on `models.Metadata`.

Field metadata with `Unique` or `DBIndex` expands into deterministic database
constraint and index state during migration generation.

`models/constraints` adds validated metadata for:

- Index fields, ordering, opclasses, conditions, include columns, tablespaces, and expressions.
- Unique, check, exclusion, deferrable, null distinct, and covering constraint metadata.

`contrib/postgres/vector` provides `FieldMeta`, `NewField`,
`HNSWIndex`, and `IVFFlatIndex` helpers for pgvector-backed models. It only
describes metadata and SQL shape; projects still own installing and migrating
the PostgreSQL extension in their environment.

## Validation

`models.ValidateMetadata` rejects invalid migration-facing metadata, including
duplicate field names or columns, declared fields without a primary key,
duplicate generated index names, duplicate generated constraint names, and
duplicate custom permission codenames. `models.Registry.ValidateRelations`
validates relation targets after all model metadata is registered.

Field validation returns `fields.ErrValidation` or `fields.ErrInvalidField`.

Model validation uses:

- Field validators through field options.
- `models/validation.Error` for one field/object error.
- `models/validation.Errors` for grouped errors.
- Model-level uniqueness hooks where provided by forms or model stores.

## Error Types

| Error | Package |
| --- | --- |
| `fields.ErrValidation` | `models/fields` |
| `fields.ErrInvalidField` | `models/fields` |
| `constraints.ErrInvalidIndex` | `models/constraints` |
| `constraints.ErrInvalidConstraint` | `models/constraints` |

## Example

```go
meta := models.Metadata{
	AppLabel:  "blog",
	ModelName: "Post",
	TableName: "blog_post",
	Fields: []models.FieldMeta{
		{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true, DBDefault: models.DefaultSQL("gen_random_uuid()")},
		{Name: "author", Column: "author_id", Kind: "uuid", RelationTarget: "auth.User"},
		{Name: "title", Column: "title", Kind: "text"},
	},
}
label := meta.Label()
_ = label
```
