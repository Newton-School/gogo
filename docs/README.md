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

Sidebar groups organize feature names without adding empty category documents. Installation, Quickstart, Running, the API tutorial and Docker are separate steps. Small features such as Admin ordering, inline forms, API idempotency and Async result cleanup have their own pages. Each feature's Reference group contains its packages. Search and historical links reach the exact page and heading, including links made when the site used consolidated articles.

## Where to edit

| Source | Purpose |
| --- | --- |
| `tree.json` | Three sidebar trees with direct feature names and no extra category pages |
| `labels.json` | Short, direct page names for the sidebar |
| `sections.json` | Historical consolidated-page layout, retained only for deep-link compatibility and ownership overrides |
| `navigation.json` | Source-fragment inventory and public-package ownership |
| `index.md`, `guides/` | Human-written tutorials and feature explanations |
| `settings-descriptions.json` | Short descriptions for every Core environment setting; the build rejects missing, stale or empty entries |
| Package-level Markdown | Advanced contracts; included from their original source, not copied by hand |
| `snippets/`, `examples/` | Tested code included in tutorials |
| `tools/catalog/` | Go parser/doc extractor for public APIs, settings, and template vocabulary |
| `generate.py` | Content adapter, ownership checks, and sidebar generation; not an HTML renderer |
| `docusaurus.config.js`, `src/`, `static/` | Framework configuration, landing page, and light/dark styling |
| `.generated/`, `.docusaurus/`, `build/` | Ignored generated content, framework cache, and deployable static output |

Add a source guide to `navigation.json`, give it a direct name in `labels.json`, and place it in `tree.json`. Map every public package to exactly one source guide. Technical notes and Go references are automatically nested under that feature; explicitly list a note in the tree to move it elsewhere. Preserve `sections.json` as a compatibility map, not a page-count constraint. The build rejects orphaned sources, duplicate placement, missing packages, broken routes and broken anchors. Do not reduce page count by dropping contracts, examples or declarations.

Author ordinary Markdown: nested lists, tables, fenced code, links, and headings. Use stable page IDs for guide links, such as `configuration.md`. Package documents keep their original relative source links; links to included documents resolve inside the site. `{{code repository/path.go}}` inserts the exact tested source as a fenced code block. `{{include repository/guide.md}}` embeds a repository document. Includes cannot escape the repository. Never put secrets, private URLs, or local machine paths in examples.

Tutorial files under `snippets/storefront/` use `.go.txt` because their imports belong to a generated client module, not the framework workspace. They render as Go and are copied into that client by the tutorial tests. Dockerfile/YAML includes retain their language highlighting. Do not hand-copy a second implementation into prose.

## Writing a usable feature guide

Use direct technical feature names. Put the feature's example next to its behavior and options. State prerequisites, the filename/location of each change, imports, complete code where practical, commands, expected output and failure/denial behavior. Label wiring fragments or compile-only examples explicitly. Keep exact signatures and advanced contracts expandable on the same page; do not create another article for each option or method.

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
