"""Focused, source-checked model/form field pages and executable examples."""

import json
import re
import subprocess

GROUPS = {
    "models": {
        "Identity": "SmallAuto Auto BigAuto UUID",
        "Numbers": "SmallInteger Integer BigInteger PositiveSmallInteger PositiveInteger PositiveBigInteger Decimal Float Boolean",
        "Text": "Char Text Slug Email URL GenericIPAddress FilePath",
        "Time": "Date DateTime Time Duration",
        "Data": "Binary JSON File Image",
        "Relations": "ForeignKey OneToOne ManyToMany",
        "Advanced": "Generated Array HStore Range SearchVector Geometry Geography Raster Custom",
    },
    "forms": {
        "Text": "Char Email URL UUID Slug IP Regex FilePath",
        "Numbers": "Integer Float Decimal Boolean NullBoolean",
        "Choices": "ChoiceKind TypedChoice MultipleChoice TypedMultipleChoice ModelChoice ModelMultipleChoice",
        "Time": "Date DateTime Time Duration",
        "Data": "JSON File Image",
        "Composite": "MultiValue Combo SplitDateTime",
    },
    "serializers": {
        "Text": "StringField UUIDField URLField EmailField SlugField IPAddressField",
        "Numbers": "IntegerField BooleanField DecimalField FloatField",
        "Time": "DateField DateTimeField TimeField DurationField",
        "Structured": "JSONField ListField DictField NestedField",
        "Other": "ComputedField ChoiceField FileField ImageField Scalar",
    },
}


def imports_for(code):
    imports = {
        "context": "context", "fmt": "fmt", "json": "encoding/json", "time": "time",
        "url": "net/url", "strconv": "strconv", "regexp": "regexp",
        "models": "github.com/Newton-School/gogo/core/models",
        "forms": "github.com/Newton-School/gogo/core/forms",
        "api": "github.com/Newton-School/gogo/core/api",
    }
    return [path for name, path in imports.items() if re.search(r"\b" + name + r"\.", code)]


def example_body(family, row):
    if family == "models":
        _, _, expression, _, good, bad = row
    else:
        _, expression, _, value = row
    lines = ["field := " + expression]
    if family == "models" and good is not None:
        lines += ["_, err := field.Clean(context.Background(), " + good + ")",
                  'if err != nil { panic("valid example failed") }']
        if bad is not None:
            lines += ["_, err = field.Clean(context.Background(), " + bad + ")",
                      'if err == nil { panic("invalid example was accepted") }']
        lines += ['fmt.Println("valid input accepted; invalid input rejected")']
    elif family == "forms" and value is not None:
        lines += ["form, err := forms.New([]forms.Field{field},",
                  "    forms.WithContext(context.Background()),",
                  "    forms.WithData(url.Values{\"value\": {" + json.dumps(value) + "}}),",
                  ")", "if err != nil { panic(err) }",
                  'if !form.IsValid() { panic("valid form example failed") }',
                  "fmt.Println(form.IsValid()) // true"]
    elif family == "serializers" and value is not None:
        lines += ["serializer, err := api.New(api.Definition{Fields: []api.Field{field}})",
                  "if err != nil { panic(err) }",
                  "cleaned, err := serializer.Validate(context.Background(), api.Values{\"value\": " + value + "}, api.BindOptions{})",
                  "if err != nil { panic(err) }",
                  "_, err = serializer.Representation(context.Background(), cleaned)",
                  "if err != nil { panic(err) }", 'fmt.Println("input validated; output represented")']
    else:
        lines += ["_ = field // Declaration only; wire the owning service before use."]
    return "\n".join(lines)


def complete_example(family, row):
    body = example_body(family, row)
    imports = "\n".join('    "' + path + '"' for path in imports_for(body))
    source = "package main\n\nimport (\n" + imports + "\n)\n\nfunc main() {\n" + "\n".join("    " + line for line in body.splitlines()) + "\n}"
    return subprocess.check_output(["gofmt"], input=source, text=True, timeout=10).rstrip()


def build_pages(data, catalog):
    pages = []
    for family, rows in data.items():
        directory = "core/api" if family == "serializers" else "core/" + family
        package = next(p for p in catalog["Packages"] if p["Directory"] == directory)
        declarations = "\n".join(d["Signature"] for d in package["Declarations"] if d["Kind"] == "constant")
        kinds = set(re.findall(r"\b([A-Z][A-Za-z]+)\s+Kind\s*=", declarations))
        if family == "serializers":
            kinds = {d["Name"] for d in package["Declarations"] if d["Kind"] == "function" and re.search(r"\) Field$", d["Signature"])}
        documented = [row[0] for row in rows]
        if len(set(documented)) != len(rows) or set(documented) != kinds:
            raise ValueError(f"Field kind coverage for {family}: source {sorted(kinds)}, docs {sorted(documented)}")
        grouped = [kind for names in GROUPS[family].values() for kind in names.split()]
        if len(grouped) != len(set(grouped)) or set(grouped) != kinds:
            raise ValueError("Field navigation coverage: " + family)
        parent = {"models": "model-fields", "forms": "forms", "serializers": "api"}[family]
        for row in rows:
            kind = row[0]
            title, behavior = (row[1], row[3]) if family == "models" else (kind, row[2])
            identifier = "field-" + family + "-" + kind.lower()
            body = ["# " + title, "", behavior, "", "## Example", "",
                    "This standalone example compiles against the checkout. Validation examples run in the documentation tests; descriptor-only declarations do not claim persistence or UI support.", "",
                    "```go", complete_example(family, row), "```", "", "## Configuration", ""]
            if family == "models":
                body += ["Use named constructors or `models.NewField`; options run in order. Constructors set `Editable=true` except identities and generated fields. Add `WithStructField` when mapping to a Go struct, then include the declaration in `Schema().Fields`.", "",
                         "| Configure | Options |", "| --- | --- |",
                         "| Missing/empty input | `Nullable`, `Optional`; they are independent |",
                         "| Application default | `WithDefault`, `WithDefaultFunc` with stable `DefaultID` |",
                         "| Storage and identity | `WithColumn`, `Primary`, `UniqueValue`, `WithDBIndex` |",
                         "| Validation | `WithBounds`, `WithMinLength`, `WithMaxLength`, `WithChoices`, `WithValidators`; applicability depends on this kind |",
                         "| Presentation | `ReadOnly`, `WithLabel`, `WithHelpText`; not authorization |", "",
                         "[Every Field member and its behavior](options-core-models-field.md) · [Relation configuration](options-core-models-relation.md)", "",
                         "## Validation and persistence", "",
                         "`Field.Clean` validates one value. `Schema.Validate` checks the declaration. `FullClean` adds model/uniqueness/constraint checks through explicit providers. None of these saves a row. Register the schema, review migrations and use the ORM for persistence. File storage and relation authorization remain separate.", "",
                         "[Models](models.md) · [Migrations](migrations.md) · [Forms](forms.md) · [Serializers](api.md)"]
            elif family == "forms":
                body += ["`NewField` sets `Required=true`; a raw `forms.Field` literal does not. Configure the field before `forms.New`. Without `WithData` the form is unbound; supplying even empty data binds it.", "",
                         "| Configure | Options |", "| --- | --- |",
                         "| Presence | `Required`, `Disabled`, `Initial` |",
                         "| Constraints | `MinLength`, `MaxLength`, `MinValue`, `MaxValue`, `MaxDigits`, `DecimalPlaces`; apply only to compatible kinds |",
                         "| Custom cleaning | `Validators`, `Clean`, `ErrorMessages` |",
                         "| Presentation | `Label`, `HelpText`, `Widget` |",
                         "| Kind-specific behavior | `Choices`, `Coerce`, `Resolve`, `Pattern`, `InputFormats`, `Fields`, `Compress`, `MaxBytes`, `Strip`, `AllowUnicode` |", "",
                         "[Every Field member and its behavior](options-core-forms-field.md) · [Widgets](options-core-forms-inputwidget.md)", "",
                         "## Errors and saving", "",
                         "Check `IsValid()` before reading `CleanedData()`. Field errors are available through `Errors()`; provider failures must not be disguised as ordinary user mistakes. A plain form does not save data. Use an explicitly authorized service, or configure ModelForm persistence for atomic model/relation saves.", "",
                         "[Complete multipart, relation and composite examples](forms.md#field-examples) · [Model forms](options-core-forms-modelformoptions.md) · [Formsets](options-core-forms-formsetoptions.md)"]
            else:
                body += ["Set the returned `api.Field` before `api.New`. Scalar constructors derive requiredness and nullability from their model metadata; lists/nested objects are required by default, and uploads are write-only. Computed fields are read-only.", "",
                         "| Configure | Options |", "| --- | --- |",
                         "| Wire name and source | `Name`, `Source`, `Label`, `HelpText` |",
                         "| Input presence | `Required`, `AllowNull`, `Default`; partial input skips absent fields and defaults |",
                         "| Direction | `ReadOnly`, `WriteOnly`, `Hidden`; hidden fields require a trusted default |",
                         "| Collections and nested objects | `Element`, `Nested`, `Dictionary`, `MinItems`, `MaxItems` |",
                         "| Custom behavior | `Validate`, `Represent`, `Compute`, `OutputSchema` |", "",
                         "[Every api.Field option](options-core-api-field.md) · [Definition](options-core-api-definition.md) · [Partial input](options-core-api-bindoptions.md)", "",
                         "## Validation and persistence", "",
                         "`Validate` returns only declared writable sources; `Representation` omits write-only/hidden fields. Declared read-only input is ignored, not accepted as an update. Unknown input is rejected unless Definition.AllowUnknown is enabled. Neither operation saves a record. Configure an explicit authorized persistence/mutation policy.", "",
                         "[Mutation routes and policies](api-writes.md) · [OpenAPI](detail-core-api-openapi.md) · [Output schemas](detail-core-api-output-schema.md)"]
            pages.append({"id": identifier, "title": title, "parent": parent, "new": True,
                          "source": "\n".join(body), "description": behavior, "fieldKind": kind,
                          "navGroup": next(group for group, names in GROUPS[family].items() if kind in names.split())})
    return pages
