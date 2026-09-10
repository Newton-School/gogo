# Gogo documentation

The public documentation uses [Docusaurus](https://docusaurus.io/), with separate navigation trees for **Guide**, **Admin**, **Async**, and **Reference**.

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

- **Guide**: first project → project fundamentals → models/databases → HTTP/APIs → forms/templates → security → services → operations.
- **Admin**: setup → lists/search/display → forms/relationships → accounts/staff tools.
- **Async**: tasks/workers → workflows/results → scheduling/retries.
- **Reference**: settings, template vocabulary, and public Go packages grouped by feature.

Each category has an overview page; expanding a category collapses its siblings. Feature pages own their deeper guides. Breadcrumbs locate the current page, the right-hand outline locates a section, and previous/next links follow the tree. Generated API declarations are separate from the learning path, with methods grouped under their types.

## Where to edit

| Source | Purpose |
| --- | --- |
| `tree.json` | Curated sidebar labels, parent categories, order, and deliberate technical-page placement |
| `navigation.json` | Guide titles, source files, feature families, and public-package ownership |
| `index.md`, `guides/` | Human-written tutorials and feature explanations |
| Package-level Markdown | Advanced contracts; included from their original source, not copied by hand |
| `snippets/`, `examples/` | Tested code included in tutorials |
| `tools/catalog/` | Go parser/doc extractor for public APIs, settings, and template vocabulary |
| `generate.py` | Content adapter, ownership checks, and sidebar generation; not an HTML renderer |
| `docusaurus.config.js`, `src/`, `static/` | Framework configuration, landing page, and light/dark styling |
| `.generated/`, `.docusaurus/`, `build/` | Ignored generated content, framework cache, and deployable static output |

Add a guide to `navigation.json` and place it in `tree.json`. Map every public package to exactly one owning guide. Package notes nest under that guide automatically; use `notes` to curate their placement. The build rejects orphaned pages, duplicate placement, missing packages, broken routes, and broken anchors.

Author ordinary Markdown: nested lists, tables, fenced code, links, and headings. Use stable page IDs for guide links, such as `configuration.md`. Package documents keep their original relative source links; links to included documents resolve inside the site. `{{code repository/path.go}}` inserts the exact tested source as a fenced code block. `{{include repository/guide.md}}` embeds a repository document. Includes cannot escape the repository. Never put secrets, private URLs, or local machine paths in examples.

## Verify before committing

```sh
make docs-check
```

This checks tree coverage, content adaptation, field inventories, executable Go examples and the generated-client tutorial, then builds with strict Docusaurus link/anchor validation and checks the built navigation/search assets. The tutorial test does not apply database migrations; it explicitly skips in a packaged Core-only module. Review the production site in Chrome as well: home page, category expansion, breadcrumbs, code copy, search, dark mode, and narrow screens.

`npm --prefix docs audit` checks the documentation dependency graph against the current advisory database. The lockfile pins its resolved versions. `docs/build/` can be deployed as static files; set the public `url` and `baseUrl` in the Docusaurus configuration for the chosen host. No public deployment is configured by default.

### Current build-tool advisory

Docusaurus 3.10.2 pulls in `image-size` 2.0.2. Its [ICNS](https://github.com/advisories/GHSA-w3rx-r6r6-pgpr) and [JXL/HEIF](https://github.com/advisories/GHSA-5p2g-fcmc-qvqq) parsers have no published patch as of September 10, 2026. The docs reject Markdown images, including reference-style images, before Docusaurus invokes that parser. No current guide uses them. The reviewed static SVG logo is served without image-size processing. Keep this guard until a patched dependency is available; an npm audit still reports the upstream dependency and its dependents, so this is a mitigation, not a clean audit. Never run documentation builds on unreviewed executable MDX or configuration. The preview binds only to loopback.

The targeted `serialize-javascript`, Express `qs`, and SockJS `uuid` overrides select patched releases. Their consumers' APIs, production build, and development-server startup are checked when updating the lockfile. These overrides belong only to documentation tooling.

Keep alpha limitations and integration requirements visible. New product behavior still follows AgentFlow; public developer documentation does not replace architecture review or implementation evidence.
