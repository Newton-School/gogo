# Slug suggestions in add forms

```go
admin.ModelAdmin{
    Schema: articleSchema,
    Fields: []string{"title", "subtitle", "slug"},
    PrepopulatedFields: map[string][]string{"slug": {"title", "subtitle"}},
}
```

The add form suggests a slug as the ordered text sources change. Existing targets and saved records are left alone; any manual target edit stops suggestions, including clearing it. This follows the add-form convenience described by [Django's prepopulated fields](https://docs.djangoproject.com/en/6.0/ref/contrib/admin/#django.contrib.admin.ModelAdmin.prepopulated_fields).

The current bounded implementation accepts editable text sources and slug targets, standard text widgets, at most 32 targets and 16 sources each. Excluded, readonly, sensitive, primary-key, relation, choice, and chained target fields are rejected during registration. Dynamic readonly fields disable their entire suggestion mapping. Configuration and widget data are copied per request.

Suggestions default to ASCII lowercasing and decomposable accent normalization. A `models.SlugField` with `models.WithAllowUnicode(true)` enables Unicode NFKC suggestions when the selected form field also permits Unicode. A stricter form override keeps ASCII suggestions. Both modes clean separators and honor the smaller positive model/form code-point length limit. This is not general language transliteration. Inline prepopulation and dynamic mapping hooks remain separate work.

Scripts are external CSP-compatible assets; metadata and values are escaped. No data is fetched or persisted by this feature. Without JavaScript the target remains an ordinary form input. ModelForm validation, readonly rules, unique constraints, scope, and the existing save/audit transaction remain authoritative. An omitted required slug is invalid; suggestions are not a server default or a uniqueness guarantee.

Run the dependency-free script contract tests with `node --test admin/tests/prepopulate.test.cjs`. These VM tests do not claim a live browser visual check.
