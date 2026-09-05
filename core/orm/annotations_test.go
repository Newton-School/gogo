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

type cancelCompileDialect struct {
	db.Dialect
	cancel context.CancelFunc
}

func (d cancelCompileDialect) QuoteIdentifier(name string) (string, error) {
	d.cancel()
	return d.Dialect.QuoteIdentifier(name)
}

func TestAnnotationsCheckCancellationAfterDialectCompilation(t *testing.T) {
	for _, mode := range []string{"sql", "all", "values"} {
		_, query := updateFixture()
		ctx, cancel := context.WithCancel(context.Background())
		query.store.Backend = decodingBackend{dialect: cancelCompileDialect{Dialect: updateDialect{}, cancel: cancel}}
		query = query.Annotate(map[string]ResultExpression{"computed": Typed(F("value"), models.BigIntegerField("out"))})
		var err error
		switch mode {
		case "sql":
			_, _, err = query.SQLContext(ctx)
		case "all":
			_, err = query.All(ctx)
		case "values":
			_, err = query.Values(ctx)
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("dialect canceled without failing terminal operation", mode, err)
		}
	}
}

func TestAnnotationsSnapshotExpressionsMetadataAndIndependentAliases(t *testing.T) {
	_, base := updateFixture()
	expression := Add(F("value"), Value(2))
	output := models.BigIntegerField("output")
	outputs := map[string]ResultExpression{"doubled": Typed(expression, output)}
	query := base.Annotate(outputs).Filter(Q("doubled__gt", 5)).OrderBy("doubled")
	before, args, err := query.SQL()
	if err != nil || !reflect.DeepEqual(args, []any{2, 2, 5}) {
		t.Fatal(before, args, err)
	}
	expression.Args[1].Value = 99
	outputs["doubled"] = Typed(Value(100), models.TextField("other"))
	queryCopy := query.Filter(Q("id", 7))
	queryCopy.selectAST.Aliases[0].Output.Kind = models.Text
	queryCopy.selectAST.Aliases[0].Expression.Args[1].Value = 9
	queryCopy.selectAST.Projections[0].Output.Kind = models.JSON
	after, got, err := query.SQL()
	if err != nil || before != after || !reflect.DeepEqual(args, got) {
		t.Fatal("annotation query retained mutable caller/copy state", after, got, err)
	}
	if statement, _, err := base.SQL(); err != nil || strings.Contains(statement, "doubled") {
		t.Fatal("annotation mutated original query", statement, err)
	}
}

func TestAnnotationsRejectUnsupportedShapesBeforeDatabase(t *testing.T) {
	b, base := updateFixture()
	integer := models.BigIntegerField("result")
	cyclic := []db.Expression{{Kind: "function", Name: "ABS"}}
	cyclic[0].Args = cyclic
	for _, expression := range []db.Expression{Sum(F("value")), Func("ROW_NUMBER"), FilteredAggregate(F("value"), Q("id", 1)), cyclic[0], F("*")} {
		if _, _, err := base.Annotate(map[string]ResultExpression{"computed": Typed(expression, integer)}).SQL(); err == nil {
			t.Fatal("unsupported row expression compiled")
		}
	}
	for _, name := range []string{"value", "payload", "bad__route", "bad name", ""} {
		if _, _, err := base.Annotate(map[string]ResultExpression{name: Typed(F("value"), integer)}).SQL(); err == nil {
			t.Fatal("colliding/invalid alias accepted", name)
		}
	}
	good := base.Annotate(map[string]ResultExpression{"computed": Typed(F("value"), integer)})
	for _, query := range []Query[*models.MapRecord]{good.Annotate(map[string]ResultExpression{"computed": Typed(F("id"), integer)}), good.Only("computed"), good.Defer("computed"), base.Annotate(map[string]ResultExpression{"cycle": Typed(F("cycle"), integer)})} {
		if _, err := query.All(context.Background()); err == nil {
			t.Fatal("invalid annotation reached database")
		}
	}
	if _, err := good.Update(context.Background(), map[string]any{"value": 1}); err == nil || b.begins != 0 {
		t.Fatal("annotated update not rejected before transaction", err)
	}
	if _, err := good.Aggregate(context.Background(), map[string]ResultExpression{"sum": Typed(Sum(F("value")), integer)}); err == nil {
		t.Fatal("annotated aggregate silently rewritten")
	}
	// A provider with ordinary field storage but no CastDialect must reject
	// a standalone typed literal before opening an executor.
	if _, err := base.Annotate(map[string]ResultExpression{"literal": Typed(Value(1), integer)}).All(context.Background()); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("literal type capability ignored", err)
	}
}

func TestAnnotationsRejectReverseRelationNamesAndCyclicOutputMetadata(t *testing.T) {
	_, query := updateFixture()
	registry := &models.Registry{}
	if err := registry.Register(query.schema); err != nil {
		t.Fatal(err)
	}
	child := models.Schema{AppLabel: "tests", Name: "Child", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: query.schema.Key(), RelatedName: "children", OnDelete: models.Cascade})}}
	if err := registry.Register(child); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	query.store.Registry = registry
	if _, _, err := query.Annotate(map[string]ResultExpression{"children": Typed(F("id"), models.BigIntegerField("out"))}).SQL(); err == nil {
		t.Fatal("reverse route collision accepted")
	}
	output := models.Field{Kind: models.Array}
	output.Element = &output
	if _, _, err := query.Annotate(map[string]ResultExpression{"loop": Typed(F("id"), output)}).SQL(); err == nil {
		t.Fatal("cyclic output metadata accepted")
	}
}
