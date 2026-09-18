# Admin setup and customization

Admin is an optional module that turns explicitly registered models into staff-facing lists and forms. It is not automatically installed with Core and does not make every model editable.

## Install and wire

Follow [Set up a working Admin](admin-wiring.md) for the full schema/account/session/router integration. This page is the option reference; it is not a replacement for that setup. Then use [Customize your Admin](admin-customization.md) to change one working model at a time.

```sh
go get github.com/Newton-School/gogo/admin@v1.0.0-alpha.1
```

Create `admin.NewSite` with a store, global permission policy, purpose-bound signer and CSRF configuration. Wire account/session authentication and the desired login/logout/password routes separately. Register models before constructing/freezing the site's handler and mount it at its configured prefix.

The default prefix is `/admin/`. Site name, header, title, index title, site URL and account URLs are configurable. Custom template loaders are explicit; importing Admin does not publish a global asset server.

## Register a model

This is the showcase's complete registration file. It receives a configured site and ORM store; its `Product` and `ProductNote` models are in the same app.

{{code examples/showcase/apps/catalog/admin.go}}

Use the [showcase](showcase.md) to run this unchanged. To adapt it, replace its fields, relation policy and action with your own domain's rules. Its relation lookup relies on the surrounding single-organization staff policy; add tenant predicates for a multi-tenant application.

## Feature and option map

| Feature | Configuration |
| --- | --- |
| Model and object creation | `Schema`, `Factory` |
| Form fields | `Fields`, `Exclude`, `FormOverrides` |
| Readonly values | `ReadonlyFields`, request/object-specific `GetReadonlyFields` |
| Form sections | `Fieldsets`, labels, descriptions and classes |
| List columns and links | `ListDisplay`, `ListDisplayLinks`, custom `Columns` |
| List editing | `ListEditable` for allowed stored scalar fields |
| Search | `SearchFields`, `SearchHelpText` |
| Filtering and ordering | `ListFilter`, `Ordering`, `SortableBy` |
| Pagination | `ListPerPage`, `ListMaxShowAll` |
| Empty values | Site/model/column `EmptyValueDisplay` |
| Relations | `AutocompleteFields`, `RawIDFields`, authorized `ResolveRelation` |
| Child rows | `Inlines`, bounded formsets, relation/constraint validation |
| Slug suggestions | `PrepopulatedFields`; browser suggestions, not server defaults |
| Save controls | `SaveOnTop`, explicit save hooks |
| Actions | `Actions`, action permission, confirmation, scoped store |
| Domain enforcement | Global `Policy`, narrower `Authorize`, `SaveModel`, `SaveRelated`, constraint checker |
| Sensitive data | `SensitiveFields`, explicit field selection and representation |
| Account administration | Domain-backed user/group forms, not raw credential-field editing |
| Operations | Change/deletion history, confirmation and authorized audit behavior |

All option types are listed in [ModelAdmin](api-admin.md#modeladmin). Nil and empty slices/pointers can have different meanings; for example, empty `SortableBy` disables user sorting while nil allows available sorts.

## Security and writes

The site's global policy is authoritative. Model authorization may narrow it, never widen it. Actions receive a scoped store; use it rather than escaping to unrestricted model queries. Recheck the current objects and relation targets when applying a confirmed action.

Readonly UI, hidden buttons and an autocomplete allowlist are not substitutes for write-side authorization. Account editing goes through account services so password/grant changes update the appropriate security state.

## Alpha boundaries

Editable changelists support stored scalar fields, not relation/file/image cells or account-domain forms. Unsupported combinations fail registration rather than degrading into unsafe generic fields. Complete Django Admin customization parity and all browser/accessibility combinations are not claimed.

Technical guides below explain list editing, ordering, pagination, inline relations, relation selection, account forms, prepopulation and display overrides in detail.
