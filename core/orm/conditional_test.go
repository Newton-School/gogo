package orm

import (
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

func TestConditionalCopiesBranchesPredicatesAndTypedOutput(t *testing.T) {
	values := []string{"first"}
	result := JSONTextPath("payload", "amount")
	branch := When(Q("name__in", values), result)
	values[0] = "changed"
	result.Value.([]string)[0] = "changed"
	output := models.DecimalField("value", 30, 9)
	expression := Case(output, Value(nil), branch)
	branch.Condition.Value.([]string)[0] = "changed again"
	branch.Then.Value.([]string)[0] = "changed again"
	output.MaxDigits = 1
	if expression.Output.MaxDigits != 30 || expression.Branches[0].Condition.Value.([]string)[0] != "first" || expression.Branches[0].Then.Value.([]string)[0] != "amount" {
		t.Fatal("conditional keeps caller-owned values")
	}
	clone := cloneExpression(expression)
	expression.Branches[0].Then.Value.([]string)[0] = "later"
	if clone.Branches[0].Then.Value.([]string)[0] != "amount" {
		t.Fatal("query clone shares a conditional branch")
	}
}

func TestConditionalAggregateValidationVisitsConditionsAndBranches(t *testing.T) {
	output := models.BigIntegerField("value")
	for _, expression := range []db.Expression{
		Sum(Case(output, Value(0), When(Q("score__gt", 0), F("score")))),
		Case(output, Value(0), When(db.Predicate{Expression: exprPointer(Sum(F("score"))), Lookup: "gt", Value: 0}, Value(1))),
	} {
		if found, err := validateAggregateExpression(expression, false, 0); err != nil || !found {
			t.Fatal("valid conditional aggregate rejected", err)
		}
	}
	for _, expression := range []db.Expression{
		Case(output, Value(0), When(Q("score__gt", 0), Sum(F("score")))),
		Sum(Case(output, Value(0), When(Q("score__gt", 0), Sum(F("score"))))),
		Sum(Case(output, Value(0), When(Q("score__gt", Sum(F("score"))), Value(1)))),
		Sum(Case(output, Value(0), When(Q("score__in", []db.Expression{Sum(F("score"))}), Value(1)))),
	} {
		if _, err := validateAggregateExpression(expression, false, 0); err == nil {
			t.Fatal("ungrouped or nested aggregate hidden in conditional")
		}
	}
}

func exprPointer(expression db.Expression) *db.Expression { return &expression }

func TestConditionalRejectsCyclicAndExcessiveASTBeforeCompilation(t *testing.T) {
	output := models.BigIntegerField("value")
	args := []db.Expression{{Kind: "function", Name: "ABS"}}
	args[0].Args = args
	children := []db.Predicate{{}}
	children[0].Children = children
	cycle := db.Expression{Kind: "case", Output: &output, Args: []db.Expression{Value(0)}}
	cycle.Branches = []db.WhenBranch{{Condition: db.Predicate{Expression: &cycle, Value: 1}, Then: Value(1)}}
	filter := db.Predicate{}
	filtered := Sum(Value(1))
	filtered.Filter = &filter
	filter.Expression = &filtered
	rhs := db.Expression{Kind: "case", Output: &output, Args: []db.Expression{Value(0)}}
	rhs.Branches = []db.WhenBranch{{Condition: Q("x__in", []db.Expression{rhs}), Then: Value(1)}}
	rhs.Branches[0].Condition.Value.([]db.Expression)[0] = rhs
	deep := Value(1)
	for i := 0; i < 1000; i++ {
		deep = db.Expression{Kind: "function", Name: "ABS", Args: []db.Expression{deep}}
	}
	wide := db.Expression{Kind: "function", Name: "COALESCE", Args: make([]db.Expression, 8193)}
	for i := range wide.Args {
		wide.Args[i] = Value(i)
	}
	for _, expression := range []db.Expression{args[0], cycle, filtered, rhs, deep, wide} {
		copy := cloneExpression(expression)
		if copy.Kind != "invalid_tree" {
			t.Fatal("invalid AST did not fail closed", copy.Kind)
		}
		// Invalid sentinels reject before accessing even a dialect/backend.
		compiler := &sqlcompiler.Compiler{}
		if _, err := compiler.Expression(copy); err == nil || len(compiler.Args) != 0 {
			t.Fatal("invalid AST reached SQL compilation", err)
		}
	}
	predicate := clonePredicate(children[0])
	if predicate.Expression == nil || predicate.Expression.Kind != "invalid_tree" {
		t.Fatal("predicate cycle did not share expression bound")
	}
	if expression := Case(output, Value(0), When(children[0], Value(1))); expression.Branches[0].Condition.Expression.Kind != "invalid_tree" {
		t.Fatal("Case constructor did not preserve explicit rejection")
	}
}
