package sqlcompiler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type groupedDialect struct{ conditionalDialect }

func (groupedDialect) SupportsFeature(string) bool { return true }

func groupedFixture() (models.Schema, db.Select) {
	schema := models.Schema{AppLabel: "tests", Name: "Groups", Fields: []models.Field{models.BigAutoField("id"), models.TextField("category"), models.IntegerField("score")}}
	output := models.BigIntegerField("out", models.Nullable)
	projection := db.Projection{Alias: "total", Expression: db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}, Output: &output}
	return schema, db.Select{Table: schema.DBTable(), Fields: []string{"category"}, GroupBy: []string{"category"}, Aliases: []db.Projection{projection}, Projections: []db.Projection{projection}, Where: db.Predicate{Field: "score", Lookup: "gt", Value: 2}, Having: db.Predicate{Field: "total", Lookup: "gt", Value: 10}, Order: []db.Order{{Field: "total", Desc: true}}}
}

func TestGroupedSQLValidatesPreGroupRowsAndPostGroupAliases(t *testing.T) {
	schema, query := groupedFixture()
	statement, args, err := Select(groupedDialect{}, schema, query)
	if err != nil || !reflect.DeepEqual(args, []any{2, 10}) || !strings.Contains(statement, `WHERE "score" > $1 GROUP BY "category" HAVING (SUM("score")) > $2 ORDER BY "total" DESC`) {
		t.Fatal(statement, args, err)
	}
	query.Projections = nil
	statement, _, err = Select(groupedDialect{}, schema, query)
	if err != nil || strings.Contains(statement, `AS "total"`) {
		t.Fatal("unselected aggregate alias lost HAVING binding", statement, err)
	}
	if _, _, err := Select(castDialect{}, schema, query); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("unadvertised grouping accepted", err)
	}
}

func TestGroupedSQLRejectsUngroupedNestedAndCyclicTrees(t *testing.T) {
	for _, mode := range []string{"projection", "having", "order", "where_alias", "membership_aggregate", "nested", "cycle", "duplicate", "alias_key", "lock", "window"} {
		schema, query := groupedFixture()
		sum := query.Aliases[0].Expression
		switch mode {
		case "projection":
			query.Fields = []string{"score"}
		case "having":
			query.Having = db.Predicate{Field: "score", Value: 1}
		case "order":
			query.Order = []db.Order{{Field: "score"}}
		case "where_alias":
			query.Where = db.Predicate{Field: "total", Value: 1}
		case "membership_aggregate":
			query.Where = db.Predicate{Field: "score", Lookup: "in", Value: []db.Expression{sum}}
		case "nested":
			query.Aliases[0].Expression.Args = []db.Expression{sum}
		case "cycle":
			query.Aliases[0].Expression = db.Expression{Kind: "field", Name: "total"}
		case "duplicate":
			query.GroupBy = []string{"category", "category"}
		case "alias_key":
			query.GroupBy = []string{"total"}
		case "lock":
			query.ForUpdate = true
		case "window":
			query.Aliases[0].Expression = db.Expression{Kind: "function", Name: "ROW_NUMBER"}
		}
		if statement, _, err := Select(groupedDialect{}, schema, query); err == nil || statement != "" {
			t.Fatal("invalid group compiled", mode, statement, err)
		}
	}
}

func TestGroupedTransformedAliasesCannotHideAggregateDependencies(t *testing.T) {
	for _, mode := range []string{"where", "membership", "nested", "cycle"} {
		schema, query := groupedFixture()
		output := models.JSONField("out")
		stats := db.Projection{Alias: "stats", Output: &output, Expression: db.Expression{Kind: "function", Name: "JSON_BUILD_OBJECT", Args: []db.Expression{{Kind: "value", Value: "n"}, query.Aliases[0].Expression}}}
		query.Aliases = append(query.Aliases, stats)
		switch mode {
		case "where":
			query.Where = db.Predicate{Field: "stats__n", Lookup: "gt", Value: 1}
		case "membership":
			query.Where = db.Predicate{Field: "score", Lookup: "in", Value: []db.Expression{{Kind: "field", Name: "stats__n"}}}
		case "nested":
			query.Projections[0].Expression.Args = []db.Expression{{Kind: "field", Name: "stats__n"}}
		case "cycle":
			query.Aliases[1].Expression = db.Expression{Kind: "field", Name: "stats__n"}
		}
		if statement, _, err := Select(groupedDialect{}, schema, query); err == nil || statement != "" {
			t.Fatal("transformed alias hid aggregate dependency", mode, statement, err)
		}
	}
}
