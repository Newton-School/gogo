# Admin Operations

The Gogo admin package provides Django-style model registration, admin sites,
staff access policies, auth views, index pages, change lists, change forms,
delete confirmation, history, filters, search, autocomplete, widgets, inlines,
actions, static assets, and queue/admin integration.

## Admin Registration

Register each model deliberately. A production admin should expose only models
that staff users need to inspect or operate.

Review every `ModelAdmin` option that changes data visibility or data mutation:

- `ListDisplay`
- `ListFilter`
- `SearchFields`
- `ReadonlyFields`
- `Fields`
- `Fieldsets`
- `Inlines`
- `Actions`
- `CustomURLs`
- `Hooks`
- Permission hooks

Run `admin.CheckSite(site)` or register `admin.RegisterChecks(registry, site)`
in project checks so invalid field names, list-editable conflicts, and other
admin option errors fail before deploy.

Treat admin checks as release blockers:

- `admin.E001` catches invalid option combinations.
- `admin.E002` catches unknown field references.
- `admin.E003` catches widget options that do not match field metadata.
- `admin.E004` catches missing many-to-many store or file-storage capability.

Do not register sensitive models only because they exist. Secrets, tokens,
sessions, password reset state, and queue internals need explicit review before
admin exposure.

## Admin Security

Admin access must require active authenticated staff users and explicit model
permissions. Use `StaffPermissionPolicy` or a stricter project policy.

Production controls:

- Review the admin URL path before deployment.
- Restrict access with SSO, VPN, IP allowlists, or equivalent controls when the
  platform supports them.
- Rate-limit admin login and password reset flows.
- Enforce strong passwords with built-in validators.
- Keep secure session and CSRF cookies enabled.
- Disable debug mode.
- Log login, logout, add, change, delete, action, and permission-denied events.
- Review custom admin URLs like normal privileged application endpoints.

Never expose admin on a separate host that bypasses the same security
middleware, CSRF protection, host validation, logging, or health controls as the
main application.

## Static Assets

Admin CSS and JavaScript are framework static files. They must be included in
the static collection step and served from the configured static root or CDN.

If admin pages render without styles:

1. Confirm `GOGO_STATIC_URL`.
2. Confirm `GOGO_STATIC_ROOT`.
3. Confirm static collection copied admin assets.
4. Confirm the web process or fronting static server serves the collected path.
5. Confirm cache or CDN invalidation after deploy.

## Template Overrides

Generated projects pass configured template dirs to the admin site. Keep admin
overrides under `templates/admin/...`:

- `templates/admin/base_site.html` customizes the admin wrapper.
- `templates/admin/<app_label>/<model_name>/change_form.html` customizes one
  model form.
- `templates/admin/<app_label>/change_list.html` customizes one app's list
  layout.

Do not put admin customizations in the project-level `templates/base.html`.
That file is for application pages and is intentionally ignored by the admin
partial loader.

## Audit Logs

Admin audit logs should capture:

- Actor.
- Timestamp.
- Model.
- Object identity.
- Action type.
- Changed fields.
- Request ID.
- Source IP or trusted forwarded client identity.
- Permission decision.

Keep audit logs in durable storage. Do not store raw passwords, session cookie
values, CSRF tokens, authorization headers, or full secret fields in audit
entries.

Generated projects install `admin.NewSQLLogStore(store.Database)` when the
configured database opens successfully and fall back to `admin.NewMemoryLogStore()`
only when no model store is available. Back up `gogo_admin_log` with the
application database when audit retention matters.

For large tables, use a `ModelStore` that implements `models.ObjectQueryStore`.
The admin change list passes search terms, filters, ordering, limit, offset,
and total-count intent to that interface so pages stay bounded under load.
Autocomplete and selected-across-pages actions also use the bounded query path.

For many-to-many fields, use a model store that implements
`admin.ManyToManyStore`. For file and image fields, configure
`Site.FileStorage`. The admin form processor validates and saves those fields
through the same add/change lifecycle as scalar fields.

## Admin Health Checks

Admin health should be part of release smoke tests:

- Login page returns success.
- Staff user can log in.
- Index renders registered apps.
- Change list renders for each critical model.
- Add and change form validation works.
- Delete confirmation blocks protected relations.
- History page renders.
- Static assets return success.
- Permission-denied paths are enforced for non-staff users.

Do not use production superuser credentials in automated checks. Create a
dedicated low-risk staff account for smoke tests and rotate it like any other
credential.

## Backups And Rollbacks

Admin depends on the same database backup policy as the application. Include
auth users, groups, permissions, content types, sessions when server-side
sessions are used, and admin log entries.

Before rolling back admin changes:

- Confirm whether new permissions or content types were created.
- Confirm whether new admin actions mutated data.
- Confirm whether deleted objects can be restored.
- Keep old static assets until old web replicas are drained.

If an admin release exposes the wrong model or action, disable the route or
permission first, then decide whether data restoration is needed.
