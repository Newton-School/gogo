# Sites, content types, pages and feeds

Contrib packages provide optional application building blocks inside Core. They still require explicit registration, storage and route composition.

## Feature map

| Package | Capability | What you must configure |
| --- | --- | --- |
| `core/contrib/contenttypes` | Stable model/content-type records and generic-reference support | Registered schemas, migrations, allowed target models and scope |
| `core/contrib/sites` | Site/domain records, request resolution and optional caching | Trusted host policy, permitted sites and explicit store |
| `core/contrib/redirects` | Stored redirect lookup and HTTP fallback behavior | Site/scope, destination validation and fallback placement |
| `core/contrib/flatpages` | Database-backed content pages with explicit rendering | Site/page access policy, template/rendering and fallback wiring |
| `core/contrib/sitemaps` | Bounded sitemap pages and sitemap indexes | Public-only sources, canonical URLs and limits |
| `core/contrib/syndication` | Atom/RSS feed rendering and handlers | Explicit public feed declarations and safe entries |

Humanize is documented under [Translation and presentation](i18n.md).

## Register data-backed features

Register the package's schemas and migrations where provided, then create its store/service from your project's backend. Mount its handlers explicitly. Importing a contrib package does not reserve routes, create records or load fixtures.

Content-type records are part of the authentication permission model as well as generic relations. Do not accept an arbitrary model label/ID pair from an untrusted caller and treat it as an authorized generic reference.

## Fallbacks

Configure redirects/flatpages through the root router's explicit fallback contract. A fallback must not turn a denied route into readable content, overwrite an existing handler or create an open redirect. Apply site and publication scope at lookup time.

Stored page content is not automatically trusted HTML. Use explicit rendering and sanitization/trust policy appropriate to who can author it.

## Public feeds and search-engine files

Sitemaps and feeds can expose every URL or title they contain. Select genuinely public records before generating them. Bound entry counts, page sizes, string lengths and work; do not turn an unauthenticated sitemap endpoint into an unrestricted database scan.

The sitemap/syndication packages include conditional-response and structured XML behavior, but do not implement every Django contrib extension vocabulary. Their individual technical guides list exact limits. Runnable recipes exist under `examples/showcase/recipes/sitemaps` and `recipes/syndication`.
