# Admin Reference

The admin package provides model registration, sites, staff access policies, auth views, index pages, change lists, change forms, delete confirmation, history, filters, search, autocomplete, widgets, inlines, actions, static assets, and queue/admin docs integrations.

## Public Types

| Area | Types |
| --- | --- |
| Site | `Site`, `SiteOptions`, `SiteCollection`, `PermissionPolicy`, `StaffPermissionPolicy`, `SessionPermissionPolicy`, `ModelObjectStore`, `AdminLogStore` |
| Registry | `Registry`, `ModelAdmin`, `ModelAdminHooks` |
| Model options | `Fieldset`, `Inline`, `InlineKind`, `URLPattern`, `ComputedColumn` |
| Auth views | `AuthViewConfig` |
| Change list | `ChangeList`, `ChangeListColumn`, `ChangeListRow`, `DateBucket` |
| Change form | `ChangeFormInput`, `ChangeFormContext`, `ChangeFormField`, `RelatedPopup`, `JavaScriptCatalogResponse` |
| Delete | `DeletionObject`, `DeletionSummary` |
| History | `HistoryPage`, `AdminLogEntry`, `MemoryLogStore`, `SQLLogStore` |
| Filters/search | `FilterChoice`, `FilterSpec`, `FilterState`, `FilterResult`, `SearchOptions`, `SearchQuery`, `AutocompleteConfig`, `AutocompleteResult`, `AutocompleteResponse` |
| Actions | `Action`, `ActionContext`, `ActionResult`, `ActionStore`, `ActionRegistry`, `MemoryActionStore` |
| Inlines | `InlineInput`, `InlineFormset`, `InlineForm`, `InlineStore`, `MemoryInlineStore` |
| Widgets | `WidgetChoice`, `WidgetConfig` |

`CheckSite` validates registered `ModelAdmin` options, and `RegisterChecks`
adds those results to the shared `checks.Registry`.

`SiteOptions.TemplateDirs` and `Site.TemplateDirs` configure admin template
override directories. Generated projects resolve `GOGO_TEMPLATE_DIRS` against
the project root and pass those directories to the admin site.

## ModelAdmin Options

`ModelAdmin` supports:

`Actions`, `ActionsOnTop`, `ActionsOnBottom`, `ActionsSelectionCounter`, `AutocompleteFields`, `DateHierarchy`, `EmptyValueDisplay`, `Exclude`, `Fields`, `Fieldsets`, `FilterHorizontal`, `FilterVertical`, `Form`, `FormfieldOverrides`, `Inlines`, `ListDisplay`, `ListDisplayLinks`, `ListEditable`, `ListFilter`, `ListMaxShowAll`, `ListPerPage`, `ListSelectRelated`, `Ordering`, `Paginator`, `PrepopulatedFields`, `PreserveFilters`, `RadioFields`, `RawIDFields`, `ReadonlyFields`, `ReadOnly`, `SaveAs`, `SaveAsContinue`, `SaveOnTop`, `SearchFields`, `SearchHelpText`, `ShowFacets`, `SortableBy`, `ViewOnSite`, `CustomURLs`, `ComputedColumns`, `ActionDefinitions`, and `Hooks`.

Set `ReadOnly` to true for admin registrations that should allow staff users to
view modules and objects while blocking add, change, and delete by default.

Change forms validate POST data through model-form metadata before calling the
site `ModelStore`. Metadata-backed stores can implement `models.ObjectQueryStore`
to let change lists push search, filters, ordering, limit, offset, and total
count into the storage layer instead of loading every row into memory.

## Store Capabilities

`Site.ModelStore` must implement `ModelObjectStore` for add, change, delete,
history object lookups, and inlines. Large tables should use a store that also
implements `models.ObjectQueryStore`; changelists, autocomplete, and
selected-across-pages actions pass bounded query intent through that interface.

Many-to-many admin fields require the configured model store to implement
`admin.ManyToManyStore`. File and image fields require `Site.FileStorage`.
Without those capabilities, `CheckSite` returns `admin.E004` instead of letting
the form silently drop submitted data.

`Site.LogStore` stores object history. `admin.NewSQLLogStore(database)` persists
history in the `gogo_admin_log` table and creates the table/indexes on demand.
Generated projects install the SQL log store when `NewModelStore` can open the
configured database and fall back to `MemoryLogStore` only when no database
store is available.

## Template Overrides

Admin templates are embedded in the framework and can be overridden from
configured template dirs. For a model page such as `change_form.html`, Gogo
checks these override names in order:

1. `admin/<app_label>/<model_name>/change_form.html`
2. `admin/<app_label>/change_form.html`
3. `admin/change_form.html`
4. `change_form.html` for full target-template overrides kept for compatibility

Shared partials intentionally use only `admin/...` names so an application
`templates/base.html` cannot accidentally replace the admin base. Override
`admin/base_site.html` to wrap or customize all admin pages. Override
`admin/popup_response.html` for related-object popup responses.

## Hooks

`ModelAdminHooks` covers queryset, ordering, search, display, filters, readonly fields, fields, fieldsets, forms, save/delete lifecycle, related saves, response hooks, messaging, lookup checks, deleted object summaries, paginator, autocomplete, prepopulation, select-related, sortable fields, inlines, custom URLs, and add/change/delete/view/module permissions.

## Views

Admin auth views:

- `LoginView`
- `LogoutView`
- `PasswordChangeView`
- `PasswordChangeDoneView`

Admin site access defaults to active authenticated staff users. Generated
projects configure `SessionPermissionPolicy` with the built-in file user store
and file session store so `/admin/` redirects anonymous users to login and
allows staff users created with `go run manage.go createsuperuser`.

Set `Site.ModelStore` to a metadata store, such as `orm.MetadataStore`, to
enable add, change, delete, autocomplete, actions, inlines, and query-backed
change lists. Set `Site.LogStore` to an `AdminLogStore` implementation to render
durable object history.

## Checks

`CheckSite` reports clear errors for invalid or unsupported admin paths:

| ID | Meaning |
| --- | --- |
| `admin.E001` | Invalid option combination, such as `list_editable` not in `list_display` or also in `list_display_links`. |
| `admin.E002` | An admin option references a field that is not in metadata and is not a computed column. |
| `admin.E003` | A widget option is incompatible with field metadata, such as `raw_id_fields` on a non-relation field. |
| `admin.E004` | The site is missing required runtime capability for many-to-many persistence or file storage. |

## Errors

`ErrInvalidURLPrefix`, `ErrDuplicateSite`, `ErrAlreadyRegistered`, `ErrNotRegistered`, `ErrUnmanagedModel`, `ErrInvalidModelAdminOption`, `ErrAdminPermissionDenied`, `ErrInvalidChangeListQuery`, `ErrProtectedRelation`, and `ErrInvalidInlineFormset`.

## Example

```go
meta := models.Metadata{AppLabel: "blog", ModelName: "Post", TableName: "blog_post"}
registry := admin.NewRegistry()
err := registry.RegisterMetadata(meta, admin.ModelAdmin{ListDisplay: []string{"title"}})
_ = err
```
