package documentation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type sqlRecorder struct{ statements []string }

func (r *sqlRecorder) Exec(_ context.Context, sql string, _ ...any) (db.Result, error) {
	r.statements = append(r.statements, sql)
	return nil, nil
}
func (*sqlRecorder) Query(context.Context, string, ...any) (db.Rows, error) {
	return nil, errors.New("unexpected query in SQL generation test")
}

func TestDocumentedConstraintSQL(t *testing.T) {
	// SQL-generation evidence only: this recorder never contacts PostgreSQL.
	schema := models.Schema{AppLabel: "catalog", Name: "Inventory", Fields: []models.Field{
		models.BigAutoField("id"), models.CharField("sku", models.Nullable),
		models.IntegerField("stock"), models.BooleanField("active"),
	}}
	distinct := false
	for _, test := range []struct {
		constraint models.Constraint
		want       string
	}{
		{models.Constraint{Name: "inventory_sku_unique", Kind: "unique", Fields: []string{"sku"}}, `UNIQUE ("sku")`},
		{models.Constraint{Name: "inventory_stock_nonnegative", Kind: "check", Expression: "stock >= 0"}, "CHECK (stock >= 0)"},
		{models.Constraint{Name: "inventory_active_sku_unique", Kind: "unique", Fields: []string{"sku"}, Condition: "active = true"}, "WHERE active = true"},
		{models.Constraint{Name: "inventory_sku_deferred", Kind: "unique", Fields: []string{"sku"}, Deferrable: true}, "DEFERRABLE INITIALLY DEFERRED"},
		{models.Constraint{Name: "inventory_sku_with_null_unique", Kind: "unique", Fields: []string{"sku"}, NullsDistinct: &distinct}, `UNIQUE NULLS NOT DISTINCT ("sku")`},
	} {
		t.Run(test.constraint.Name, func(t *testing.T) {
			recorder := &sqlRecorder{}
			if err := (postgres.SchemaEditor{}).AddConstraint(context.Background(), recorder, schema, test.constraint); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(recorder.statements, "\n"), test.want) {
				t.Fatalf("missing SQL clause %q", test.want)
			}
		})
	}
}
