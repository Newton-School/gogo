package orm

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestScalarAggregateBoundsSharedBranchesBeforeSemanticValidation(t *testing.T) {
	_, query := updateFixture()
	// Each level shares the prior subtrees. Semantic recursion before the
	// bounded snapshot would expand exponentially despite small input storage.
	expression := Value(1)
	for range 30 {
		expression = Add(expression, expression)
	}
	if _, err := query.Aggregate(context.Background(), map[string]ResultExpression{"total": Typed(Sum(expression), models.BigIntegerField("out"))}); err == nil {
		t.Fatal("unbounded shared aggregate expression accepted")
	}
}
