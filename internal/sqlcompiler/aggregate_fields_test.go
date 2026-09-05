package sqlcompiler

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestAggregateFieldReferencesFollowOnlyAggregateDependenciesAndBounds(t *testing.T) {
	field := func(name string) db.Expression { return db.Expression{Kind: "field", Name: name} }
	aliases := []db.Projection{
		{Alias: "row", Expression: field("children__score")},
		{Alias: "unselected", Expression: field("other__secret")},
		{Alias: "total", Expression: db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{field("row")}, Filter: &db.Predicate{Field: "children__active", Value: true}}},
		{Alias: "conditional", Expression: db.Expression{Kind: "function", Name: "COUNT", Args: []db.Expression{{Kind: "case", Branches: []db.WhenBranch{{Condition: db.Predicate{Field: "children__score", Lookup: "in", Value: []db.Expression{field("notes__value")}}, Then: field("children__id")}}}}}},
	}
	fields, err := AggregateFieldReferences(models.Schema{}, nil, aliases)
	want := []string{"children__active", "children__id", "children__score", "notes__value"}
	if err != nil || !reflect.DeepEqual(fields, want) {
		t.Fatal("aggregate dependency paths", fields, err)
	}
	loop := []db.Projection{{Alias: "loop", Expression: field("loop__n")}}
	if _, err := AggregateFieldReferences(models.Schema{}, nil, loop); err == nil {
		t.Fatal("cyclic alias traversal accepted")
	}
	wide := []db.Projection{{Expression: db.Expression{Kind: "function", Name: "SUM", Args: make([]db.Expression, 8193)}}}
	if _, err := AggregateFieldReferences(models.Schema{}, wide, nil); err == nil {
		t.Fatal("unbounded aggregate relation traversal accepted")
	}
}
