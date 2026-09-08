package sqlcompiler

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type subqueryParameterNode struct {
	Left, Right *subqueryParameterNode
	Data        any
}

type subqueryParameterTripwire struct {
	Data    any
	private int
}

func (subqueryParameterTripwire) MarshalJSON() ([]byte, error) {
	panic("parameter scanner invoked a marshaler")
}

func (subqueryParameterTripwire) String() string {
	panic("parameter scanner invoked a Stringer")
}

func TestSubqueryParameterScanUsesGraphIdentityNotASTLeafBudget(t *testing.T) {
	leaf := &subqueryParameterNode{Data: "ordinary"}
	root := leaf
	for i := 0; i < 30; i++ {
		root = &subqueryParameterNode{Left: root, Right: root}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	slice := make([]any, 1)
	slice[0] = slice
	value := []any{root, root, cycle, slice, subqueryParameterTripwire{Data: "ordinary"}}
	walker := queryExpressionWalker{visit: func(*db.Expression, bool, bool) error {
		t.Fatal("ordinary data visited as expression")
		return nil
	}}
	if err := walker.parameter(reflect.ValueOf(value), false, 1); err != nil {
		t.Fatal(err)
	}
	if walker.nodes != 0 || len(walker.parameterSeen) != 34 {
		t.Fatal("ordinary graph expanded or consumed AST budget", walker.nodes, len(walker.parameterSeen))
	}
	// Parameter depth is not expression depth. Type cycles must not be
	// provisionally pruned before the final public interface field is seen.
	deep := leaf
	for i := 0; i < 10000; i++ {
		deep = &subqueryParameterNode{Left: deep}
	}
	if err := walker.parameter(reflect.ValueOf(deep), false, 1); err != nil || walker.nodes != 0 {
		t.Fatal("ordinary deep parameter rejected", err, walker.nodes)
	}
}

func TestSubqueryParameterScanFindsExpressionsInRecursiveTypesAndSliceViews(t *testing.T) {
	sentinel := errors.New("found expression")
	expression := db.Expression{Kind: "outer_ref", Name: "id"}
	leaf := &subqueryParameterNode{Data: expression}
	leaf.Left, leaf.Right = leaf, leaf
	for _, value := range []any{leaf, subqueryParameterTripwire{Data: leaf}, map[string]any{"nested": leaf}} {
		walker := queryExpressionWalker{visit: func(_ *db.Expression, inner, parameter bool) error {
			if inner || !parameter {
				t.Fatal("parameter placement lost", inner, parameter)
			}
			return sentinel
		}}
		if err := walker.parameter(reflect.ValueOf(value), false, 1); !errors.Is(err, sentinel) {
			t.Fatal("recursive public expression was pruned", err)
		}
	}
	values := []any{"ordinary", expression}
	walker := queryExpressionWalker{visit: func(*db.Expression, bool, bool) error { return sentinel }}
	if err := walker.parameter(reflect.ValueOf(values[:1]), false, 1); err != nil {
		t.Fatal(err)
	}
	if err := walker.parameter(reflect.ValueOf(values), false, 1); !errors.Is(err, sentinel) {
		t.Fatal("longer slice view skipped", err)
	}
}

func TestSubqueryParameterExpressionsStillShareASTBudget(t *testing.T) {
	for _, value := range []any{make([]db.Expression, 8193), make([]db.Predicate, 8193)} {
		walker := queryExpressionWalker{visit: func(*db.Expression, bool, bool) error { return nil }}
		if err := walker.parameter(reflect.ValueOf(value), false, 1); err == nil {
			t.Fatalf("expression work limit omitted for %T", value)
		}
	}
	// Large scalar slices (including membership parameters) are not ASTs.
	query := db.Select{Where: db.Predicate{Field: "id", Lookup: "in", Value: make([]int, 9000)}}
	if err := inspectQueryExpressions(query, func(*db.Expression, bool, bool) error { return nil }); err != nil {
		t.Fatal("ordinary membership acquired an expression-count limit", err)
	}
}

func compilerSubquery() (models.Schema, db.Expression) {
	schema := windowSchema()
	output, _ := SubqueryOutput(schema, "score")
	one := 1
	return schema, db.Expression{Kind: "subquery", Output: &output, Subquery: &db.Subquery{Schema: schema, Field: "score", Query: db.Select{
		Table: schema.DBTable(), Fields: []string{"score"}, Limit: &one,
		Where: db.Predicate{Field: "team", Value: db.Expression{Kind: "outer_ref", Name: "team"}},
		Order: []db.Order{{Field: "id", Desc: true}},
	}}}
}

func TestSubqueryCompilerSharesParametersAndAvoidsAliasCollisions(t *testing.T) {
	schema, expression := compilerSubquery()
	expression.Subquery.Query.Where.Children = []db.Predicate{{Field: "score", Value: 12}}
	// The outer root already owns the first generated name; the inner must not
	// shadow it, including when both queries use the same physical table.
	query := db.Select{Table: schema.DBTable(), Alias: "gogo_subquery_1", Fields: []string{"id"}, Where: db.Predicate{Field: "score", Value: 13}, Projections: []db.Projection{{Alias: "latest", Expression: expression, Output: expression.Output}}}
	statement, args, err := Select(windowDialect{}, schema, query)
	if err != nil || !strings.Contains(statement, `AS "gogo_subquery_2"`) || !reflect.DeepEqual(args, []any{12, 1, 13}) {
		t.Fatal(statement, args, err)
	}
}

func TestSubqueryCompilerRejectsRawShapesAndStandaloneOwners(t *testing.T) {
	for _, mode := range []string{"table", "output", "limit", "fields", "nested", "group", "lock", "alias", "scope_join", "window", "metadata", "lazy", "outside_ref", "parameter", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			schema, expression := compilerSubquery()
			query := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Projections: []db.Projection{{Alias: "latest", Expression: expression}}}
			dialect := windowDialect{}
			switch mode {
			case "table":
				expression.Subquery.Query.Table = "other"
			case "output":
				expression.Output.Null = false
			case "limit":
				expression.Subquery.Query.Limit = nil
			case "fields":
				expression.Subquery.Query.Fields = []string{"score", "id"}
			case "nested":
				expression.Subquery.Query.Where.Value = expression
			case "group":
				query.GroupBy = []string{"id"}
			case "lock":
				query.ForUpdate = true
			case "alias":
				expression.Subquery.Query.Alias = "caller_alias"
			case "scope_join":
				expression.Subquery.Query.Joins = []db.Join{{}}
			case "window":
				query.Projections = append(query.Projections, db.Projection{Alias: "number", Expression: windowExpression("ROW_NUMBER")})
			case "metadata":
				expression.Kind = "value"
				query.Projections[0].Expression = expression
			case "lazy":
				query.Projections[0].Expression = db.Expression{Kind: "orm_subquery"}
			case "outside_ref":
				query.Where = db.Predicate{Field: "id", Value: db.Expression{Kind: "outer_ref", Name: "id"}}
			case "parameter":
				query.Where = db.Predicate{Field: "id", Value: map[string]any{"expression": expression}}
			case "unsupported":
				dialect.disabled = "correlated_subqueries"
			}
			if _, args, err := Select(dialect, schema, query); err == nil || len(args) != 0 {
				t.Fatal("malformed SELECT accepted", args, err)
			}
		})
	}
	schema, expression := compilerSubquery()
	compiler := Compiler{Dialect: windowDialect{}, Schema: schema}
	if _, err := compiler.Expression(expression); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("standalone expression", err)
	}
	if _, err := compiler.Predicate(db.Predicate{Field: "id", Value: expression}); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("standalone DELETE predicate", err)
	}
	if _, _, err := Update(windowDialect{}, schema, db.Predicate{}, []Assignment{{Field: "score", Expression: expression}}); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("update expression", err)
	}
}

func TestSubqueryCompilerSharesInnerAndOuterTreeBudgets(t *testing.T) {
	schema, expression := compilerSubquery()
	leaf := db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{{Kind: "value", Value: 1}, {Kind: "value", Value: 2}}}
	for i := 0; i < 15; i++ {
		leaf = db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{leaf, leaf}}
	}
	expression.Subquery.Query.Where = db.Predicate{Field: "score", Value: leaf}
	query := db.Select{Table: schema.DBTable(), Fields: []string{"id"}, Projections: []db.Projection{{Alias: "latest", Expression: expression}}}
	if _, _, err := Select(windowDialect{}, schema, query); err == nil {
		t.Fatal("shared inner DAG exceeded budget")
	}
	_, expression = compilerSubquery()
	query.Projections = nil
	for i := 0; i < 65; i++ {
		query.Projections = append(query.Projections, db.Projection{Alias: "latest", Expression: expression})
	}
	if _, _, err := Select(windowDialect{}, schema, query); err == nil {
		t.Fatal("subquery count exceeded")
	}
}
