# Django Admin UI

Gogo Admin includes Django 5.2.17 Admin static assets, copied without modification
from `django/contrib/admin/static/admin/` at upstream commit
`e802ada38b3ecf345915163bb6d7f008be411664`:
`https://github.com/django/django/tree/e802ada38b3ecf345915163bb6d7f008be411664`

The HTML templates in `internal/templates/` adapt Django Admin's template
structure to Gogo's Go template engine and existing scoped form/URL contracts.
These adaptations and the assets are covered by Django's BSD-3-Clause license.
The full notice is retained in `internal/assets/django/LICENSE` and embedded in
the compiled application. The asset endpoint also exposes that notice.

Django's bundled jQuery, Select2, XRegExp and image notices are retained in their
original asset directories. Distributors must retain the applicable notices,
including when distributing a compiled binary or container image. Gogo's own
code remains under its existing license. Django does not endorse this project.

Gogo initializes the UI dependencies for the controls present on the page. Shipping
an upstream asset is not a claim that Gogo implements its corresponding Python
backend feature. No Python runtime or external CDN is required by clients.

To update, copy the static directory from an explicitly reviewed upstream
release, retain all license files, update the source reference here and in
`django_assets.go`, then run the Admin tests and browser widget checks.
