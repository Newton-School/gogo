package sqlcompiler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestUpdateUsesOldRowExpressionsAndBindsAssignmentsBeforeScope(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Counter", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value"), models.TextField("tenant")}}
	statement, args, err := Update(numberedDialect{}, schema, db.Predicate{Field: "tenant", Value: "authorized"}, []Assignment{
		{Field: "value", Expression: db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{{Kind: "field", Name: "value"}, {Kind: "value", Value: 7}}}},
		{Field: "tenant", Expression: db.Expression{Kind: "value", Value: "next' tenant"}},
	})
	if err != nil || statement != `UPDATE "tests_counter" SET "value" = ("value" + $1), "tenant" = $2 WHERE "tenant" = $3` || !reflect.DeepEqual(args, []any{7, "next' tenant", "authorized"}) {
		t.Fatal(statement, args, err)
	}
	if strings.Contains(statement, "next'") {
		t.Fatal("assignment data entered SQL")
	}
	statement, _, err = Update(numberedDialect{}, schema, db.Predicate{}, []Assignment{{Field: "id", Expression: db.Expression{Kind: "value", Value: 5}}})
	if err != nil || strings.Contains(statement, "WHERE") {
		t.Fatal("explicit all-row/primary-key update unavailable", err)
	}
}

func TestUpdateRejectsAggregateWindowAndCyclicExpressionsBeforeSQL(t *testing.T) {
	sum := db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "value"}}}
	output := models.BigIntegerField("result")
	for _, expression := range []db.Expression{
		sum,
		{Kind: "function", Name: "ROW_NUMBER"},
		{Kind: "case", Output: &output, Args: []db.Expression{{Kind: "value", Value: 0}}, Branches: []db.WhenBranch{{Condition: db.Predicate{Field: "value", Lookup: "in", Value: []db.Expression{sum}}, Then: db.Expression{Kind: "value", Value: 1}}}},
	} {
		if err := ValidateUpdateExpression(expression); err == nil {
			t.Fatal("aggregate/window hidden in update expression")
		}
	}
	cycle := []db.Expression{{Kind: "function", Name: "ABS"}}
	cycle[0].Args = cycle
	if err := ValidateUpdateExpression(cycle[0]); err == nil {
		t.Fatal("cyclic update expression accepted")
	}
	schema := models.Schema{AppLabel: "tests", Name: "Counter", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("value")}}
	for _, field := range []string{"unknown", "parent__value", "id;DELETE"} {
		if statement, args, err := Update(numberedDialect{}, schema, db.Predicate{}, []Assignment{{Field: field, Expression: db.Expression{Kind: "value", Value: 1}}}); err == nil || statement != "" || args != nil {
			t.Fatal("invalid update field escaped compilation", err)
		}
	}
}
