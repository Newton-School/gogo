# Templates

Gogo's template engine provides Django-like template syntax with explicit Go loaders, context and extension callbacks. It is not a Python template runtime, and arbitrary application methods are not callable from templates.

## Render a template

**Load a template**

{{snippet docs/examples/templates_test.go template-loader}}

**Render values**

{{snippet docs/examples/templates_test.go template-render}}

Result: `<h1>NOTEBOOK</h1><p>&lt;b&gt;Plain text&lt;/b&gt;</p>`. Untrusted text is escaped.

<details>
<summary>Complete runnable example, including imports</summary>

{{code docs/examples/templates_test.go}}

</details>

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

## Filter reference

Use a filter after a pipe, for example `{{ name|upper }}`. Supply an argument after a colon, for example `{{ name|default:"Untitled" }}`. These are the default engine's filters, grouped by use:

| Filters | Behavior and arguments |
| --- | --- |
| `lower`, `upper`, `capfirst`, `title` | Change text case; no argument |
| `cut` | Remove occurrences of the supplied text |
| `addslashes` | Escape quotes/backslashes as text; not general JavaScript-context escaping |
| `slugify` | Produce the engine's simple lower-case slug transformation; not a unique identifier generator |
| `default`, `default_if_none` | Substitute an argument for false-like values or only nil, respectively |
| `length`, `first`, `last`, `make_list` | Inspect a collection or convert text into a sequence |
| `join`, `slice` | Join using a delimiter; slice using a start:end:step string with nonzero step |
| `yesno` | Choose true/false/optional-nil text from a comma-separated argument |
| `pluralize` | Select singular/plural suffixes; full language pluralization belongs to i18n catalogs |
| `add`, `divisibleby`, `get_digit` | Integer addition or string concatenation, divisibility, or a digit counted from the right |
| `escape`, `force_escape`, `escapeseq` | Escape text or sequence elements; force_escape also escapes already trusted HTML |
| `safe`, `safeseq` | Accept only explicitly trusted SafeHTML values; ordinary strings are refused |
| `linebreaks`, `linebreaksbr` | Escape text and render paragraph/line-break HTML |
| `striptags` | Strip matching tags for display; **not an HTML sanitizer** |
| `wordcount`, `truncatechars`, `truncatewords`, `linenumbers` | Count, truncate with a numeric bound, or number lines |
| `ljust`, `rjust`, `center` | Pad text to the supplied width, subject to the engine's bound |
| `urlencode`, `iriencode` | URL/query-oriented encoding; not destination authorization |
| `pprint` | Display the engine's text representation; not unrestricted object introspection |
| `floatformat` | Floating-point formatting with a decimal-place argument; not exact monetary arithmetic |
| `filesizeformat` | Human-readable size using powers of 1024 |
| `date`, `time` | Calendar/clock formatting; see the detailed date-format contract |
| `localtime`, `utc`, `timezone` | Explicit timezone conversion; timezone takes the selected zone argument |
| `dictsort`, `dictsortreversed` | Sort a sequence by the supplied lookup key |
| `json_script` | Emit escaped JSON in an application/json script element with optional element ID |

## Escaping and limits

Ordinary values are escaped for their output context. Do not convert user input into `templates.SafeHTML`; that type is an explicit trusted-content boundary. A `safe` filter is not a sanitizer.

Configure recursion, loop and output budgets rather than letting an unbounded collection determine rendering work. Custom processors, tags and filters are trusted Go callbacks: they must honor context, return promptly and avoid secret exposure.

## Static assets, forms and localization

Register the static manifest's template integration when you want fingerprinted asset URLs. Pass trusted form rendering output only through the documented template boundary. Locale and timezone state belongs in the current request context, not a mutable global variable.

See [Static assets](static.md), [Forms](forms.md) and [Translation](i18n.md) for their registration steps. Do not assume that a Python/Django tag or third-party template extension is available merely because its syntax looks familiar.
