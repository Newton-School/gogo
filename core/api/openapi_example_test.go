package api_test

import (
	"context"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

func ExampleOpenAPI() {
	// Application bootstrap supplies its opened backend, frozen registry,
	// policy and mandatory visibility scope. Here catalog.Book has an integer
	// id and a title; its other model fields are not automatically exposed.
	// This example is compile-only: it starts no services and serves no requests.
	build := func(
		ctx context.Context,
		backend db.Backend,
		registry *models.Registry,
		policy auth.Policy,
		scope func(context.Context, auth.Principal, models.Schema) (db.Predicate, error),
	) (*urls.Router, []byte, error) {
		serializer, err := api.New(api.Definition{Fields: []api.Field{
			api.IntegerField("id"), api.StringField("title"),
		}})
		if err != nil {
			return nil, nil, err
		}
		resource, err := api.NewResource(api.ResourceConfig{
			Store: orm.New(backend, registry), Model: "catalog.Book",
			Serializer: serializer, Policy: policy, Scope: scope,
		})
		if err != nil {
			return nil, nil, err
		}
		routes, err := resource.ReadOpenAPIRoutes("books/", "book", api.ReadOpenAPIOptions{
			Security: []api.OpenAPISecurity{{Type: "bearer"}},
		})
		if err != nil {
			return nil, nil, err
		}
		// Generate from this selected API router, whose list/detail handlers are
		// bound to their output schemas. No opaque or mutation routes are added.
		router, err := urls.New(urls.Include("api/", "api",
			urls.Include("v1/", "v1", routes...),
		))
		if err != nil {
			return nil, nil, err
		}
		document, err := api.OpenAPI(ctx, router, api.OpenAPIOptions{
			Title: "Book API", Version: "1.0.0",
		})
		if err != nil {
			return nil, nil, err
		}
		// Generation queries no rows and samples no policy or representation
		// callbacks. The returned bytes are a document, not a docs UI or client.
		// Before serving this router, install real bearer authentication that
		// supplies the verified principal. Security metadata installs nothing.
		// HTTP reads still need the real backend and applied model migrations.
		return router, document, nil
	}
	_ = build
}
