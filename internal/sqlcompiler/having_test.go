package sqlcompiler

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestSplitAggregateFiltersPreservesBooleanTreesAndInput(t *testing.T) {
	row, group := db.Predicate{Field: "row", Value: true}, db.Predicate{Field: "count", Value: true}
	aliases := []db.Projection{{Alias: "count", Expression: db.Expression{Kind: "function", Name: "COUNT", Args: []db.Expression{{Kind: "field", Name: "*"}}}}}
	trees := []db.Predicate{row, group}
	for _, connector := range []string{"AND", "OR", "XOR"} {
		for _, negated := range []bool{false, true} {
			trees = append(trees, db.Predicate{Connector: connector, Negated: negated, Children: []db.Predicate{row, group}})
		}
	}
	initial := append([]db.Predicate(nil), trees...)
	for _, child := range initial {
		for _, connector := range []string{"AND", "OR", "XOR"} {
			for _, negated := range []bool{false, true} {
				trees = append(trees, db.Predicate{Connector: connector, Negated: negated, Children: []db.Predicate{child, row}})
			}
		}
	}
	for _, predicate := range trees {
		where, having, err := SplitAggregateFilters(models.Schema{}, predicate, aliases)
		if err != nil {
			t.Fatal(err)
		}
		for _, rowValue := range []bool{false, true} {
			for _, groupValue := range []bool{false, true} {
				values := map[string]bool{"row": rowValue, "count": groupValue}
				if evaluateBooleanPredicate(predicate, values) != (evaluateBooleanPredicate(where, values) && evaluateBooleanPredicate(having, values)) {
					t.Fatal("routing changed boolean meaning", predicate, where, having, values)
				}
			}
		}
	}
	and := db.Predicate{Connector: "AND", Children: []db.Predicate{row, group}}
	where, having, err := SplitAggregateFilters(models.Schema{}, and, aliases)
	if err != nil || len(where.Children) != 1 || where.Children[0].Field != "row" || len(having.Children) != 1 || having.Children[0].Field != "count" {
		t.Fatal("conjunctive row predicate crossed grouping", where, having, err)
	}
	where.Children[0].Field = "changed"
	if and.Children[0].Field != "row" {
		t.Fatal("split changed source children")
	}
	where, having, err = SplitAggregateFilters(models.Schema{}, row, aliases)
	if err != nil || !reflect.DeepEqual(where, row) || !reflect.DeepEqual(having, db.Predicate{}) {
		t.Fatal("row-only tree was changed", err)
	}
}

func evaluateBooleanPredicate(p db.Predicate, values map[string]bool) bool {
	value := true
	if len(p.Children) == 0 {
		if p.Field == "" {
			return true
		}
		value = values[p.Field]
	} else {
		value = evaluateBooleanPredicate(p.Children[0], values)
		for _, child := range p.Children[1:] {
			other := evaluateBooleanPredicate(child, values)
			switch p.Connector {
			case "OR":
				value = value || other
			case "XOR":
				value = value != other
			default:
				value = value && other
			}
		}
	}
	return value != p.Negated
}

func TestSplitAggregateFiltersResolvesTransformedAliasesAndExpressionOperands(t *testing.T) {
	sum := db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}
	aliases := []db.Projection{
		{Alias: "total", Expression: sum},
		{Alias: "stats", Expression: db.Expression{Kind: "function", Name: "JSON_BUILD_OBJECT", Args: []db.Expression{{Kind: "value", Value: "n"}, {Kind: "field", Name: "total"}}}},
		{Alias: "derived", Expression: db.Expression{Kind: "field", Name: "stats__n"}},
	}
	for _, predicate := range []db.Predicate{
		{Field: "stats__n", Lookup: "gt", Value: 1},
		{Field: "derived", Lookup: "gt", Value: 1},
		{Expression: &sum, Lookup: "gt", Value: 1},
		{Field: "score", Lookup: "exact", Value: db.Expression{Kind: "field", Name: "total"}},
		{Field: "score", Lookup: "in", Value: []db.Expression{{Kind: "field", Name: "stats__n"}}},
		{Field: "score", Lookup: "range", Value: []any{1, db.Expression{Kind: "field", Name: "total"}}},
	} {
		where, having, err := SplitAggregateFilters(models.Schema{}, predicate, aliases)
		if err != nil || !reflect.DeepEqual(where, db.Predicate{}) || !reflect.DeepEqual(having, predicate) {
			t.Fatal("aggregate operand remained in row filter", predicate, err)
		}
	}
	aliases = append(aliases, db.Projection{Alias: "loop", Expression: db.Expression{Kind: "field", Name: "loop__n"}})
	for _, predicate := range []db.Predicate{{Field: "loop"}, {Field: "score", Lookup: "in", Value: make([]int, 8193)}, {Connector: "AND", Children: make([]db.Predicate, 8193)}} {
		if _, _, err := SplitAggregateFilters(models.Schema{}, predicate, aliases); err == nil {
			t.Fatal("cyclic/oversized routing tree accepted")
		}
	}
}

func TestAggregateRoutingPreservesExactStoredFieldBeforeAliasPath(t *testing.T) {
	schema := models.Schema{Fields: []models.Field{models.IntegerField("stats__n")}}
	aliases := []db.Projection{{Alias: "stats", Expression: db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}}}
	predicate := db.Predicate{Field: "stats__n", Lookup: "gt", Value: 1}
	where, having, err := SplitAggregateFilters(schema, predicate, aliases)
	if err != nil || !reflect.DeepEqual(where, predicate) || !reflect.DeepEqual(having, db.Predicate{}) {
		t.Fatal("exact stored field was mistaken for aggregate alias path", where, having, err)
	}
}
