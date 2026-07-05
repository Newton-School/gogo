# Admin Tutorial

This tutorial customizes a `Post` admin with list columns, search, filters, readonly fields, actions, inlines, autocomplete, and permission hooks.

## Base Registration

```go
_ = admin.ModelAdmin{
	ListDisplay:  []string{"title", "author", "status", "published_at"},
	SearchFields: []string{"title", "body", "author__name"},
	ListFilter:   []string{"status", "author"},
}
```

`ListDisplay` controls columns. `SearchFields` controls text search. `ListFilter` controls sidebar filters.

## Form Layout

```go
_ = admin.ModelAdmin{
	Fieldsets: []admin.Fieldset{
		{Name: "Content", Fields: []string{"title", "slug", "body"}},
		{Name: "Publishing", Fields: []string{"status", "published_at"}},
	},
	ReadonlyFields: []string{"created_at", "updated_at"},
}
```

`ReadonlyFields` prevents staff from editing generated values.

## Actions

Create bulk `Actions` for publish/unpublish:

```go
publish := admin.Action{
	Name:        "publish_posts",
	Label:       "Publish selected posts",
	Permissions: []string{"blog.change_post"},
	Handler: func(ctx admin.ActionContext) (admin.ActionResult, error) {
		return admin.ActionResult{Message: "Published selected posts"}, nil
	},
}

_ = admin.ModelAdmin{
	ActionDefinitions: []admin.Action{publish},
	Actions:           []string{"publish_posts"},
}
```

## Inlines

Use `Inlines` for comments below a post:

```go
_ = admin.Inline{
	Model:     "blog.Comment",
	Kind:      admin.InlineTabular,
	Extra:     1,
	CanDelete: true,
}
```

## Autocomplete

Use `AutocompleteFields` for large relations:

```go
_ = admin.ModelAdmin{
	AutocompleteFields: []string{"author", "tags"},
}
```

Pair it with `SearchFields` on related admins.

## Permission Hooks

Use hooks for object-sensitive permissions:

```go
_ = admin.ModelAdmin{
	Hooks: admin.ModelAdminHooks{
		HasChangePermission: func(r *http.Request, user auth.User) bool {
			return user.IsStaff && user.IsActive && auth.HasPerm(user, "blog.change_post")
		},
	},
}
```

`HasChangePermission`, add/delete/view hooks, and module permission hooks are evaluated by admin views and actions.

## Checks And Storage

Use admin checks during startup or CI:

```go
registry := checks.NewRegistry()
admin.RegisterChecks(registry, site)
```

Set `Site.ModelStore` to an `orm.MetadataStore` or another store implementing
`models.ObjectQueryStore` so change lists can search, filter, order, paginate,
and count through the storage layer. Set `Site.LogStore` to an
`admin.AdminLogStore` implementation so object history records additions,
changes, deletions, and actions.

## Testing

Use `testing.NewAdminClient` to attach a staff user, `testing.AssertAdminModelRegistered` for registry checks, and `testing.AssertAdminColumn` for rendered page checks.
