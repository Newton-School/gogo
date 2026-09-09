package catalog

import (
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/models"
)

// ProductSerializer exposes only public catalog fields, independently from the
// model and Admin declarations. Internal notes are never included.
func ProductSerializer() (*api.Serializer, error) {
	return api.New(api.Definition{Fields: []api.Field{
		api.IntegerField("id"), api.StringField("name"), api.StringField("slug"),
		api.StringField("description", models.Optional), api.DecimalField("price", 12, 2),
		api.IntegerField("stock"),
	}})
}
