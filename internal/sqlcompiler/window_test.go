package sqlcompiler

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type windowDialect struct {
	numberedDialect
	disabled string
}

func (d windowDialect) SupportsFeature(name string) bool { return name != d.disabled }

func windowExpression(name string, args ...db.Expression) db.Expression {
	return db.Expression{Kind: "window", Args: []db.Expression{{Kind: "function", Name: name, Args: args}}}
}
func windowSchema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "Score", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("team"), models.IntegerField("score")}}
}

func TestWindowCompilerBindsFunctionKeysFilterAndFrameInSQLOrder(t *testing.T) {
	schema := windowSchema()
	expression := windowExpression("SUM", db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{{Kind: "field", Name: "score"}, {Kind: "value", Value: 7}}})
	expression.Args[0].Filter = &db.Predicate{Field: "score", Lookup: "gt", Value: 2}
	expression.Window = &db.WindowSpec{PartitionBy: []db.Expression{{Kind: "value", Value: "group"}}, OrderBy: []db.WindowOrder{{Expression: db.Expression{Kind: "field", Name: "score"}, Desc: true, NullsLast: true}}, Frame: &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowPreceding, Offset: 2}, End: db.WindowBound{Kind: db.WindowFollowing, Offset: 3}, Exclusion: db.WindowExcludeTies}}
	query := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Projections: []db.Projection{{Alias: "running", Expression: expression}}, Where: db.Predicate{Field: "team", Value: 1}}
	statement, args, err := Select(windowDialect{}, schema, query)
	if err != nil || !reflect.DeepEqual(args, []any{7, 2, "group", int64(2), int64(3), 1}) {
		t.Fatal(statement, args, err)
	}
	want := `SUM(("score" + $1)) FILTER (WHERE "score" > $2) OVER (PARTITION BY $3 ORDER BY "score" DESC NULLS LAST ROWS BETWEEN $4 PRECEDING AND $5 FOLLOWING EXCLUDE TIES)`
	if !strings.Contains(statement, want) || !strings.Contains(statement, `WHERE "team" = $6`) {
		t.Fatal(statement)
	}
}

func TestWindowCompilerRejectsUnsupportedCapabilityWithoutRetainingArguments(t *testing.T) {
	expression := windowExpression("SUM", db.Expression{Kind: "value", Value: 10})
	expression.Window = &db.WindowSpec{Frame: &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowUnboundedPreceding}, End: db.WindowBound{Kind: db.WindowCurrentRow}, Exclusion: db.WindowExcludeGroup}}
	for _, disabled := range []string{"window", "window_rows_frame", "window_frame_exclusion"} {
		compiler := Compiler{Dialect: windowDialect{disabled: disabled}, Schema: windowSchema(), Args: []any{"prior"}}
		if _, err := compiler.Expression(expression); !db.IsCode(err, db.UnsupportedFeature) || !reflect.DeepEqual(compiler.Args, []any{"prior"}) {
			t.Fatal(disabled, compiler.Args, err)
		}
	}
	expression.Window.Frame.Mode = db.WindowRange
	compiler := Compiler{Dialect: windowDialect{disabled: "window_range_frame"}, Schema: windowSchema()}
	if _, err := compiler.Expression(expression); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 0 {
		t.Fatal(err)
	}
}

func TestWindowCompilerValidatesEveryFrameAndFunctionShape(t *testing.T) {
	base := db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowUnboundedPreceding}, End: db.WindowBound{Kind: db.WindowCurrentRow}}
	frames := []db.WindowFrame{base, base, base, base, base, base, base, base}
	frames[0].Mode = "raw SQL"
	frames[1].Start = db.WindowBound{Kind: db.WindowUnboundedFollowing}
	frames[2].End = db.WindowBound{Kind: db.WindowUnboundedPreceding}
	frames[3].Start = db.WindowBound{Kind: db.WindowFollowing, Offset: 1}
	frames[4].Start.Offset = 1
	frames[5].Start = db.WindowBound{Kind: db.WindowPreceding, Offset: -1}
	frames[6].Exclusion = "raw SQL"
	frames[7].Mode = db.WindowRange
	frames[7].Start = db.WindowBound{Kind: db.WindowPreceding, Offset: 0}
	for _, frame := range frames {
		expression := windowExpression("ROW_NUMBER")
		expression.Window = &db.WindowSpec{Frame: &frame}
		compiler := Compiler{Dialect: windowDialect{}, Schema: windowSchema()}
		if _, err := compiler.Expression(expression); err == nil || len(compiler.Args) != 0 {
			t.Fatal(frame, compiler.Args, err)
		}
	}
	for _, expression := range []db.Expression{
		windowExpression("ABS", db.Expression{Kind: "value", Value: 1}), windowExpression("ROW_NUMBER", db.Expression{Kind: "value", Value: 1}),
		windowExpression("LAG"), windowExpression("NTILE", db.Expression{Kind: "value", Value: 0}),
		windowExpression("NTH_VALUE", db.Expression{Kind: "field", Name: "score"}, db.Expression{Kind: "value", Value: int64(1) << 40}),
		windowExpression("LAG", db.Expression{Kind: "field", Name: "score"}, db.Expression{Kind: "field", Name: "id"}),
		windowExpression("LAG", windowExpression("ROW_NUMBER")),
		windowExpression("SUM", db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}),
	} {
		compiler := Compiler{Dialect: windowDialect{}, Schema: windowSchema()}
		if _, err := compiler.Expression(expression); err == nil || len(compiler.Args) != 0 {
			t.Fatal(expression, compiler.Args, err)
		}
	}
}

func TestWindowPlacementResolvesAliasesPredicatesAndNestedDependencies(t *testing.T) {
	schema := windowSchema()
	output := models.BigIntegerField("out")
	rank := windowExpression("ROW_NUMBER")
	base := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Aliases: []db.Projection{{Alias: "position", Expression: rank, Output: &output}, {Alias: "indirect", Expression: db.Expression{Kind: "field", Name: "position"}, Output: &output}}}
	for _, where := range []db.Predicate{
		{Field: "position", Value: 1}, {Field: "indirect", Value: 1},
		{Field: "score", Lookup: "in", Value: []db.Expression{{Kind: "field", Name: "indirect"}}},
		{Expression: &rank, Value: 1}, {Children: []db.Predicate{{Field: "indirect", Value: 1}}},
	} {
		query := base
		query.Where = where
		if _, args, err := Select(windowDialect{}, schema, query); !db.IsCode(err, db.UnsupportedFeature) || args != nil {
			t.Fatal(where, args, err)
		}
	}
	for _, mode := range []string{"group", "lock", "having", "nested", "aggregate", "cycle", "capability"} {
		query := base
		query.Aliases = append([]db.Projection(nil), base.Aliases...)
		dialect := windowDialect{}
		switch mode {
		case "group":
			query.GroupBy = []string{"id"}
		case "lock":
			query.ForUpdate = true
		case "having":
			query.Having = db.Predicate{Field: "indirect", Value: 1}
		case "nested":
			e := windowExpression("LAG", db.Expression{Kind: "field", Name: "indirect"})
			query.Projections = []db.Projection{{Alias: "nested", Expression: e}}
		case "aggregate":
			query.Projections = []db.Projection{{Alias: "sum", Expression: db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}}}
		case "cycle":
			query.Aliases[0].Expression = db.Expression{Kind: "field", Name: "indirect"}
		case "capability":
			dialect.disabled = "window"
		}
		if _, args, err := Select(dialect, schema, query); err == nil || args != nil {
			t.Fatal(mode, args, err)
		}
	}
	// A scalar alias expands inside OVER; an output alias is not emitted as
	// a column in PARTITION BY or the window's own ORDER BY.
	base.Aliases = []db.Projection{{Alias: "scalar", Expression: db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{{Kind: "field", Name: "score"}, {Kind: "value", Value: 1}}}, Output: &output}}
	rank.Window = &db.WindowSpec{OrderBy: []db.WindowOrder{{Expression: db.Expression{Kind: "field", Name: "scalar"}}}}
	base.Projections = []db.Projection{{Alias: "position", Expression: rank}}
	statement, args, err := Select(windowDialect{}, schema, base)
	if err != nil || !strings.Contains(statement, `OVER (ORDER BY (("score" + $1)) ASC)`) || !reflect.DeepEqual(args, []any{1}) {
		t.Fatal(statement, args, err)
	}
}

func TestWindowRawConnectorTreesShareOneWorkBudget(t *testing.T) {
	shared := db.Expression{Kind: "value", Value: 1}
	for range 30 {
		shared = db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{shared, shared}}
	}
	schema := windowSchema()
	output := models.BigIntegerField("out")
	for _, mode := range []string{"argument", "partition", "order", "outer_alias"} {
		expression := windowExpression("ROW_NUMBER")
		switch mode {
		case "argument":
			expression = windowExpression("FIRST_VALUE", shared)
		case "partition":
			expression.Window = &db.WindowSpec{PartitionBy: []db.Expression{shared}}
		case "order":
			expression.Window = &db.WindowSpec{OrderBy: []db.WindowOrder{{Expression: shared}}}
		case "outer_alias":
			expression = shared
		}
		query := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Aliases: []db.Projection{{Alias: "bounded", Expression: expression, Output: &output}}}
		if _, args, err := Select(windowDialect{}, schema, query); err == nil || args != nil {
			t.Fatal(mode, args, err)
		}
	}
	cyclic := windowExpression("ROW_NUMBER")
	cyclic.Window = &db.WindowSpec{PartitionBy: []db.Expression{cyclic}}
	cyclic.Window.PartitionBy[0].Window = cyclic.Window
	compiler := Compiler{Dialect: windowDialect{}, Schema: schema}
	if _, err := compiler.Expression(cyclic); err == nil || len(compiler.Args) != 0 {
		t.Fatal("cyclic raw window accepted", err)
	}
	if _, err := compiler.Predicate(db.Predicate{Expression: &cyclic, Value: 1}); err == nil {
		t.Fatal("standalone command predicate accepted a window")
	}
	// Separate join scopes share the same query work budget, and large raw
	// projection inventories are visited without allocating a second inventory.
	query := db.Select{Table: schema.DBTable(), Alias: "root", Fields: []string{"id"}}
	conditions := make([]db.Predicate, 100)
	for i := range conditions {
		conditions[i] = db.Predicate{Field: "team", Value: 1}
	}
	for i := range 48 {
		query.Joins = append(query.Joins, db.Join{Path: fmt.Sprintf("related%d", i), Alias: fmt.Sprintf("joined%d", i), Schema: schema, ParentField: "id", TargetField: "id", Where: db.Predicate{Children: conditions}})
	}
	if _, args, err := Select(windowDialect{}, schema, query); err == nil || !strings.Contains(err.Error(), "work bound") || args != nil {
		t.Fatal("independent join budgets", args, err)
	}
	query.Joins = nil
	query.Fields = make([]string, 10000)
	for i := range query.Fields {
		query.Fields[i] = "id"
	}
	if _, args, err := Select(windowDialect{}, schema, query); err == nil || !strings.Contains(err.Error(), "work bound") || args != nil {
		t.Fatal("unbounded inventory", args, err)
	}
}

func TestWindowRejectsAliasesInJoinEndpoints(t *testing.T) {
	schema := windowSchema()
	output := models.BigIntegerField("out")
	for _, name := range []string{"position", "indirect"} {
		query := db.Select{Table: schema.DBTable(), Alias: "root", Fields: []string{"id"},
			Aliases: []db.Projection{{Alias: "position", Output: &output, Expression: windowExpression("ROW_NUMBER")}, {Alias: "indirect", Output: &output, Expression: db.Expression{Kind: "field", Name: "position"}}},
			Joins:   []db.Join{{Path: "related", Alias: "joined", Schema: schema, ParentField: name, TargetField: "id"}},
		}
		statement, args, err := Select(windowDialect{}, schema, query)
		if !db.IsCode(err, db.UnsupportedFeature) || statement != "" || args != nil {
			t.Fatal("window escaped into JOIN ON", name, statement, args, err)
		}
	}
}
