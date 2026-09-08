package http_test

import (
	"context"
	"net/http"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

// Bootstrap supplies an opened backend. Registering descriptors does not create
// their tables: the application's migrations must be applied separately.
func exampleBookReadStore(backend db.Backend) (*orm.Store, error) {
	registry := &models.Registry{}
	if err := registry.Register(models.Schema{AppLabel: "catalog", Name: "Book", Fields: []models.Field{
		models.UUIDField("id", models.Primary), models.UUIDField("owner_id"),
		models.TextField("title"), models.BooleanField("published"),
	}}); err != nil {
		return nil, err
	}
	if err := registry.Freeze(); err != nil {
		return nil, err
	}
	return orm.New(backend, registry), nil
}

func exampleBookModelOptions(store *orm.Store) ghttp.ModelReadOptions {
	return ghttp.ModelReadOptions{
		Store: store, Model: "catalog.Book", Fields: []string{"title"}, PolicyFields: []string{"owner_id"},
		Scope: func(ctx context.Context, principal auth.Principal, _ models.Schema) (db.Predicate, error) {
			if err := ctx.Err(); err != nil {
				return db.Predicate{}, err
			}
			// Verified user IDs in this application are UUIDs. Visibility belongs
			// in scope so pagination cannot include another user's private rows.
			return orm.And(orm.Q("owner_id", principal.ID), orm.Q("published", true)), nil
		},
		Policy: auth.PolicyFunc(func(ctx context.Context, principal auth.Principal, action string, resource auth.Resource) error {
			if err := (auth.ModelPolicy{}).Authorize(ctx, principal, action, resource); err != nil {
				return err
			}
			if resource.Object == nil {
				return nil // Model-level grant; object grants follow scoped lookup.
			}
			record, ok := resource.Object.(models.Record)
			if !ok {
				return auth.ErrPermissionDenied
			}
			owner, err := record.Get("owner_id")
			if err != nil || owner != principal.ID {
				return auth.ErrPermissionDenied
			}
			return nil
		}),
	}
}

func exampleBookRequestGrant(request *http.Request) error {
	if !auth.FromContext(request.Context()).Authenticated {
		return auth.ErrUnauthenticated
	}
	return request.Context().Err()
}

func ExampleNewListView() {
	// This constructor can be called by application bootstrap with its backend.
	// The example is compile-only: it does not open a database or serve requests.
	build := func(backend db.Backend) (http.Handler, error) {
		store, err := exampleBookReadStore(backend)
		if err != nil {
			return nil, err
		}
		page, err := ghttp.NewListView(ghttp.ListViewOptions{
			ModelReadOptions: exampleBookModelOptions(store),
			TemplateViewOptions: ghttp.TemplateViewOptions{
				ReadViewOptions: ghttp.ReadViewOptions{Authorize: exampleBookRequestGrant},
				TemplateName:    "books/list.html",
				Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
					"books/list.html": `{% for book in object_list %}<p>{{ book.title }}</p>{% endfor %}{% if page.has_next %}<a href="{{ page.next }}">Next</a>{% endif %}`,
				}}},
			},
			Ordering: []string{"title"}, Pagination: pagination.Config{DefaultSize: 25, MaxSize: 100},
		})
		if err != nil {
			return nil, err
		}
		// Trusted authentication middleware must install the verified principal,
		// including the catalog.view_book permission, before reaching this router.
		return urls.New(urls.Path("books/", page, "book-list"))
	}
	_ = build
}

func ExampleNewDetailView() {
	build := func(backend db.Backend) (http.Handler, error) {
		store, err := exampleBookReadStore(backend)
		if err != nil {
			return nil, err
		}
		page, err := ghttp.NewDetailView(ghttp.DetailViewOptions{
			ModelReadOptions: exampleBookModelOptions(store),
			TemplateViewOptions: ghttp.TemplateViewOptions{
				ReadViewOptions: ghttp.ReadViewOptions{Authorize: exampleBookRequestGrant},
				TemplateName:    "books/detail.html",
				Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
					"books/detail.html": `<h1>{{ object.title }}</h1>`,
				}}},
			},
			Key: func(request *http.Request) (map[string]any, error) {
				id, ok := urls.Param(request, "id").(string)
				if !ok {
					return nil, ghttp.ErrInvalidLookup
				}
				return map[string]any{"id": id}, nil
			},
		})
		if err != nil {
			return nil, err
		}
		return urls.New(urls.Path("books/<uuid:id>/", page, "book-detail"))
	}
	_ = build
}
