package catalog

import (
	"context"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

func Routes(store *orm.Store) ([]urls.Route, error) {
	serializer, err := ProductSerializer()
	if err != nil {
		return nil, err
	}
	resource, err := api.NewResource(api.ResourceConfig{
		Store: store, Model: "catalog.Product", Serializer: serializer,
		AllowAnonymous: true, IncludeCount: true, EntityTags: true,
		Policy: auth.PolicyFunc(func(ctx context.Context, _ auth.Principal, action string, _ auth.Resource) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if action != "view" {
				return auth.ErrPermissionDenied
			}
			return nil
		}),
		Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
			return orm.Q("published", true), nil
		},
	})
	if err != nil {
		return nil, err
	}
	return resource.ReadOpenAPIRoutes("products/", "product", api.ReadOpenAPIOptions{Security: []api.OpenAPISecurity{{Type: "public"}}})
}
