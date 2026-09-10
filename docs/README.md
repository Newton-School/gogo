# Gogo documentation

Start reading at [Introduction](index.md) or build the complete offline documentation site:

```sh
make docs
```

Open `docs/_site/index.html` directly in a browser. No server, npm install, Python packages, database, Redis or framework `.env` is needed. Build dependencies are Python 3.10+ and the Go toolchain required by this repository. Normal Go dependency resolution may need network access on a fresh checkout. The built site uses local CSS/JavaScript, no CDN, analytics or remote fonts, and works under `file://` as well as static HTTP hosting.

## Structure

- `navigation.json`: ordered feature-parent navigation and public-package ownership.
- `index.md`, `guides/`: developer tutorials and feature explanations.
- `snippets/`, `examples/`: tested code included in tutorials.
- `tools/catalog/`: standard-library Go parser/doc extractor for every public library package, Core settings and default template vocabulary.
- `build.py`, `assets/`: small static renderer and accessible documentation shell.
- `tests/`, `tutorial_test.go`: rendering/link/security checks and a generated-client tutorial test.
- `_site/`: ignored generated output, including API pages and a coverage summary.

Existing package-level Markdown files are included as nested technical guides rather than copied into a second manually maintained source. New public packages must be mapped to a guide in `navigation.json`; the build fails if one is missing. Reference pages include exported types/fields, interfaces, declaration groups, functions and methods. They exclude private implementation and executable commands, whose usage belongs in the management guide.

## Authoring

Use headings, paragraphs, fenced code, tables, flat lists, block quotes, inline code, emphasis and Markdown links. Raw HTML is escaped. Use page IDs for internal links, such as `configuration.md`; the site resolves them to HTML. Source Markdown is readable in a repository viewer, but code-inclusion directives expand only in the built site.

`{{code repository/path.go}}` includes the exact source as a code block. `{{include repository/guide.md}}` embeds an existing technical document. Includes are confined to repository files. Keep examples free of secrets, private URLs and local machine paths.

Run `make docs-check` before committing changes. It runs renderer/field-inventory tests, the Go examples/tutorial, JavaScript syntax/search tests and a site build with local-link/anchor checks. The tutorial test uses temporary local module replacements and does not apply database migrations; it runs only in a complete multi-module checkout and explicitly skips inside a packaged Core-only module. The independently pinned showcase is a separate release-consumer check.

Do not claim a capability is complete because its name appears in the generated reference. Keep descriptor-only, compile-only, unit-tested, real-provider and browser/deployment evidence distinct. New product behavior still follows AgentFlow approval; these public docs do not replace that workflow.
