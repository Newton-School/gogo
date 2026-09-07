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
This checkpoint does not establish complete field compatibility: the model's
default maximum length and implicit database index remain separate schema work.

The option is carried by schema clones, historical migration state and generated
migration source. Its false value is omitted from JSON so existing migration
checksums do not change merely because the descriptor gained an option. Enabling
it is an explicit detected field change. Ordinary ORM Save still does not call
FullClean automatically; database writes are not an alphabet validator.
