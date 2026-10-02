package catalog

import "github.com/Newton-School/gogo/core/models"

type Product struct {
	models.Base
	ID   int32
	Name string
}

func (*Product) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "Product", Fields: []models.Field{
		models.AutoField("id", models.WithStructField("ID")),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(120), models.UniqueValue),
	}}
}
