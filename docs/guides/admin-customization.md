# Customize lists, forms, and actions

Prerequisite: a [working, authenticated Admin site](admin-wiring.md) and registered model schemas. Put model configuration in your app's `admin.go`, not in a global package initializer.

## Complete product registration

Configure one concern at a time before calling `site.Register`. These snippets use the showcase's `catalog.Product` schema:

{{snippet examples/showcase/recipes/documentation/admin_test.go admin-list}}

{{snippet examples/showcase/recipes/documentation/admin_test.go admin-search}}

{{snippet examples/showcase/recipes/documentation/admin_test.go admin-form}}

<details>
<summary>Complete Product and ProductNote registration</summary>

{{code examples/showcase/apps/catalog/admin.go}}

</details>

## What each part changes

| Goal | Change | Verify in the browser |
| --- | --- | --- |
| Choose table columns | `ListDisplay` | Name, price, stock and publication appear |
| Choose the edit link | `ListDisplayLinks` | Name opens the change form |
| Edit values from the list | `ListEditable` | Price/stock/published cells submit through authorized saves |
| Search or filter | `SearchFields`, `ListFilter` | Search matches allowed fields; filters retain row scope |
| Stable sorting/page size | `Ordering`, `ListPerPage` | Sort/page controls produce stable bounded results |
| Make fields non-editable | `ReadonlyFields` | IDs and created timestamps are displayed but not accepted as edits |
| Group the form | `Fieldsets` | Product, Inventory and History sections appear |
| Suggest a slug | `PrepopulatedFields` | Typing a name suggests a slug; this is not a server-side default |
| Add child rows | `Inlines` | Notes are bounded to 20 and belong to the current product |
| Add a bulk operation | `Actions` | Publish requires confirmation and change permission |
| Search a relation | `AutocompleteFields`, `ResolveRelation` | Submitted IDs resolve through an authorized lookup |

The action deliberately saves through its supplied `ScopedStore`. It must not escape to an unrestricted global ORM store. In a multi-tenant app, the relation resolver must include the tenant/current-user predicate; the example's surrounding staff policy permits its single organization.

## Adapt to the first-project Product

The tutorial Product has only `id`, `name`, `price`, and `published`. Do not paste the showcase registration unchanged into that model. Start with this declaration inside your site's registration function:

```go
err := site.Register(admin.ModelAdmin{
    Schema: (&Product{}).Schema(),
    Fields: []string{"name", "price", "published"},
    ReadonlyFields: []string{"id"},
    ListDisplay: []string{"name", "price", "published"},
    SearchFields: []string{"name"},
    ListFilter: []string{"published"},
    ListPerPage: 25,
    ConstraintChecker: store,
})
```

`site` and `store` come from your configured Admin wiring; return the registration error. This declaration does not register accounts, a session store, or routes by itself.

## Next controls to explore

- [Editable lists](detail-admin-list-edit.md): supported scalar fields and denial behavior.
- [Ordering](detail-admin-list-ordering.md) and [pagination](detail-admin-list-pagination.md): defaults and request overrides.
- [Inline forms](detail-admin-inline-relations.md) and [relation selectors](detail-admin-relation-select.md): ownership and authorized IDs.
- [Search help](detail-admin-search-help.md), [empty display](detail-admin-empty-display.md), [prepopulation](detail-admin-prepopulate.md), and [save controls](detail-admin-save-on-top.md).
- [User and group forms](detail-admin-accounts.md): account changes use domain services, not raw password-field editing.

Keep registration errors visible. Unsupported field/widget combinations fail rather than promising every Django Admin control exists.
