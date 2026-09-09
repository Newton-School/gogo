package catalog

import (
	"time"

	"github.com/Newton-School/gogo/core/models"
)

type Product struct {
	models.Base
	ID                             int64
	Name, Slug, Description, Price string
	Stock                          int64
	Published                      bool
	CreatedAt                      time.Time
}

func (*Product) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "Product", LabelPlural: "Products", Ordering: []string{"name"}, Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(120)),
		models.SlugField("slug", models.WithStructField("Slug"), models.WithMaxLength(120), models.UniqueValue),
		models.TextField("description", models.WithStructField("Description"), models.Optional),
		models.DecimalField("price", 12, 2, models.WithStructField("Price")),
		models.PositiveIntegerField("stock", models.WithStructField("Stock")),
		models.BooleanField("published", models.WithStructField("Published")),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), func(f *models.Field) { f.AutoNowAdd = true }),
	}}
}

type ProductNote struct {
	models.Base
	ID, ProductID int64
	Body          string
}

func (*ProductNote) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "ProductNote", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.ForeignKeyField("product", models.Relation{Target: "catalog.Product", OnDelete: models.Cascade}, models.WithStructField("ProductID")),
		models.TextField("body", models.WithStructField("Body")),
	}}
}

func Schemas() []models.Schema {
	return []models.Schema{(&Product{}).Schema(), (&ProductNote{}).Schema()}
}
func Factories() map[string]func() models.Model {
	return map[string]func() models.Model{
		"catalog.Product":     func() models.Model { return &Product{} },
		"catalog.ProductNote": func() models.Model { return &ProductNote{} },
	}
}
