package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type windowTestDialect struct{ updateDialect }

func (windowTestDialect) SupportsFeature(string) bool { return true }

type windowTestBackend struct{ *updateBackend }

func (windowTestBackend) Dialect() db.Dialect { return windowTestDialect{} }

func TestWindowQueryOwnsFunctionPartitionOrderAndFrame(t *testing.T) {
	b, base := updateFixture()
	base.store.Backend = windowTestBackend{b}
	spec := db.WindowSpec{PartitionBy: []db.Expression{F("value")}, OrderBy: []db.WindowOrder{{Expression: F("id"), NullsLast: true}}, Frame: &db.WindowFrame{Mode: db.WindowRows, Start: db.WindowBound{Kind: db.WindowPreceding, Offset: 1}, End: db.WindowBound{Kind: db.WindowCurrentRow}}}
	expression := Window(Sum(Add(F("value"), Value(5))), spec)
	query := base.Annotate(map[string]ResultExpression{"running": Typed(expression, models.BigIntegerField("out"))})
	before, args, err := query.SQLContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	spec.PartitionBy[0] = F("missing")
	spec.OrderBy[0].Expression = F("missing")
	spec.Frame.Start.Offset = 999
	expression.Args[0].Args[0].Args[1].Value = 999
	expression.Window.PartitionBy[0] = F("missing")
	expression.Window.OrderBy[0].Desc = true
	expression.Window.Frame.End.Kind = db.WindowUnboundedFollowing
	after, got, err := query.SQLContext(context.Background())
	if err != nil || before != after || !reflect.DeepEqual(args, got) || !reflect.DeepEqual(args, []any{5, int64(1)}) {
		t.Fatal(before, after, args, got, err)
	}
	if strings.Contains(after, "GROUP BY") {
		t.Fatal("aggregate-over-window collapsed model rows", after)
	}
	if len(base.selectAST.Aliases) != 0 || base.modelGrouping {
		t.Fatal("base query was mutated")
	}
	cyclic := db.Expression{Kind: "window", Args: []db.Expression{RowNumber()}}
	cyclic.Window = &db.WindowSpec{PartitionBy: []db.Expression{cyclic}}
	cyclic.Window.PartitionBy[0].Window = cyclic.Window
	if _, _, err := base.Annotate(map[string]ResultExpression{"cycle": Typed(cyclic, models.BigIntegerField("out"))}).SQLContext(context.Background()); err == nil {
		t.Fatal("cyclic specification accepted")
	}
	shared := Value(1)
	for range 30 {
		shared = Add(shared, shared)
	}
	if _, _, err := base.Annotate(map[string]ResultExpression{"wide": Typed(Window(FirstValue(shared), db.WindowSpec{}), models.BigIntegerField("out"))}).SQLContext(context.Background()); err == nil {
		t.Fatal("exponential shared window tree accepted")
	}
}

func TestWindowProviderCancellationAndWriteFailuresPrecedeExecution(t *testing.T) {
	b, base := updateFixture()
	expression := Window(RowNumber(), db.WindowSpec{})
	query := base.Annotate(map[string]ResultExpression{"position": Typed(expression, models.BigIntegerField("out"))})
	if _, _, err := query.SQLContext(context.Background()); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("unadvertised provider accepted window", err)
	}
	base.store.Backend = windowTestBackend{b}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := query.SQLContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { cancel(); return Q("id", 1), nil }).SQLContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, values := range []map[string]any{{"value": expression}, {"value": Add(F("value"), expression)}} {
		if _, err := base.Update(context.Background(), values); err == nil || b.begins != 0 {
			t.Fatal("window write reached transaction", err, b.begins)
		}
	}
	if _, err := base.Aggregate(context.Background(), map[string]ResultExpression{"bad": Typed(Sum(expression), models.BigIntegerField("out"))}); err == nil {
		t.Fatal("window terminal aggregate accepted")
	}
}
