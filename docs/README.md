# Gogo documentation

The public documentation uses [Docusaurus](https://docusaurus.io/) with exactly three top-level tabs: **Docs**, **Admin**, and **Async**. Focused guides, feature contracts and Go references have separate sidebar entries under their feature. The home URL opens the documentation directly, without a marketing landing page.

## Open the docs locally

From the repository root:

```sh
make docs
make docs-serve
```

Open [http://localhost:3000](http://localhost:3000). Keep the terminal running; Ctrl+C stops the server. This builds the complete site, including full-text search. No framework server, PostgreSQL, Redis, `.env`, search account, or API key is needed.

Requirements: Node.js 20+, npm, Python 3.10+, and the Go toolchain required by this checkout. The first build downloads pinned npm and Go dependencies. Built pages, fonts, scripts, and the search index are served locally; there are no analytics or external search requests. The docs toolchain is not a dependency of any public Go module.

The old `docs/_site/index.html` viewer is no longer used. Do not open `docs/build/index.html` as a file: Docusaurus needs an HTTP server for routes and search assets.

## Edit and preview

```sh
make docs-dev
```

This starts Docusaurus at the same local URL. It never opens a browser automatically. Do not run the dev and production servers on the same port simultaneously. For a different port after building, run `npm --prefix docs run serve -- --port 3001`.

Content is prepared from source before startup. After changing a guide, package document, tree, or Go declaration, run `npm --prefix docs run generate` in a second terminal; Docusaurus reloads the generated content. CSS and React page edits reload directly. Full-text search is generated during production builds, not in the dev server; use `make docs` and `make docs-serve` to review it.

## How the page trees work

- **Docs**: Introduction, Quickstart, Example, Projects, Configuration, Models, ORM, Migrations, Fixtures, HTTP, API, Forms, Templates, I18n, Auth, Storage, Cache, Mail, Signals, Contrib, Connectors, Deployment, Testing.
- **Admin**: Setup, Models, Accounts.
- **Async**: Setup, Tasks, Workflows, Schedules.

Sidebar categories only expand or collapse: they never open a document or generated landing page. Every document appears exactly once as a leaf. A feature's introductory content is its first **Overview** leaf when that feature has children; its original URL stays valid. Installation, Quickstart, Running, the API tutorial and Docker are separate steps. Small features such as Admin ordering, inline forms, API idempotency and Async result cleanup have their own pages. Each feature's Reference group contains its packages. Search and historical links reach the exact page and heading, including links made when the site used consolidated articles.

## Where to edit

| Source | Purpose |
| --- | --- |
| `tree.json` | Three sidebar trees with direct feature names and no extra category pages |
| `labels.json` | Short, direct page names for the sidebar |
| `sections.json` | Historical consolidated-page layout, retained only for deep-link compatibility and ownership overrides |
| `navigation.json` | Source-fragment inventory and public-package ownership |
| `index.md`, `guides/` | Human-written tutorials and feature explanations |
| `settings-descriptions.json` | Short descriptions for every Core environment setting; the build rejects missing, stale or empty entries |
| `options*.json` | Reviewed configuration behavior keyed by package/type/member; every member of a covered type must have a description or source comment |
| `fields.json`, `field_reference.py` | One focused page per model/form kind and serializer constructor, grouped navigation, formatted standalone examples and exact source-coverage checks |
| Package-level Markdown | Advanced contracts; included from their original source, not copied by hand |
| `snippets/`, `examples/` | Tested code included in tutorials |
| `code-examples.json`, `code_examples.py` | Named, tested 1–20-line usage excerpts mapped to API symbols and configuration members |
| `tools/catalog/` | Go parser/doc extractor for public APIs, settings, and template vocabulary |
| `reference.py` | API reference presentation: symbol indexes, type/method sections, member descriptions, parameter/return tables and expandable declarations |
| `generate.py` | Content adapter, ownership checks, and sidebar generation; not an HTML renderer |
| `docusaurus.config.js`, `src/`, `static/` | Framework configuration, landing page, and light/dark styling |
| `.generated/`, `.docusaurus/`, `build/` | Ignored generated content, framework cache, and deployable static output |

Add a source guide to `navigation.json` with `"new": true` if it did not exist in the historical compact layout, give it a direct name in `labels.json`, and place it in `tree.json`. Map every public package to exactly one source guide. Technical notes and Go references are automatically nested under that feature; explicitly list a note in the tree to move it elsewhere. Preserve `sections.json` as a compatibility map, not a page-count constraint. The build rejects orphaned sources, duplicate placement, missing packages, broken routes and broken anchors. Do not reduce page count by dropping contracts, examples or declarations.

Use `{"id": "...", "label": "...", "items": [...]}` for an expand-only category, `{"guide": "...", "items": [...]}` to place a guide's related leaves, and `{"doc": "..."}` for an explicit leaf. A guide with children automatically becomes a category with an Overview leaf; `leafLabel` can name that introductory leaf more precisely. Explicit placement overrides inferred ownership for both technical notes and package references. Keep environment variables under Configuration, mutations separate from serializers, backend-specific configuration under its connector, and Async workers, results, retries and testing in their own groups.

Author ordinary Markdown: nested lists, tables, fenced code, links, and headings. Use stable page IDs for guide links, such as `configuration.md`. Package documents keep their original relative source links; links to included documents resolve inside the site. `{{code repository/path.go}}` inserts the exact tested source as a fenced code block. `{{include repository/guide.md}}` embeds a repository document. Includes cannot escape the repository. Never put secrets, private URLs, or local machine paths in examples.

Prefer one short explanation, a small usage block, and its expected result or caveat. Mark an excerpt in compiled Go with matching `// docs:begin example-name` and `// docs:end example-name` comments, then insert it with `{{snippet repository/path_test.go example-name}}`. Excerpts must contain 1–20 lines; missing, duplicate, nested and mismatched markers fail the build. Markers are omitted from full-file displays. Keep imports and the complete runnable file in an expandable section after the short examples. Explain fragment prerequisites immediately beside the code. Do not hide essential authorization, validation or persistence warnings in the expandable file.

Map excerpts to API symbols and option members in `code-examples.json`; the build rejects stale mappings. Field constructors reuse the same tested declarations as their individual field pages. Do not invent placeholder callbacks or zero-value assignments to make an option look documented. Core examples run with `go test ./docs/...`; optional-module excerpts run with `go test ./examples/showcase/recipes/documentation ./examples/showcase/recipes/async`.

Every entry in `options*.json` must declare a nonempty `usage` list of `{ "example": "named-excerpt", "title": "Short step" }` items. These appear before individual member contracts; `usageNotes` can add required commands and warnings. Generation fails if usage is missing, an excerpt is unknown, or step titles repeat. A bare declaration such as `Name string` is reference metadata, never a usage example. Member types render inline; map focused examples to members when their choices or behavior need demonstration. Label provider-wiring and simulator examples explicitly, and distinguish schema/SQL-generation checks from live database enforcement.

Keep examples and signatures in the documentation; do not add GitHub source links or source buttons. Repository links to a documented page resolve locally; source-only links render as plain text. Code fences retain required Go module import paths. Generation still checks source coverage internally, without publishing per-symbol source links.

Package references render each type/function as a distinct section and keep methods inside their owning type. Symbol indexes use a responsive grid; large indexes and long type declarations use native, keyboard-accessible disclosures. Parameters and returns use separate tables that stack on narrow screens. Unnamed return values are numbered in declaration order, never assigned invented names or explanations. Member contracts remain beside their types; reviewed configuration types link to their dedicated option pages rather than duplicating another large table. Preserve the original signatures and stable symbol anchors when changing this layout.

Tutorial files under `snippets/storefront/` use `.go.txt` because their imports belong to a generated client module, not the framework workspace. They render as Go and are copied into that client by the tutorial tests. Dockerfile/YAML includes retain their language highlighting. Do not hand-copy a second implementation into prose.

## Writing a usable feature guide

Use direct technical feature names. Put the feature's example next to its behavior and options. State prerequisites, the filename/location of each change, imports, complete code where practical, commands, expected output and failure/denial behavior. Label wiring fragments or compile-only examples explicitly. Give a small independently configurable feature its own page; keep the members of one configuration type together, with individual anchors. Put defaults, nil/empty/zero distinctions, allowed combinations, validation, provider requirements and unsupported behavior next to the option, not only in an introductory paragraph.

`options*.json` overrides are documentation, not runtime defaults. They must match actual constructors and validators. Source comments fill the remaining members of each reviewed type; generation fails if a member is missing or an override names a removed member. Do not invent behavioral descriptions from a name or Go zero value. Every public declaration remains available in the package references, including inputs, outputs and existing source contracts.

In `fields.json`, model rows are `[kind, title, declaration, behavior, validGoValue, invalidGoValue]`; form/serializer rows are `[kindOrConstructor, declaration, behavior, validInput]`. A null input marks a declaration-only example. Form input is literal submitted text; model and serializer values are Go expressions. The tests compile every declaration and execute each supplied validation example. Backend persistence, authorization and browser behavior still need their separate integration checks.

Add a link from **All features** for every package-owning guide. A working public read example must not be described as authenticated CRUD. A constructor-only field catalog must not be described as end-to-end backend support. Distinguish local-source test evidence, pinned published-module recipes, Docker configuration validation, and a real container runtime test.

## Verify before committing

```sh
make docs-check
```

This checks tree coverage, content adaptation, field inventories, executable Go examples and both generated-client tutorial chapters, then builds with strict Docusaurus link/anchor validation and checks the built navigation/search assets. The default tutorial test does not apply database migrations; it explicitly skips in a packaged Core-only module. Run `GOGO_TEST_REQUIRE_SERVICES=1 go test ./tests/integration -run TestDocumentationStorefrontPostgres` for its separate real-database gate. Review the production site in Chrome as well: home page, category expansion, breadcrumbs, code copy, search, dark mode, and narrow screens.

`npm --prefix docs audit` checks the documentation dependency graph against the current advisory database. The lockfile pins its resolved versions. `docs/build/` can be deployed as static files; set the public `url` and `baseUrl` in the Docusaurus configuration for the chosen host. No public deployment is configured by default.

### Build-tool security

The lockfile pins `image-size` 2.0.4, replacing the version affected by the [ICNS](https://github.com/advisories/GHSA-w3rx-r6r6-pgpr) and [JXL/HEIF](https://github.com/advisories/GHSA-5p2g-fcmc-qvqq) parser advisories. The dependency audit reported zero vulnerabilities when this update was verified; rerun it because advisories change. Markdown images, including reference-style images, remain disabled before Docusaurus invokes its image parser. No current guide needs them; use reviewed static theme assets such as the SVG logo. Never run documentation builds on unreviewed executable MDX or configuration. The preview binds only to loopback.

The targeted `serialize-javascript`, Express `qs`, and SockJS `uuid` overrides select patched releases. Their consumers' APIs, production build, and development-server startup are checked when updating the lockfile. These overrides belong only to documentation tooling.

Keep alpha limitations and integration requirements visible. Document new product behavior alongside its implementation and tests; public developer documentation does not replace code review or verification evidence.
