package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestScopedConditionalBranchesAggregatesAndNulls(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Conditional", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score", models.Nullable), models.JSONField("payload", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []map[string]any{
		{"tenant": 1, "score": 1, "payload": map[string]any{"value": json.Number("9007199254740993")}},
		{"tenant": 1, "score": 2, "payload": map[string]any{"value": models.JSONNull}},
		{"tenant": 1, "score": nil, "payload": map[string]any{}},
		{"tenant": 2, "score": 99, "payload": map[string]any{"value": json.Number("1")}},
	} {
		saveMap(t, store, schema, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	integer := models.BigIntegerField("value", models.Nullable)
	conditional := orm.Case(integer, orm.Value(0),
		orm.When(orm.Q("score__gte", 1), orm.Add(orm.F("score"), orm.Value(10))),
		orm.When(orm.Q("score__gte", 2), orm.Value(99)),
	)
	nullable := orm.Case(integer, orm.Value(nil), orm.When(orm.Q("score__gte", 1), orm.F("score")))
	jsonOutput := models.JSONField("value", models.Nullable)
	jsonCase := orm.Case(jsonOutput, orm.Value(nil), orm.When(orm.Q("score__gte", 1), orm.JSONPath("payload", "value")))
	result, err := query.Aggregate(ctx, map[string]orm.ResultExpression{
		"sum":            orm.Typed(orm.Sum(conditional), integer),
		"nullable_count": orm.Typed(orm.Count(nullable), integer),
		"json_count":     orm.Typed(orm.Count(jsonCase), integer),
		"fallback_only":  orm.Typed(orm.Sum(orm.Case(integer, orm.Value(7))), integer),
	})
	if err != nil || counted.queries.Load() != 1 || result["sum"] != int64(23) || result["nullable_count"] != int64(2) || result["json_count"] != int64(2) || result["fallback_only"] != int64(21) {
		t.Fatal("conditional order/scope/null failed", result, counted.queries.Load(), err)
	}
	for _, test := range []struct {
		expression db.Expression
		lookup     string
		value      any
		want       int64
	}{
		{conditional, "exact", 12, 1}, {nullable, "isnull", true, 1}, {jsonCase, "exact", nil, 1}, {jsonCase, "isnull", true, 1}, {jsonCase, "exact", json.Number("9007199254740993"), 1},
		{orm.Case(models.TextField("value"), orm.Value("default"), orm.When(orm.Q("score__gte", 1), orm.Value(7))), "exact", "7", 2},
	} {
		got, err := query.Filter(db.Predicate{Expression: &test.expression, Lookup: test.lookup, Value: test.value}).Count(ctx)
		if err != nil || got != test.want {
			t.Fatal("conditional predicate or JSON typing failed", got, err)
		}
	}
	// An aggregate inside a condition contributes to aggregate validation;
	// ordinary ungrouped row references outside aggregates must fail before SQL.
	sum := orm.Sum(orm.F("score"))
	outer := orm.Case(integer, orm.Value(0), orm.When(db.Predicate{Expression: &sum, Lookup: "gt", Value: 2}, orm.Value(1)))
	result, err = query.Aggregate(ctx, map[string]orm.ResultExpression{"outer": orm.Typed(outer, integer)})
	if err != nil || result["outer"] != int64(1) {
		t.Fatal("conditional aggregate condition not resolved", result, err)
	}
	for _, expression := range []db.Expression{
		orm.Case(integer, orm.Value(0), orm.When(orm.Q("score__gte", 1), sum)),
		orm.Sum(orm.Case(integer, orm.Value(0), orm.When(orm.Q("score__gte", 1), sum))),
	} {
		counted.queries.Store(0)
		if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"invalid": orm.Typed(expression, integer)}); err == nil || counted.queries.Load() != 0 {
			t.Fatal("ungrouped/nested aggregate reached database", err)
		}
	}
	// The Query owns its complete branch/default/output tree.
	frozen := query.Filter(db.Predicate{Expression: &conditional, Value: 12})
	before, args, err := frozen.SQLContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conditional.Branches[0].Condition.Value = 100
	conditional.Branches[0].Then.Args[1].Value = 100
	conditional.Output.Kind = models.Text
	after, got, err := frozen.SQLContext(ctx)
	if err != nil || before != after || !reflect.DeepEqual(args, got) {
		t.Fatal("conditional query retained mutable branch", err)
	}
	cyclic := db.Expression{Kind: "case", Output: &integer, Args: []db.Expression{orm.Value(0)}}
	cyclic.Branches = []db.WhenBranch{{Condition: db.Predicate{Expression: &cyclic, Value: 1}, Then: orm.Value(1)}}
	for _, malformed := range []db.Predicate{
		{Expression: &cyclic, Value: 1},
		orm.Q("score__in", []db.Expression{cyclic}),
	} {
		counted.queries.Store(0)
		if _, err := query.Filter(malformed).Count(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("cyclic conditional reached the database", err)
		}
	}
}

func TestConditionalJoinedPredicatesKeepIndependentScopes(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "ConditionalTarget", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("score")}}
	source := models.Schema{AppLabel: "tests", Name: "ConditionalSource", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Protect}, models.Nullable)}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{target, source} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := b.SchemaEditor().(db.SchemaResolverEditor).WithSchemas([]models.Schema{target, source})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []models.Schema{target, source} {
		if err := editor.CreateModel(ctx, b, schema); err != nil {
			t.Fatal(err)
		}
	}
	store := orm.New(b, registry)
	visible := saveMap(t, store, target, map[string]any{"tenant": 2, "score": 5})
	hidden := saveMap(t, store, target, map[string]any{"tenant": 3, "score": 99})
	for _, values := range []map[string]any{{"tenant": 1, "target": mustValue(t, visible, "id")}, {"tenant": 1, "target": mustValue(t, hidden, "id")}, {"tenant": 4, "target": mustValue(t, visible, "id")}} {
		saveMap(t, store, source, values)
	}
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(source); return record }).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == source.Key() {
			return orm.Q("tenant", 1), nil
		}
		return orm.Q("tenant", 2), nil
	}).SelectRelated("target")
	integer := models.BigIntegerField("value")
	conditional := orm.Case(integer, orm.Value(1), orm.When(orm.Q("target__score__gt", 4), orm.Value(10)))
	result, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"visible": orm.Typed(orm.Sum(conditional), integer)})
	if err != nil || result["visible"] != int64(11) {
		t.Fatal("joined conditional leaked scope or lost hidden-root fallback", result, err)
	}
	if count, err := query.Filter(db.Predicate{Expression: &conditional, Value: 10}).Count(ctx); err != nil || count != 1 {
		t.Fatal("scoped conditional predicate mismatch", count, err)
	}
}
