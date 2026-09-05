package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAggregateFilterRoutingPreservesScopedRowsBooleanLogicAndTypedGroups(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "RoutedGroup", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.TextField("category"), models.IntegerField("score", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []map[string]any{
		{"tenant": 11, "category": "a", "score": 1}, {"tenant": 11, "category": "a", "score": 3},
		{"tenant": 11, "category": "b", "score": 2}, {"tenant": 11, "category": "c", "score": 5},
		{"tenant": 11, "category": "d", "score": nil}, {"tenant": 11, "category": "e", "score": 1},
		{"tenant": 99, "category": "a", "score": 100}, {"tenant": 99, "category": "b", "score": 100},
	} {
		saveMap(t, store, schema, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 11), nil })
	integer, nullable := models.BigIntegerField("out"), models.BigIntegerField("out", models.Nullable)
	query := base.GroupBy("category").Annotate(map[string]orm.ResultExpression{
		"count": orm.Typed(orm.Count(orm.F("*")), integer),
		"total": orm.Typed(orm.Sum(orm.F("score")), nullable),
		"stats": orm.Typed(orm.Cast(orm.Func("JSON_BUILD_OBJECT", orm.Cast(orm.Value("n"), models.TextField("out")), orm.F("total")), models.JSONField("out")), models.JSONField("out")),
	}).OrderBy("category")
	conditional := orm.Case(integer, orm.Value(0), orm.When(orm.Q("total__gte", 4), orm.Value(1)))
	for _, test := range []struct {
		name  string
		query orm.Query[*models.MapRecord]
		want  []string
	}{
		{"aggregate", query.Filter(orm.Q("total__gte", 4)), []string{"a", "c"}},
		{"rows_before_groups", query.Filter(orm.Q("score__gte", 2), orm.Q("total__gte", 4)), []string{"c"}},
		{"connected_or", query.Filter(orm.Or(orm.Q("category", "b"), orm.Q("total__gte", 4))), []string{"a", "b", "c"}},
		{"negated_and", query.Exclude(orm.Q("category", "b"), orm.Q("total__gte", 4)), []string{"a", "b", "c", "d", "e"}},
		{"negated_or", query.Exclude(orm.Or(orm.Q("category", "b"), orm.Q("total__gte", 4))), []string{"e"}},
		{"repeated_filters", query.Filter(orm.Q("total__gte", 4)).Filter(orm.Q("total__lt", 5)), []string{"a"}},
		{"explicit_having", query.Having(orm.Q("count__gte", 2)).Filter(orm.Q("total__gte", 4)), []string{"a"}},
		{"json_alias_path", query.Filter(orm.Q("stats__n", 4)), []string{"a"}},
		{"expression_rhs", query.Filter(orm.Q("count", orm.F("total"))), []string{"e"}},
		{"membership_rhs", query.Filter(orm.Q("count__in", []db.Expression{orm.F("total")})), []string{"e"}},
		{"range_rhs", query.Filter(orm.Q("count__range", []any{1, orm.F("total")})), []string{"a", "b", "c", "e"}},
		{"conditional", query.Filter(db.Predicate{Expression: &conditional, Value: 1}), []string{"a", "c"}},
		{"null_aggregate", query.Filter(orm.Q("total__isnull", true)), []string{"d"}},
		{"negated_null", query.Exclude(orm.Q("total__isnull", true)), []string{"a", "b", "c", "e"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := test.query.Values(ctx, "category", "total")
			if err != nil {
				t.Fatal(err)
			}
			actual := make([]string, len(rows))
			for i := range rows {
				actual[i] = rows[i]["category"].(string)
			}
			if !reflect.DeepEqual(actual, test.want) {
				t.Fatal("group routing changed selected categories", actual, test.want)
			}
			count, err := test.query.Count(ctx)
			if err != nil || count != int64(len(test.want)) {
				t.Fatal("group routing count mismatch", count, err)
			}
		})
	}
	if count, err := query.Filter(orm.Q("total__gte", 4)).Limit(1).Count(ctx); err != nil || count != 1 {
		t.Fatal("routed group pagination failed", count, err)
	}
	// Implicit per-model grouping supports the same filter/iterator path without
	// turning annotation snapshots into stored fields or collapsing identities.
	modelQuery := base.Annotate(map[string]orm.ResultExpression{"maximum": orm.Typed(orm.Max(orm.F("score")), nullable)}).Filter(orm.Q("maximum__gte", 3))
	rows, err := modelQuery.All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal("implicit model aggregate Filter failed", len(rows), err)
	}
	for _, row := range rows {
		if !row.State().Persisted || row.State().Annotations["maximum"] == nil {
			t.Fatal("routed model lost state")
		}
	}
	iterator, err := modelQuery.Iterator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for iterator.Next() {
		seen++
	}
	if iterator.Err() != nil || seen != 2 {
		t.Fatal("routed model iterator failed", iterator.Err())
	}
	for _, invalid := range []orm.Query[*models.MapRecord]{
		query.Filter(orm.Or(orm.Q("score__gt", 1), orm.Q("total__gt", 3))),
		query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("total__gt", 0), nil }),
		query.Annotate(map[string]orm.ResultExpression{"nested": orm.Typed(orm.Sum(orm.F("stats__n")), integer)}).Filter(orm.Q("nested__gt", 1)),
	} {
		counted.queries.Store(0)
		if _, err := invalid.Values(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid routed expression executed SQL", err)
		}
	}
	counted.queries.Store(0)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := query.Filter(orm.Q("total__gt", 0)).Values(canceled); !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
		t.Fatal("routed query ignored cancellation", err)
	}
}

func TestAggregateRoutingRetainsExactStoredFieldPrecedence(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "RoutingNames", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("category"), models.IntegerField("stats__n"), models.IntegerField("score")}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []map[string]any{{"category": 1, "stats__n": 1, "score": 3}, {"category": 1, "stats__n": 2, "score": 9}} {
		saveMap(t, store, schema, value)
	}
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).GroupBy("category").Annotate(map[string]orm.ResultExpression{
		"stats": orm.Typed(orm.Sum(orm.F("score")), models.BigIntegerField("out")),
	}).Filter(orm.Q("stats__n", 1))
	rows, err := query.Values(ctx, "category", "stats")
	if err != nil || len(rows) != 1 || rows[0]["stats"] != int64(3) {
		t.Fatal("exact stored field filter crossed grouping boundary", rows, err)
	}
}
