# Field laboratory

This app demonstrates the released APIs, not every feature proposed for Gogo.
`Catalog()` drives the browser/JSON inventory. Tests execute each inventory row.

```text
Field laboratory
├── Models: 40 kinds
│   ├── 31 named field constructors
│   │   ├── Integer: small / ordinary / big; all three positive variants
│   │   ├── Identity: SmallAuto / Auto / BigAuto; UUID
│   │   ├── Numeric: Decimal / Float / Boolean
│   │   ├── Text: Char / Text / Slug / Email / URL / IP / FilePath
│   │   ├── Time: Date / DateTime / Time / Duration
│   │   ├── Data: Binary / JSON / File / Image
│   │   └── Relations: ForeignKey / OneToOne / ManyToMany
│   └── NewField descriptors
│       ├── Array: element validation + PostgreSQL type mapping
│       ├── Generated / Range / SearchVector: type mapping
│       ├── HStore / Geometry / Geography / Raster: extension-gated mapping
│       └── Custom: schema accepted; stock PostgreSQL mapping rejected
├── Forms: all 29 declared kinds
│   ├── Scalar, nullable Boolean, choice, typed choice, multiple choice
│   ├── Numeric, temporal, text formats, Regex, JSON, FilePath
│   ├── File / Image: bounded multipart bytes + actual image decode
│   ├── ModelChoice / ModelMultipleChoice: fixed public demo IDs, fail closed
│   └── MultiValue / Combo / SplitDateTime: component clean + compression
└── Widgets: 21 configurations
    ├── All 17 accepted InputWidget types
    ├── Nullable Boolean select
    ├── MultiWidget + split date/time
    └── Split hidden date/time composed from two hidden inputs
```

## Paths that execute

| Entry | Behavior | Writes |
| --- | --- | --- |
| `ModelCases()` | Constructs descriptors and valid/invalid samples | None |
| Model tests | Clean samples, validate schemas, check dialect/capability mapping | None |
| `Schemas()` | Four migratable models; frozen registry creates the M2M intermediary | None until migrations run |
| `NewSpecimen()` | Normalizes every scalar sample into an unsaved public `MapRecord` | None until caller saves |
| `Form()` | Unbound form with illustrative initial values | None |
| `Form(WithData(...), WithFiles(...))` | Bound cleaning, errors, cleaned values | None |
| `RenderWidgets()` | Escaped, unbound widget gallery | None |
| Formset test | Bounded management counts and row ordering; forged count rejected | None |

`Specimen` contains the scalar fields, including a BigAuto primary key.
`SmallIdentity` and `StandardIdentity` demonstrate the other auto sizes in
separate tables. `Related` declares all three ordinary relation kinds.
The sample's integration tests own the database persistence evidence.

## Deliberate boundaries

- Array/Generated/HStore/Range/SearchVector/GIS/Custom descriptors are **not** in
  the migrated sample tables. Type mapping is not a codec, CRUD round trip,
  extension installation, query implementation, or usable Admin widget.
- File/Image model values are illustrative storage-key strings, not uploaded
  objects. Form upload validation neither publishes nor deletes an object.
- FilePath is currently string cleaning, not filesystem enumeration or access
  control. Never use its cleaned value as an authorized arbitrary server path.
- Model-choice form examples use only the fixed public IDs `1` and `2`. Real
  application IDs require a request-scoped, authorized resolver.
- Binary needs a custom form override; the sample Admin uses read-only display.
- Clearable-file and select-date widgets are explicitly absent. The hidden
  date/time example is composition, not a dedicated Django-equivalent class.
- These tests do not assert complete Django or Celery compatibility.

From the sample module: `GOWORK=off go test ./apps/fieldlab/...`.
