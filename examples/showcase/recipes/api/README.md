# API serializer recipes

Run from the sample module: `GOWORK=off go test ./recipes/api`.

Every public field constructor is exercised with real binding/representation:

| Group | Constructors | Evidence |
| --- | --- | --- |
| Scalar | Scalar, StringField, IntegerField, BooleanField, DecimalField, JSONField, FloatField | Valid/invalid values; exact integers/decimals; JSON-safe output |
| Formats | UUIDField, URLField, EmailField, SlugField, IPAddressField, DateField, DateTimeField, TimeField, DurationField | Format rejection and serialized values |
| Choices | ChoiceField | Only declared choices accepted |
| Structured | ListField, DictField, NestedField | Bounded collections; indexed/nested errors; unknown nested fields denied |
| Output | ComputedField | Read-only input; computed output; explicit custom output metadata |
| Uploads | FileField, ImageField | Bounded multipart parsing; image decode; traversal/size denial; write-only output |

Additional tests cover read-only/write-only/hidden fields, trusted defaults,
source aliases, omission versus explicit null, partial updates, unknown-field
denial, model-derived explicit projections, fixed-public relation policies,
custom validators/representations, and an explicit save-policy boundary.

No recipe here proves database persistence or transaction durability. The
save-policy test uses an in-memory boundary probe. Upload validation does not
publish an object. OutputSchema describes output metadata, not runtime input
validation. Relation ID `1` is a fixed public example; applications must supply
current actor-scoped resolvers for actual records.
