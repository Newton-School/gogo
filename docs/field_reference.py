"""Focused, source-checked model/form field pages and executable examples."""

import json
import re
import subprocess
from code_examples import block

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


def format_fragment(source):
    wrapped = "package main\nfunc main() {\n" + source + "\n}\n"
    formatted = subprocess.check_output(["gofmt"], input=wrapped, text=True, timeout=10)
    return "\n".join(line.removeprefix("\t") for line in formatted.splitlines()[3:-1])


def constructor_examples(data):
    """Reuse the executable field examples in constructor references, not just signatures."""
    examples = {}
    for family in ("models", "serializers"):
        for row in data[family]:
            expression = row[2] if family == "models" else row[1]
            match = re.match(r"(models|api)\.([A-Za-z]+)\(", expression)
            if not match or match[2] == "NewField":
                continue
            key = "core/" + match[1] + "." + match[2]
            field_id = "field-" + family + "-" + row[0].lower()
            examples[key] = block(format_fragment("field := " + expression)) + "\n\n"
            examples[key] += f"[Validation example and configuration]({field_id}.md). This declaration alone does not save data."
    return examples


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
            expression = row[2] if family == "models" else row[1]
            declaration = "field := " + expression
            validation = example_body(family, row)[len(declaration):].strip()
            notes = re.split(r"(?<=[.!?])\s+", behavior)
            body = ["# " + title, "", notes[0], "", "## Example", "",
                    "**1. Declare the field**", "", block(format_fragment(declaration)), ""]
            if not validation.startswith("_ = field"):
                body += ["**2. Validate the value**", "", block(format_fragment(validation)), ""]
            else:
                body += ["> Declaration only. Wire the required provider before validation or persistence.", ""]
            if len(notes) > 1:
                body += ["\n".join("- " + note for note in notes[1:]), ""]
            body += ["<details>", "<summary>Complete runnable example, including imports</summary>", "",
                     block(complete_example(family, row)), "", "</details>", "", "## Configuration", ""]
            if family == "models":
                body += ["Map the field to a Go struct member, then add it to `Schema().Fields`:", "",
                         block('field.StructField = "Value"\n// Your model must declare a compatible exported Value member.'), "",
                         "[Every Field member and its behavior](options-core-models-field.md) · [Relation configuration](options-core-models-relation.md)", "",
                         "## Validation and persistence", "",
                         "- `Field.Clean` validates a value; it does **not** save a row.\n- `FullClean` adds model and configured constraint checks.\n- Save through the ORM after authorization; file storage and relation checks are separate.", "",
                         "[Models](models.md) · [Migrations](migrations.md) · [Forms](forms.md) · [Serializers](api.md)"]
            elif family == "forms":
                body += ["Set options before calling `forms.New`:", "",
                         block('field.Required = false // NewField defaults to true.\nfield.Label = "Value"\nfield.HelpText = "Enter a value."'), "",
                         "[Every Field member and its behavior](options-core-forms-field.md) · [Widgets](options-core-forms-inputwidget.md)", "",
                         "## Errors and saving", "",
                         "- Check `IsValid()` before reading `CleanedData()`; inspect `Errors()` on failure.\n- A form without `WithData` is unbound, not submitted.\n- Validation does not save data or grant write permission.", "",
                         "[Complete multipart, relation and composite examples](forms.md#field-examples) · [Model forms](options-core-forms-modelformoptions.md) · [Formsets](options-core-forms-formsetoptions.md)"]
            else:
                body += ["Set metadata before `api.New` freezes the declaration:", "",
                         block('field.Label = "Value"\nfield.HelpText = "The value returned by this API."'), "",
                         "[Every api.Field option](options-core-api-field.md) · [Definition](options-core-api-definition.md) · [Partial input](options-core-api-bindoptions.md)", "",
                         "## Validation and persistence", "",
                         "- `Validate` cleans input; `Representation` produces output. Neither saves a row.\n- Read-only input is ignored; write-only and hidden fields are omitted from output.\n- Unknown input is rejected unless `AllowUnknown` is enabled.\n- Persistence requires an explicit, authorized mutation policy.", "",
                         "[Mutation routes and policies](api-writes.md) · [OpenAPI](detail-core-api-openapi.md) · [Output schemas](detail-core-api-output-schema.md)"]
            pages.append({"id": identifier, "title": title, "parent": parent, "new": True,
                          "source": "\n".join(body), "description": behavior, "fieldKind": kind,
                          "navGroup": next(group for group, names in GROUPS[family].items() if kind in names.split())})
    return pages
