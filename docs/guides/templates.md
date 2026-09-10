# Templates

Gogo's template engine provides Django-like template syntax with explicit Go loaders, context and extension callbacks. It is not a Python template runtime, and arbitrary application methods are not callable from templates.

## Render a template

{{code docs/examples/templates_test.go}}

Use `MapLoader` for small tests and `FSLoader` with an application-owned `fs.FS` for real template files. Configure loaders, filters, tags and context processors before concurrent use.

## Core syntax

| Feature | Syntax |
| --- | --- |
| Values and filters | `{{ name }}` and the `upper` filter |
| Conditions | `{% if condition %}`, `elif`, `else`, `endif` |
| Iteration | `{% for item in items %}`, `empty`, `endfor` |
| Inheritance | `extends`, `block`, `endblock` |
| Composition | `include`, `with`, `only`, named partial definitions |
| Escaping and literal text | `autoescape`, `verbatim`, `comment`, `templatetag` |
| Presentation helpers | `cycle`, `resetcycle`, `ifchanged`, `firstof`, `regroup`, `widthratio`, `spaceless` |
| Time | `now`, `timezone`, `localtime`, `get_current_timezone` |
| Integration tags | CSRF, query-string helpers and explicitly registered service tags |

The [template vocabulary](template-vocabulary.md) is generated from a default engine's `Describe` output. It lists the actual built-in tags and filters; installed extensions can add their own names. Description does not load or render templates.

## Escaping and limits

Ordinary values are escaped for their output context. Do not convert user input into `templates.SafeHTML`; that type is an explicit trusted-content boundary. A `safe` filter is not a sanitizer.

Configure recursion, loop and output budgets rather than letting an unbounded collection determine rendering work. Custom processors, tags and filters are trusted Go callbacks: they must honor context, return promptly and avoid secret exposure.

## Static assets, forms and localization

Register the static manifest's template integration when you want fingerprinted asset URLs. Pass trusted form rendering output only through the documented template boundary. Locale and timezone state belongs in the current request context, not a mutable global variable.

See [Static assets](static.md), [Forms](forms.md) and [Translation](i18n.md) for their registration steps. Do not assume that a Python/Django tag or third-party template extension is available merely because its syntax looks familiar.
