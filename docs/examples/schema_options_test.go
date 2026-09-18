package examples_test

import (
	"fmt"
	"testing"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func inventorySchema() models.Schema {
	// docs:begin schema-options
	schema := models.Schema{
		AppLabel: "catalog", Name: "Inventory", Table: "catalog_inventory",
		Label: "Inventory item", LabelPlural: "Inventory items",
		Ordering: []string{"sku", "id"},
		Fields: []models.Field{
			models.BigAutoField("id", models.WithStructField("ID")),
			models.CharField("sku", models.WithStructField("SKU"),
				models.WithMaxLength(40), models.Nullable),
			models.IntegerField("stock", models.WithStructField("Stock")),
			models.BooleanField("active", models.WithStructField("Active")),
		},
	}
	// docs:end schema-options
	return schema
}

func Example_constraints() {
	schema := inventorySchema()
	// docs:begin constraint-unique
	uniqueSKU := models.Constraint{
		Name: "inventory_sku_unique", Kind: "unique",
		Fields: []string{"sku"},
	}
	// docs:end constraint-unique
	// docs:begin constraint-check
	positiveStock := models.Constraint{
		Name: "inventory_stock_nonnegative", Kind: "check",
		Expression: "stock >= 0",
	}
	// docs:end constraint-check
	// docs:begin constraint-attach
	schema.Constraints = []models.Constraint{uniqueSKU, positiveStock}
	if err := schema.Validate(); err != nil {
		panic(err)
	}
	// Return schema from your model's Schema() method.
	// Validation checks the declaration, not existing database rows.
	// docs:end constraint-attach
	fmt.Println(len(schema.Constraints))
	// Output: 2
}

func Example_constraintCondition() {
	// docs:begin constraint-condition
	constraint := models.Constraint{
		Name: "inventory_active_sku_unique", Kind: "unique",
		Fields: []string{"sku"}, Condition: "active = true",
	}
	// docs:end constraint-condition
	schema := inventorySchema()
	schema.Constraints = []models.Constraint{constraint}
	fmt.Println(schema.Validate() == nil)
	// Output: true
}

func Example_constraintDeferred() {
	// docs:begin constraint-deferred
	constraint := models.Constraint{
		Name: "inventory_sku_deferred", Kind: "unique",
		Fields: []string{"sku"}, Deferrable: true,
	}
	// docs:end constraint-deferred
	schema := inventorySchema()
	schema.Constraints = []models.Constraint{constraint}
	fmt.Println(schema.Validate() == nil)
	// Output: true
}

func Example_constraintNulls() {
	// docs:begin constraint-nulls
	distinct := false
	constraint := models.Constraint{
		Name: "inventory_sku_with_null_unique", Kind: "unique",
		Fields: []string{"sku"}, NullsDistinct: &distinct,
	}
	// nil: backend default; &true: distinct NULLs; &false: equal NULLs.
	// docs:end constraint-nulls
	schema := inventorySchema()
	schema.Constraints = []models.Constraint{constraint}
	fmt.Println(schema.Validate() == nil)
	// Output: true
}

func Example_constraintMigration() {
	schema := inventorySchema()
	constraint := models.Constraint{Name: "inventory_sku_unique", Kind: "unique", Fields: []string{"sku"}}
	// docs:begin constraint-migration
	operation := migrations.AddConstraint(schema, constraint)
	migration := migrations.Migration{
		App: "catalog", Name: "0002_unique_sku",
		Dependencies: []string{"catalog.0001_initial"},
		Operations:   []migrations.Operation{operation},
	}
	// Register the migration before running manage.go migrate.
	// Constructing an operation does not execute SQL.
	// docs:end constraint-migration
	fmt.Println(migration.Key())
	// Output: catalog.0002_unique_sku
}

func Example_indexOptions() {
	schema := inventorySchema()
	// docs:begin index-options
	index := models.Index{
		Name: "inventory_active_sku_idx", Fields: []string{"sku"},
		Method: "btree", Condition: "active = true",
		Include: []string{"stock"},
	}
	schema.Indexes = []models.Index{index}
	if err := schema.Validate(); err != nil {
		panic(err)
	}
	// docs:end index-options
	fmt.Println(schema.Indexes[0].Name)
	// Output: inventory_active_sku_idx
}

func Example_uniqueIndexOptions() {
	schema := inventorySchema()
	// docs:begin index-unique
	distinct := false
	index := models.Index{
		Name: "inventory_sku_unique_idx", Fields: []string{"sku"},
		Unique: true, NullsDistinct: &distinct,
	}
	// docs:end index-unique
	schema.Indexes = []models.Index{index}
	fmt.Println(schema.Validate() == nil)
	// Output: true
}

func Example_concurrentIndexOptions() {
	schema := inventorySchema()
	// docs:begin index-concurrent
	index := models.Index{
		Name: "inventory_sku_online_idx", Fields: []string{"sku"}, Concurrent: true,
	}
	migration := migrations.Migration{
		App: "catalog", Name: "0002_online_index", NonAtomic: true,
		Dependencies: []string{"catalog.0001_initial"},
		Operations:   []migrations.Operation{migrations.AddIndex(schema, index)},
	}
	// docs:end index-concurrent
	fmt.Println(migration.NonAtomic)
	// Output: true
}

func TestConstraintExamplesRejectInvalidCombinations(t *testing.T) {
	for _, constraint := range []models.Constraint{
		{Name: "bad", Kind: "unique", Fields: []string{"sku"}, Condition: "active = true", Deferrable: true},
		{Name: "bad", Kind: "check", Expression: "stock >= 0", Deferrable: true},
		{Name: "bad", Kind: "unique", Fields: []string{"missing"}},
		{Name: "bad", Kind: "check", Expression: "stock >= 0; DROP TABLE catalog_inventory"},
	} {
		schema := inventorySchema()
		schema.Constraints = []models.Constraint{constraint}
		if schema.Validate() == nil {
			t.Fatal("invalid constraint accepted", constraint.Name)
		}
	}
}
