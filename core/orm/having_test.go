package orm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestAggregateFiltersRouteBeforeTrustedScopeWithoutMutatingQuery(t *testing.T) {
	b, base := updateFixture()
	base.store.Backend = decodingBackend{Backend: b, dialect: modelGroupingDialect{}}
	query := base.Annotate(map[string]ResultExpression{"total": Typed(Sum(F("value")), models.BigIntegerField("out"))}).Filter(Q("value__gt", 2), Q("total__gt", 3)).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return Q("value__lt", 100), nil })
	statement, args, err := query.SQL()
	if err != nil || len(args) != 3 || args[0] != 2 || args[1] != 100 || args[2] != 3 {
		t.Fatal("application rows, scope and groups lost SQL argument order", statement, args, err)
	}
	whereAt, groupAt, havingAt := strings.Index(statement, "WHERE"), strings.Index(statement, "GROUP BY"), strings.Index(statement, "HAVING")
	if whereAt < 0 || groupAt < whereAt || havingAt < groupAt || strings.Contains(statement[whereAt:groupAt], "SUM(") || !strings.Contains(statement[havingAt:], "SUM(") {
		t.Fatal("aggregate not separated from trusted row scope", statement)
	}
	if query.prepared || len(query.selectAST.Having.Children) != 0 {
		t.Fatal("terminal routing mutated query")
	}
	if second, _, err := query.SQL(); err != nil || second != statement {
		t.Fatal("repeated routing appended duplicate conditions", err)
	}
	invalidScope := query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return Q("total__gt", 0), nil })
	if _, _, err := invalidScope.SQL(); err == nil {
		t.Fatal("trusted aggregate scope was silently moved after grouping")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := query.SQLContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("routing ignored cancellation", err)
	}
}
