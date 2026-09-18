# Forms and widgets

A form binds submitted data to an explicit field definition, cleans values, collects errors and renders controls. A widget controls HTML; it does not replace server-side validation.

Every kind has a focused sidebar page with a declaration and example, such as [Char](field-forms-char.md), [TypedChoice](field-forms-typedchoice.md) and [SplitDateTime](field-forms-splitdatetime.md). Use [Field options](options-core-forms-field.md) for every configurable member, [ModelForm options](options-core-forms-modelformoptions.md) for persistence, [FormSet options](options-core-forms-formsetoptions.md) for collections, and [InputWidget](options-core-forms-inputwidget.md) or [MultiWidget](options-core-forms-multiwidget.md) for rendering.

## Bind and validate

{{code docs/examples/forms_test.go}}

`forms.NewField` is required by default. A raw `forms.Field{}` is not. Set `Required = false` intentionally for optional fields. An unbound form is for display; `IsValid()` requires bound input.

Use `WithContext`, `WithData`, `WithFiles`, `WithInitial`, `WithPrefix` and `WithClean` when constructing a form. Read `Errors`, `CleanedData`, `ChangedData` and `HasChanged` after validation. Do not persist raw request values when you meant to persist cleaned values.

## All 29 form kinds

Create each with `forms.NewField(name, forms.Kind)`:

| Kind | Input and behavior |
| --- | --- |
| `Char` | Text; trims by default, with explicit `Strip` override |
| `Boolean` | Checkbox/boolean semantics; required true means the value must be true |
| `NullBoolean` | True, false or unknown |
| `ChoiceKind` | One allowed choice |
| `TypedChoice` | One allowed choice plus explicit coercion |
| `MultipleChoice` | Multiple allowed values |
| `TypedMultipleChoice` | Multiple allowed values with coercion |
| `Integer` | Signed integer parsing |
| `Float` | Finite floating-point parsing |
| `Decimal` | Decimal string with digit/scale limits |
| `Date` | Calendar date; default `2006-01-02` layout |
| `DateTime` | Instant or timezone-aware wall-time parsing |
| `Time` | Time-of-day parsing |
| `Duration` | Go duration input such as `1h30m` |
| `Email` | Bare email-address validation |
| `URL` | Allowed URL-scheme/host validation |
| `UUID` | Canonical UUID validation |
| `Slug` | ASCII or explicitly enabled Unicode slug |
| `IP` | IP parsing and normalization |
| `Regex` | Match an explicitly configured pattern |
| `JSON` | Bounded JSON with exact numeric decoding |
| `File` | Bounded uploaded file validation |
| `Image` | Uploaded image validation; not implicit storage |
| `FilePath` | Path-like text; not arbitrary filesystem access |
| `ModelChoice` | One ID resolved by an authorized callback |
| `ModelMultipleChoice` | Multiple IDs resolved within current scope |
| `MultiValue` | Clean component fields, then optionally compress |
| `Combo` | Apply component cleaners in sequence to one value |
| `SplitDateTime` | Separate date/time input combined using locale/timezone policy |

Field options include initial values, labels/help text, length/value/decimal bounds, choices, validators, coercers, regex patterns, input formats, error messages, component fields, compression, file limits and widgets. The [Field declaration](api-core-forms.md#field) gives their exact Go types.

## Field examples

This is `apps/fieldlab/forms.go` from the example application. `FormCases()` defines every form kind with submitted values and its required options, including decimal precision, choices/coercion, bounded uploads, relation resolution and composite fields. Run `go test ./apps/fieldlab` from `examples/showcase` to execute its validation tests.

<details>
<summary>All 29 form kinds: definitions and inputs</summary>

{{code examples/showcase/apps/fieldlab/forms.go}}

</details>

The fixed public relation choices are for demonstration only. Replace that resolver with an authorized lookup in your application's current scope; field validation does not grant access to an arbitrary record ID.

## Widgets

Configure a field and widget together. Inside your form factory:

```go
name := forms.NewField("name", forms.Char)
name.Label = "Product name"
name.MaxLength = 120
name.Widget = forms.InputWidget{Type: "text"}

price := forms.NewField("price", forms.Decimal)
price.MaxDigits, price.DecimalPlaces = 12, 2
price.Widget = forms.InputWidget{Type: "number"}
```

Pass these fields into the form definition as in the binding example above. Browser control types do not replace server-side bounds; invalid input still needs a field error. Use the showcase's [forms page](http://localhost:8000/forms/) after [starting it](showcase.md) to submit valid and invalid values and inspect the controls.

`forms.InputWidget{Type: ...}` supports:

| Family | Type strings |
| --- | --- |
| Text and scalar | `text`, `number`, `email`, `url`, `password`, `textarea` |
| Hidden values | `hidden`, `multiple-hidden` |
| Date/time | `date`, `datetime-local`, `time` |
| Choices | `checkbox`, `select`, `select-multiple`, `radio`, `checkbox-multiple` |
| Upload | `file` |

`MultiWidget` composes child widgets. The showcase covers 21 configurations: the 17 input types above, null-boolean select, multi-value, split datetime and hidden split datetime.

<details>
<summary>All 21 widget configurations</summary>

This complete example selects each widget, supplies a matching field/value and renders the gallery. It is `apps/fieldlab/widgets.go` in the same tested example application:

{{code examples/showcase/apps/fieldlab/widgets.go}}

</details>

Render a form with `Render("div")`, `"p"`, `"ul"` or `"table"`. Rendering escapes untrusted values, associates labels/errors and does not echo submitted passwords. Unsupported widget types fail explicitly. `ClearableFileInput` and `SelectDateWidget` are not implemented in this release.

## Model forms

`NewModelForm` takes a bound model record and an explicit field allowlist. Use `Overrides` for types requiring custom form behavior. Supply an authorized relation resolver, constraint checker and `ModelPersistence` implementation for save operations. Parent and relation writes must share a real transaction.

Binary, search-vector, GIS and custom model kinds require explicit form overrides. An array/range/HStore form mapping to JSON is not proof of database codec or Admin support.

## Formsets

`BindFormSet` validates management counts before allocating rows. Set minimum/maximum counts, the complete server-scoped `ExistingIDs`, row identity and delete permission callbacks. The default absolute maximum is 1,000. Submitted IDs cannot expand the authorized set of existing rows.

Optional ordering and deletion are explicit. Inline Admin editing builds on these bounded form concepts. A successfully rendered formset is not proof that its eventual writes are authorized.
