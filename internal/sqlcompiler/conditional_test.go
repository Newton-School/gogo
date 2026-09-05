package sqlcompiler

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type conditionalDialect struct{ castDialect }

func (conditionalDialect) SupportsFeature(name string) bool { return name == "conditional_expressions" }

func TestConditionalSQLBranchOrderNullMetadataAndParameterRollback(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Case", Fields: []models.Field{models.BigIntegerField("score"), models.JSONField("payload")}}
	output := models.TextField("value")
	expression := db.Expression{Kind: "case", Output: &output, Args: []db.Expression{{Kind: "value", Value: "fallback"}}, Branches: []db.WhenBranch{
		{Condition: db.Predicate{Field: "score", Lookup: "gte", Value: 1}, Then: db.Expression{Kind: "value", Value: "first"}},
		{Condition: db.Predicate{Field: "score", Lookup: "gte", Value: 2}, Then: db.Expression{Kind: "value", Value: "second"}},
	}}
	compiler := Compiler{Dialect: conditionalDialect{}, Schema: schema}
	statement, err := compiler.Expression(expression)
	want := `CONVERT(CASE WHEN "score" >= $1 THEN CONVERT($2 AS text) WHEN "score" >= $3 THEN CONVERT($4 AS text) ELSE CONVERT($5 AS text) END AS text)`
	if err != nil || statement != want || !reflect.DeepEqual(compiler.Args, []any{1, "first", 2, "second", "fallback"}) {
		t.Fatal(statement, compiler.Args, err)
	}
	// Storage/cast capability alone never advertises conditional support.
	compiler = Compiler{Dialect: castDialect{}, Schema: schema, Args: []any{"prior"}}
	if _, err := compiler.Expression(expression); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 1 {
		t.Fatal("conditional capability ignored", err)
	}
	compiler.Dialect = conditionalDialect{}
	expression.Branches[1].Condition = db.Predicate{}
	if _, err := compiler.Expression(expression); err == nil || !reflect.DeepEqual(compiler.Args, []any{"prior"}) {
		t.Fatal("invalid branch left partial bind parameters", err)
	}
	// Empty Case is its typed fallback, not an invalid empty CASE statement.
	expression.Branches = nil
	statement, err = compiler.Expression(expression)
	if err != nil || statement != `CONVERT($2 AS text)` {
		t.Fatal(statement, err)
	}
	jsonOutput := models.JSONField("value")
	expression.Output = &jsonOutput
	expression.Args[0] = db.Expression{Kind: "field", Name: "payload"}
	compiler.Args = nil
	statement, err = compiler.Predicate(db.Predicate{Expression: &expression, Value: nil})
	if err != nil || statement != `CONVERT("payload" AS json) = $1` || !reflect.DeepEqual(compiler.Args, []any{"null"}) {
		t.Fatal("conditional output lost JSON null lookup semantics", statement, compiler.Args, err)
	}
}
