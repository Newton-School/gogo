# Empty-value display

`Config`, `ModelAdmin`, and `DisplayColumn` accept `EmptyValueDisplay *string`.
Nil inherits from the next level; a pointer to `""` explicitly renders no text.
Construction/registration snapshots the pointed-to text. Later caller edits do
not change a registered site.

| Value / surface | Result |
| --- | --- |
| Empty display column | Column override → model override → site override → existing `—` default. |
| Empty readonly model field | Same precedence, using the already loaded readonly value; a matching display descriptor supplies only its text override, not its value callback. |
| Readonly inline field | Site override/default through the shared readonly renderer; inlines do not inherit unrelated registered ModelAdmin configuration. |
| Nil (including typed nil), empty string, zero-length array/slice/map | Render the selected placeholder as escaped text. |
| Zero, false, whitespace, populated collection, other nonempty value | Preserve the existing display value. Collection contents are not traversed. |
| Editable widget | Unchanged; placeholders do not become initial values, cleaned data, or submitted values. |

Emptiness checks inspect Go value shape without calling application methods or
relation resolvers. Readonly relations still require their scoped snapshots;
denied and genuinely empty visible selections receive the same placeholder. A
missing required snapshot remains an error, not a reason to query unscoped data.

All placeholder configuration is plain text, including HTML-like text. It never
becomes trusted markup or an attribute, and it cannot change field labels or
read-only controls. No general field formatting, Boolean icon support, additional
readonly callables, or new relation visibility behavior is introduced here.

Source-render tests cover site/model/column and explicit-empty precedence,
registration snapshots, nil/collection versus zero/false handling, flat and
fieldset readonly controls, escaping, and denied-relation no-read/no-resolver
behavior. The empty string/collection default regression failed before this
implementation. Live browser verification remains pending.
