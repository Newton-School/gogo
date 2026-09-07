package migrations

import (
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestDetectSelfTargetRenameDoesNotAlterConstraint(t *testing.T) {
	before := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{
		models.BigAutoField("id"),
		models.CharField("slug", models.UniqueValue),
		models.ForeignKeyField("parent", models.Relation{Target: "tests.Node", TargetFields: []string{"slug"}}, models.Nullable),
	}}
	after := before.Clone()
	after.Fields[1].Name = "heading"
	after.Fields[2].Relation.TargetFields[0] = "heading"
	operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{Renames: map[string]string{"tests.Node.slug": "heading"}})
	if err != nil || len(operations) != 1 || operations[0].Kind != "rename_field" {
		t.Fatalf("pure logical rename generated a redundant storage alteration: %#v %v", operations, err)
	}
}
