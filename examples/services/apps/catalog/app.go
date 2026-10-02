package catalog

import (
	appmigrations "example.com/gogo-services/apps/catalog/migrations"
	"github.com/Newton-School/gogo/core/app"
)

func App() app.Config {
	return app.Config{Name: "services.catalog", Label: "catalog", Register: func(r *app.Registry) error {
		if err := registerModels(r); err != nil {
			return err
		}
		return r.Register("migrations", "catalog", appmigrations.All)
	}}
}
