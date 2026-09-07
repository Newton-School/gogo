# Slug validation

`models.SlugField("slug", models.WithAllowUnicode(true))` accepts Unicode
letters and numbers as well as underscores and hyphens. The default is ASCII
letters/numbers, underscores and hyphens. Combining marks, spaces, path
separators, punctuation, emoji and invalid UTF-8 are rejected in both modes.
Validation does not normalize, lowercase or transliterate stored values.

`forms.Field.AllowUnicode` selects the same alphabet on a plain Slug field;
`FieldFromModel`, ModelForm and model-derived API serializers preserve the
model option. Slug forms strip surrounding whitespace by default; set `Strip`
to false to preserve it and then validate strictly. Empty values still obey
the form's Required policy. Model/API value validation does not silently trim.
Explicit length bounds count Unicode code points, not bytes or graphemes.

These alphabet rules follow the documented [Django SlugField option](https://docs.djangoproject.com/en/6.0/ref/models/fields/#slugfield)
and [form SlugField behavior](https://docs.djangoproject.com/en/6.0/ref/forms/fields/#slugfield).
`SlugField` defaults to a maximum of 50 code points and `DBIndex=true`.
`WithMaxLength(...)` and `WithDBIndex(false)` explicitly override those defaults.
PostgreSQL migrations create the corresponding varchar bound and owned B-tree
index; unique fields and effective single-column primary keys do not get a
redundant plain index. This does not establish complete field compatibility.

The option is carried by schema clones, historical migration state and generated
migration source. Its false value is omitted from JSON so existing migration
checksums do not change merely because the descriptor gained an option. Enabling
it is an explicit detected field change. Ordinary ORM Save still does not call
FullClean automatically; database writes are not an alphabet validator.

The new constructor defaults do not rewrite generated historical descriptors.
Hand-written historical migrations must use fixed field metadata instead of
reconstructing old fields from current constructor defaults. See the
[field index lifecycle](../../connectors/postgres/field_indexes.md) for legacy
metadata and index ownership reconciliation.
