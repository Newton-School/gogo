package config

import (
	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/apps/fieldlab"
	fieldmigrations "example.com/gogo-showcase/apps/fieldlab/migrations"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func InstalledApps() []app.Config {
	return []app.Config{catalog.App(), {Name: "showcase.fieldlab", Label: "fieldlab", Register: func(r *app.Registry) error {
		for _, schema := range fieldlab.Schemas() {
			if err := r.Register("models", schema.Key(), schema); err != nil {
				return err
			}
		}
		return r.Register("migrations", "fieldlab", fieldmigrations.All)
	}}, {Name: "showcase.accounts", Label: "accounts", Register: func(r *app.Registry) error {
		for _, schema := range builtinSchemas() {
			if err := r.Register("models", schema.Key(), schema); err != nil {
				return err
			}
		}
		all := append(contenttypes.Migrations(), AccountModels().Migrations()...)
		all = append(all, admin.Migrations()...)
		return r.Register("migrations", "accounts", []migrations.Migration(all))
	}}}
}

func builtinSchemas() []models.Schema {
	return append(AccountModels().Schemas(), (&contenttypes.ContentType{}).Schema(), admin.LogSchema())
}

func ModelRegistry() (*models.Registry, error) {
	r := &models.Registry{}
	all := append(catalog.Schemas(), fieldlab.Schemas()...)
	all = append(all, builtinSchemas()...)
	for _, schema := range all {
		if err := r.Register(schema); err != nil {
			return nil, err
		}
	}
	if err := r.Freeze(); err != nil {
		return nil, err
	}
	return r, nil
}
