// Package contenttypes supplies stable model identities and scoped generic
// object references. Synchronization is explicit, normally after migrate; no
// database access or table creation happens at package initialization.
package contenttypes

import (
	"strings"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

const IdentityConstraint = "gogo_content_types_identity"

type ContentType struct {
	models.Base
	ID                  int64
	AppLabel, ModelName string
	SchemaVersion       int64
	Active              bool
}

func (*ContentType) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_contenttypes", Name: "ContentType", Table: "gogo_content_types", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("app_label", models.WithStructField("AppLabel"), models.WithMaxLength(128)),
		models.CharField("model_name", models.WithStructField("ModelName"), models.WithMaxLength(128)),
		models.PositiveBigIntegerField("schema_version", models.WithStructField("SchemaVersion"), models.WithDefault(int64(1))),
		models.BooleanField("active", models.WithStructField("Active"), models.WithDefault(true)),
	}, Constraints: []models.Constraint{{Name: IdentityConstraint, Kind: "unique", Fields: []string{"app_label", "model_name"}}}, Ordering: []string{"app_label", "model_name"}}
}

func (c *ContentType) NaturalKey() [2]string { return [2]string{c.AppLabel, c.ModelName} }

func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_contenttypes", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel((&ContentType{}).Schema())}}}
}

func identity(schema models.Schema) string {
	return schema.AppLabel + "." + strings.ToLower(schema.Name)
}
