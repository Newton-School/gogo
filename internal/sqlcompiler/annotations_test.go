package sqlcompiler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestRowAliasesExpandScopedExpressionsInSelectFilterAndOrder(t *testing.T) {
	output := models.BigIntegerField("result", models.Nullable)
	schema := models.Schema{AppLabel: "tests", Name: "Source", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.BigIntegerField("target_id")}}
	target := models.Schema{AppLabel: "tests", Name: "Target", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score")}}
	projection := db.Projection{Alias: "computed", Output: &output, Expression: db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{{Kind: "field", Name: "target__score"}, {Kind: "value", Value: 11}}}}
	query := db.Select{Table: schema.DBTable(), Alias: "r", Fields: []string{"id"}, Projections: []db.Projection{projection}, Aliases: []db.Projection{projection}, Joins: []db.Join{{Path: "target", Alias: "j", Schema: target, ParentField: "target_id", TargetField: "id", Where: db.Predicate{Field: "tenant", Value: 22}}}, Where: db.Predicate{Children: []db.Predicate{{Field: "tenant", Value: 33}, {Field: "computed", Lookup: "gt", Value: 44}}}, Order: []db.Order{{Field: "computed", Desc: true}}}
	statement, args, err := Select(castDialect{}, schema, query)
	want := []any{11, 22, 33, 11, 44}
	if err != nil || !reflect.DeepEqual(args, want) || !strings.Contains(statement, `("j"."score" + $1) AS "computed"`) || !strings.Contains(statement, `ON "r"."target_id"="j"."id" AND ("j"."tenant" = $2)`) || !strings.Contains(statement, `(("j"."score" + $4)) > $5`) || !strings.Contains(statement, `ORDER BY "computed" DESC`) {
		t.Fatal(statement, args, err)
	}
	// Selection and definition are separate: an alias used only in filtering
	// stays resolved without adding a secret/unrequested column to VALUES.
	query.Projections = nil
	statement, _, err = Select(castDialect{}, schema, query)
	if err != nil || strings.Contains(statement, `AS "computed"`) {
		t.Fatal("unselected alias was emitted", statement, err)
	}
}

func TestRowAliasesRejectCollisionsAndRawCyclicExpressions(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Alias", Fields: []models.Field{models.BigAutoField("id")}}
	output := models.BigIntegerField("result")
	projection := db.Projection{Alias: "cycle", Output: &output, Expression: db.Expression{Kind: "field", Name: "cycle"}}
	query := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Projections: []db.Projection{projection}, Aliases: []db.Projection{projection}}
	if _, _, err := Select(castDialect{}, schema, query); err == nil {
		t.Fatal("cyclic alias compiled")
	}
	for _, alias := range []string{"id", "a__b", "invalid name"} {
		query.Aliases[0].Alias = alias
		if _, _, err := Select(castDialect{}, schema, query); err == nil {
			t.Fatal("invalid alias compiled", alias)
		}
	}
	query.Aliases[0] = projection
	query.Aliases = append(query.Aliases, projection)
	if _, _, err := Select(castDialect{}, schema, query); err == nil {
		t.Fatal("duplicate alias compiled")
	}
	query.Aliases = query.Aliases[:1]
	query.Aliases[0].Expression = db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "id"}}}
	if _, _, err := Select(castDialect{}, schema, query); err == nil {
		t.Fatal("ungrouped aggregate alias compiled")
	}
	cycle := []db.Expression{{Kind: "function", Name: "ABS"}}
	cycle[0].Args = cycle
	compiler := Compiler{Dialect: castDialect{}, Schema: schema}
	if _, err := compiler.Expression(cycle[0]); err == nil {
		t.Fatal("raw cyclic expression compiled")
	}
	predicates := []db.Predicate{{Connector: "AND"}}
	predicates[0].Children = predicates
	compiler = Compiler{Dialect: castDialect{}, Schema: schema}
	if _, err := compiler.Predicate(predicates[0]); err == nil {
		t.Fatal("raw cyclic predicate compiled")
	}
}
