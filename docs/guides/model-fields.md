# Model field reference

Gogo declares 40 model kinds. There are 31 named constructors and nine additional kinds created through `models.NewField`. This list describes the vocabulary; the support column prevents a descriptor from being mistaken for a complete CRUD implementation.

## Start with a complete model

Each field belongs in the `Fields` slice returned by your model's `Schema` method. Match the Go field explicitly with `WithStructField`; import types such as `time.Time` in the app when used.

{{code docs/snippets/catalog/models.go}}

After changing a schema, run `go run manage.go generate`, then `go run manage.go makemigrations catalog`, inspect the migration, and apply it with `go run manage.go migrate`. Schema declarations do not change a database merely by compiling.

## Combine options

These expressions go inside the model's `Fields` slice; the corresponding Go fields must also exist:

```go
models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(120)),
models.SlugField("slug", models.WithStructField("Slug"), models.UniqueValue),
models.DecimalField("price", 12, 2, models.WithStructField("Price")),
models.BooleanField("published", models.WithStructField("Published"), models.WithDefault(false)),
models.TextField("summary", models.WithStructField("Summary"), models.Optional),
models.EmailField("contact", models.WithStructField("Contact"), models.Nullable, models.Optional),
```

Use a nullable Go representation such as `*string` for nullable `Contact`. `Optional` permits blank values; `Nullable` permits database null. Neither supplies an application authorization policy. For uniqueness under concurrency, keep the database constraint, not only a prior existence check.

For all field families in a running UI, [start the showcase](showcase.md) and visit [the field inventory](http://localhost:8000/fields/). Its [field definitions](../../examples/showcase/apps/fieldlab/models.go) distinguish persisted scalars from descriptor-only extension/GIS kinds. Do not enable advanced descriptors just to make a dropdown appear.

## Numeric and identity fields

| Constructor | Typical Go value | Meaning |
| --- | --- | --- |
| `SmallIntegerField` | `int64` | Small signed integer; backend range applies |
| `IntegerField` | `int64` | Standard signed integer |
| `BigIntegerField` | `int64` | Large signed integer |
| `PositiveSmallIntegerField` | `int64` | Nonnegative small integer |
| `PositiveIntegerField` | `int64` | Nonnegative standard integer |
| `PositiveBigIntegerField` | `int64` | Nonnegative large integer |
| `SmallAutoField` | `int64` | Small database-generated primary key |
| `AutoField` | `int64` | Standard database-generated primary key |
| `BigAutoField` | `int64` | Large database-generated primary key |
| `UUIDField` | `string` | Validated UUID; add an explicit default generator if wanted |
| `DecimalField(name, digits, places)` | `string` | Exact decimal with declared precision, such as `"19.95"` |
| `FloatField` | `float64` | Finite floating-point number; not exact currency |
| `BooleanField` | `bool` | Boolean value |

## Text fields

| Constructor | Meaning | Useful options |
| --- | --- | --- |
| `CharField` | Bounded text | `WithMaxLength`, `WithMinLength` |
| `TextField` | Longer text | Validation length bounds if required |
| `SlugField` | URL-friendly label | Defaults to max length 50 and a DB index; `WithAllowUnicode` widens accepted characters |
| `EmailField` | Email-address validation | Length and custom validators |
| `URLField` | URL validation | Length and custom validators |
| `GenericIPAddressField` | IP address | Backend mapping and validation |
| `FilePathField` | A path-like value | Not authorization to read arbitrary server files |

These are string-backed in ordinary model declarations. Slug validation does not automatically generate a slug, normalize Unicode or guarantee uniqueness. Admin prepopulation is a separate feature.

## Time and data fields

| Constructor | Typical value | Important behavior |
| --- | --- | --- |
| `DateField` | `time.Time` | Calendar date; distinguish from an instant |
| `DateTimeField` | `time.Time` | Timestamp; timezone presentation is separate |
| `TimeField` | `time.Time` | Time-of-day representation |
| `DurationField` | `time.Duration` | Duration |
| `BinaryField` | `[]byte` | Binary data; use an explicit API representation |
| `JSONField` | JSON-compatible values | Preserve number precision; nested assignment is validated |
| `FileField` | `string` storage key | Does not save or authorize a file by itself |
| `ImageField` | `string` storage key | Does not establish that bytes are a valid, available image |

`AutoNow` and `AutoNowAdd` are explicit field flags. Review their save behavior with selected update fields, raw saves and bulk operations; they are not database triggers.

## Relationships

| Constructor | Meaning |
| --- | --- |
| `ForeignKeyField` | Many source rows reference a target record |
| `OneToOneField` | Unique relation to a target record |
| `ManyToManyField` | Join/intermediary relation, not a scalar column on the source |

Supply a `models.Relation` with a registered `Target`, delete policy and optional reverse/through metadata. See [Relationships](relations.md).

## Advanced kinds and explicit limits

Use `models.NewField(name, models.Kind, options...)` for these kinds:

| Kind constant | Intended metadata | Alpha boundary |
| --- | --- | --- |
| `Generated` | Generated expression, noneditable field | Expression/type support must be checked for the backend and operation |
| `Array` | Element field in `Field.Element` | Not proof of complete array codecs or form/Admin editing |
| `HStore` | PostgreSQL key/value type | Extension/type mapping is not end-to-end support |
| `Range` | Range subtype in `Field.RangeType` | Not a general range-field CRUD implementation |
| `SearchVector` | Search-vector storage vocabulary | Not a full-text search product by itself |
| `Geometry` | Spatial geometry vocabulary | Full GIS queries, codecs and widgets are not implemented |
| `Geography` | Spatial geography vocabulary | Full GIS is not implemented |
| `Raster` | Spatial raster vocabulary | Full raster operations are not implemented |
| `Custom` | Application codec/capability metadata | You own the codec, backend support, validation and presentation contracts |

These are intentionally marked descriptor/capability-level in the showcase. Do not infer database round trips, migrations, serializers or Admin controls merely from successful schema construction.

## Common options

| Option | Effect |
| --- | --- |
| `WithStructField`, `WithColumn` | Map Go field and database column names independently |
| `Nullable`, `Optional` | Allow null and blank values, respectively |
| `ReadOnly`, `Primary`, `UniqueValue`, `WithDBIndex` | Editing, primary-key, uniqueness and index metadata |
| `WithMaxLength`, `WithMinLength`, `WithPrecision`, `WithBounds` | Length, decimal and value bounds |
| `WithDefault`, `WithDefaultFunc` | Explicit static or callable default; callable defaults have a stable ID |
| `WithChoices`, `WithValidators` | Choice values and context-aware validation |
| `WithLabel`, `WithHelpText` | Human-facing labels and help |
| `WithAllowUnicode` | Widen slug characters without implicit transformation |
| `WithRelation` | Attach a relation declaration |

Options are functions, so a client may set other exported `Field` metadata explicitly. The [complete Field declaration](api-core-models.md#field) covers DB defaults, generated expressions, collations, codecs, date-scoped uniqueness and remaining metadata. An exported option still needs compatible consumer/backend behavior.
