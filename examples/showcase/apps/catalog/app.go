package catalog

import (
	"example.com/gogo-showcase/apps/catalog/migrations"
	"github.com/Newton-School/gogo/core/app"
)

func App() app.Config {
	return app.Config{Name: "example.com/gogo-showcase/apps/catalog", Label: "catalog", Register: func(registry *app.Registry) error {
		for _, schema := range Schemas() {
			if err := registry.Register("models", schema.Key(), schema); err != nil {
				return err
			}
		}
		return registry.Register("migrations", "catalog", migrations.All)
	}}
}
