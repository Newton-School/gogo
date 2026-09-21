package catalog

import "github.com/Newton-School/gogo/core/models"

// docs:begin model-struct
type Product struct {
	models.Base
	ID        int64
	Name      string
	Price     string
	Published bool
}

// docs:end model-struct

// docs:begin model-schema
func (*Product) Schema() models.Schema {
	return models.Schema{
		AppLabel: "catalog",
		Name:     "Product",
		Ordering: []string{"name"},
		Fields: []models.Field{
			models.AutoField("id", models.WithStructField("ID")),
			models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(120)),
			models.DecimalField("price", 12, 2, models.WithStructField("Price")),
			models.BooleanField("published", models.WithStructField("Published")),
		},
	}
}

// docs:end model-schema
