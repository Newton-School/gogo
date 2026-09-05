package sqlcompiler

import (
	"testing"

	"github.com/Newton-School/gogo/core/db"
)

func TestContainsAggregateVisitsConditionsFiltersAndBoundedTrees(t *testing.T) {
	sum := db.Expression{Kind: "function", Name: "SUM", Args: []db.Expression{{Kind: "field", Name: "score"}}}
	for _, expression := range []db.Expression{
		sum,
		{Kind: "function", Name: "COALESCE", Args: []db.Expression{sum, {Kind: "value", Value: 0}}},
		{Kind: "case", Branches: []db.WhenBranch{{Condition: db.Predicate{Field: "score", Lookup: "in", Value: []db.Expression{sum}}}}},
		{Kind: "function", Name: "ABS", Filter: &db.Predicate{Expression: &sum}},
	} {
		if found, err := ContainsAggregate(expression); err != nil || !found {
			t.Fatal("hidden aggregate not detected", err)
		}
	}
	if found, err := ContainsAggregate(db.Expression{Kind: "field", Name: "score"}); found || err != nil {
		t.Fatal("row field became aggregate", err)
	}
	args := []db.Expression{{Kind: "function", Name: "ABS"}}
	args[0].Args = args
	predicate := db.Predicate{}
	expression := db.Expression{Kind: "case", Branches: []db.WhenBranch{{Condition: predicate}}}
	expression.Branches[0].Condition.Expression = &expression
	for _, invalid := range []db.Expression{args[0], expression, {Kind: "function", Name: "ABS", Args: make([]db.Expression, 8193)}, {Kind: "invalid_tree"}} {
		if _, err := ContainsAggregate(invalid); err == nil {
			t.Fatal("cyclic/overlarge aggregate detection accepted")
		}
	}
}
