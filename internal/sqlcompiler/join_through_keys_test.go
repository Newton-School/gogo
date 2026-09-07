package sqlcompiler

import (
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestGroupedRelationJoinDeclaredScalarPrimaryKey(t *testing.T) {
	endpoint := models.Schema{AppLabel: "review", Name: "Endpoint", PrimaryKey: []string{"key"}, Fields: []models.Field{models.CharField("key", models.WithMaxLength(32))}}
	bridge := models.Schema{AppLabel: "review", Name: "Bridge", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("endpoint", models.Relation{Target: endpoint.Key(), OnDelete: models.Cascade})}}
	if err := endpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validateBridgeEndpoint(bridge, "endpoint", endpoint, "key"); err != nil {
		t.Fatal("valid scalar primary-key endpoint rejected", err)
	}
}

func TestGroupedRelationJoinUniqueConstraintCase(t *testing.T) {
	endpoint := models.Schema{AppLabel: "review", Name: "Endpoint", Fields: []models.Field{models.BigAutoField("id"), models.CharField("key", models.WithMaxLength(32))}, Constraints: []models.Constraint{{Name: "unique_key", Kind: "UNIQUE", Fields: []string{"key"}}}}
	bridge := models.Schema{AppLabel: "review", Name: "Bridge", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("endpoint", models.Relation{Target: endpoint.Key(), TargetFields: []string{"key"}, OnDelete: models.Cascade})}}
	if err := endpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validateBridgeEndpoint(bridge, "endpoint", endpoint, "key"); err != nil {
		t.Fatal("valid unique endpoint rejected", err)
	}
}
