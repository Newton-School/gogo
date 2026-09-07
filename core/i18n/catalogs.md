# Application translation catalogs

Build one `Translator` from typed catalogs at startup. `NewTranslator` validates
the complete input and compiles private lookup maps; a failed compilation returns
no partial translator. Catalog JSON fields are ordinary source data, not an
executable plural-expression language.

```go
translator, err := i18n.NewTranslator(locales, i18n.TranslatorConfig{
    Catalogs: []i18n.Catalog{{
        Language: "fr",
        Domain: "shop",
        Messages: []i18n.Translation{
            {ID: "Open", Text: "Ouvrir"},
            {ID: "Open", Context: "state", Text: "Ouvert"},
            {ID: "item", Forms: map[i18n.PluralForm]string{
                i18n.One: "article", i18n.Other: "articles",
            }},
        },
    }},
})
if err != nil {
    return err
}
label, err := translator.Pgettext(ctx, "shop", "state", "Open")
itemLabel, err := translator.Ngettext(ctx, "shop", "item", "items", count)
```

| Lookup step | Exact behavior |
| --- | --- |
| Locale | Use the explicit request/task context, revalidated against the resolver; no context locale uses its default. Nil/cancelled contexts fail. |
| Key | `(domain, context, source ID)`; there is no fallback to another app domain or from a contextual key to an unqualified key. |
| Catalog chain | Selected language → CLDR parents → configured fallback languages and each one's parents. Parents follow the pinned Go language data, not naive subtag removal. |
| Default fallback list | Nil means the resolver's default language; an explicit empty list disables additional languages, not the selected language's parent chain. |
| Singular | `Gettext`/`Pgettext` return the selected plain text, including a deliberately empty translation. |
| Plural | `Ngettext`/`NPgettext` select Zero/One/Two/Few/Many/Other using the language of the catalog that supplies the message. An absent category uses that entry's required Other. |
| No matching key | Return the unchanged source; plural source fallback uses singular only for count exactly 1. An optional missing-key observer runs only in development. |
| Output | Plain Go string, never trusted HTML, template execution, printf interpolation or a mutation of a stored source value. Normal template escaping still applies. |

Integer plural selection uses the pinned
[Go CLDR cardinal rules](https://pkg.go.dev/golang.org/x/text/feature/plural#Rules.MatchPlural).
The complete signed `int64` range is supported without float conversion or
absolute-value overflow. Translation categories use absolute magnitude; source
fallback is the explicit singular/other rule above. Decimal and ordinal plural
operands are not part of this API.

Catalog languages must be allowed resolver languages or their CLDR parents.
Additional configured fallback languages must themselves be allowed. Duplicate
keys—even across catalog fragments—fail, and the same key cannot alternate
between singular/plural kinds in different languages. Project overrides must be
resolved explicitly before passing catalogs; declaration order never silently
overrides an app message. All slices/maps are detached at construction.

Compilation accepts at most 4096 catalog fragments, 100,000 entries and 64 MiB of
aggregate key/text bytes. Domains are bounded ASCII app identifiers; source IDs
are bounded to 4096 UTF-8 bytes, contexts to 512 and each text to 64 KiB. NUL and
invalid UTF-8 are rejected. No more than six known plural categories are accepted,
Other is mandatory, and nonempty singular Text cannot accompany Forms.
An explicit nonnil empty Forms map is malformed; singular entries leave it nil.

## Lazy messages

`LazyMessage{Domain: "shop", Singular: "Open"}` stores only source strings.
Use `Resolve(ctx, translator)` inside each request/task. For plural values, supply
`Plural` and call `ResolveCount(ctx, translator, count)`. The same immutable value
can resolve concurrently to different languages; it deliberately has no String
method that could capture a process/default locale prematurely.

The development missing-key observer receives language/domain/context/static ID
metadata, not formatting arguments or task data. It must be bounded and
concurrency-safe. Its panic cannot replace the source fallback; context
cancellation still stops output. Production configuration never calls it.

This checkpoint provides in-memory catalog compilation and lookup. PO/MO file
compatibility, source extraction/`makemessages`, `compilemessages` CLI, localized
number/currency/date formatting, template translation tags and automatic
application catalog discovery remain separate work.
