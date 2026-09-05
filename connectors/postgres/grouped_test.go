package postgres_test

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestGroupedAnnotationsHavingTypedNullsAndScopedCounts(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Grouped", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.TextField("category", models.Nullable), models.IntegerField("score", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, value := range []map[string]any{
		{"tenant": 1, "category": "a", "score": 1}, {"tenant": 1, "category": "a", "score": 3}, {"tenant": 1, "category": "b", "score": 2}, {"tenant": 1, "category": "c", "score": nil}, {"tenant": 1, "category": nil, "score": 4}, {"tenant": 2, "category": "a", "score": 99},
	} {
		saveMap(t, store, schema, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	integer, nullable := models.BigIntegerField("out"), models.BigIntegerField("out", models.Nullable)
	grouped := base.GroupBy("category").Annotate(map[string]orm.ResultExpression{
		"count":           orm.Typed(orm.Count(orm.F("*")), integer),
		"total":           orm.Typed(orm.Sum(orm.F("score")), nullable),
		"nonempty":        orm.Typed(orm.Count(orm.F("score")), integer),
		"distinct_scores": orm.Typed(orm.DistinctAggregate(orm.Count(orm.F("score"))), integer),
		"higher":          orm.Typed(orm.FilteredAggregate(orm.Count(orm.F("*")), orm.Q("score__gt", 1)), integer),
		"default_total":   orm.Typed(orm.Func("COALESCE", orm.Sum(orm.F("score")), orm.Value(0)), integer),
		"derived":         orm.Typed(orm.Add(orm.F("count"), orm.Value(1)), integer),
	})
	values, err := grouped.OrderBy("category").Values(ctx)
	if err != nil || len(values) != 4 || counted.queries.Load() != 1 {
		t.Fatal("grouped values", values, err)
	}
	if values[0]["category"] != "a" || values[0]["count"] != int64(2) || values[0]["total"] != int64(4) || values[0]["higher"] != int64(1) || values[0]["distinct_scores"] != int64(2) || values[0]["derived"] != int64(3) {
		t.Fatal("scoped aggregate values", values[0])
	}
	if values[2]["category"] != "c" || values[2]["total"] != nil || values[2]["default_total"] != int64(0) || values[2]["nonempty"] != int64(0) || values[3]["category"] != nil {
		t.Fatal("typed empty/null group semantics", values)
	}
	filtered := grouped.Having(orm.Q("total__gte", 3)).OrderBy("-total", "category")
	values, err = filtered.Values(ctx, "category", "total")
	if err != nil || len(values) != 2 || len(values[0]) != 2 || values[0]["category"] != "a" || values[1]["category"] != nil {
		t.Fatal("HAVING or selected group projections", values, err)
	}
	if count, err := filtered.Count(ctx); err != nil || count != 2 {
		t.Fatal("scoped grouped count", count, err)
	}
	if count, err := filtered.Limit(1).Count(ctx); err != nil || count != 1 {
		t.Fatal("group pagination count", count, err)
	}
	values, err = grouped.Filter(orm.Q("score__gte", 2)).OrderBy("category").Values(ctx, "category", "total")
	if err != nil || len(values) != 3 || values[0]["total"] != int64(3) {
		t.Fatal("row filter crossed grouping boundary", values, err)
	}
	values, err = grouped.Filter(orm.Q("tenant", 999)).Values(ctx)
	if err != nil || len(values) != 0 {
		t.Fatal("empty grouped set", values, err)
	}
	values, err = base.GroupBy("category").OrderBy("category").Values(ctx)
	if err != nil || len(values) != 4 || len(values[0]) != 1 {
		t.Fatal("key-only grouping", values, err)
	}
	jsonStats := base.GroupBy("category").Annotate(map[string]orm.ResultExpression{
		"stats": orm.Typed(orm.Func("JSON_BUILD_OBJECT", orm.Value("n"), orm.Sum(orm.F("score"))), models.JSONField("out")),
	})
	for _, invalid := range []orm.Query[*models.MapRecord]{
		grouped.Having(orm.Q("score__gt", 1)), grouped.Filter(orm.Q("count__gt", 1)), grouped.OrderBy("score"), grouped.SelectForUpdate(false, false), grouped.Distinct(), base.GroupBy("category", "category"), base.GroupBy("category").Annotate(map[string]orm.ResultExpression{"bad": orm.Typed(orm.Sum(orm.Sum(orm.F("score"))), integer)}), base.GroupBy("category").Annotate(map[string]orm.ResultExpression{"loop": orm.Typed(orm.F("loop"), integer)}), base.GroupBy("category").Annotate(map[string]orm.ResultExpression{"window": orm.Typed(orm.Func("ROW_NUMBER"), integer)}),
		jsonStats.Filter(orm.Q("stats__n__gt", 1)),
		jsonStats.Filter(orm.Q("score__in", []db.Expression{orm.F("stats__n")})),
		jsonStats.Annotate(map[string]orm.ResultExpression{"nested": orm.Typed(orm.Sum(orm.F("stats__n")), integer)}),
	} {
		counted.queries.Store(0)
		if _, err := invalid.Values(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid grouped query executed", err)
		}
	}
	counted.queries.Store(0)
	if _, err := grouped.Values(ctx, "score"); err == nil || counted.queries.Load() != 0 {
		t.Fatal("ungrouped projection executed", err)
	}
	if _, err := grouped.All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("group materialized as persisted model", err)
	}
	if _, err := grouped.Update(ctx, map[string]any{"score": 1}); err == nil || counted.queries.Load() != 0 {
		t.Fatal("grouped update executed", err)
	}
	if _, err := grouped.Delete(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("grouped delete executed", err)
	}
}

func TestGroupedJoinedKeysKeepIndependentScopesAndNullGroups(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "GroupedTarget", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.TextField("category"), models.IntegerField("score")}}
	source := models.Schema{AppLabel: "tests", Name: "GroupedSource", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Protect}, models.Nullable)}}
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
	visible := saveMap(t, store, target, map[string]any{"tenant": 22, "category": "visible", "score": 5})
	hidden := saveMap(t, store, target, map[string]any{"tenant": 44, "category": "secret", "score": 99})
	for _, value := range []map[string]any{{"tenant": 33, "target": mustValue(t, visible, "id")}, {"tenant": 33, "target": mustValue(t, visible, "id")}, {"tenant": 33, "target": mustValue(t, hidden, "id")}, {"tenant": 33, "target": nil}, {"tenant": 44, "target": mustValue(t, visible, "id")}} {
		saveMap(t, store, source, value)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	integer := models.BigIntegerField("out")
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(source); return r }).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == source.Key() {
			return orm.Q("tenant", 33), nil
		}
		return orm.Q("tenant", 22), nil
	}).SelectRelated("target").GroupBy("target__category").Annotate(map[string]orm.ResultExpression{
		"rows":            orm.Typed(orm.Count(orm.F("*")), integer),
		"visible_targets": orm.Typed(orm.Count(orm.F("target__id")), integer),
		"sum":             orm.Typed(orm.Sum(orm.F("target__score")), models.BigIntegerField("out", models.Nullable)),
		"conditional":     orm.Typed(orm.Sum(orm.Case(integer, orm.Value(7), orm.When(orm.Q("target__score__gt", 4), orm.Value(11)))), integer),
	}).OrderBy("target__category")
	values, err := query.Values(ctx)
	if err != nil || len(values) != 2 || counted.queries.Load() != 1 {
		t.Fatal("scoped joined groups", values, err)
	}
	if values[0]["target__category"] != "visible" || values[0]["rows"] != int64(2) || values[0]["sum"] != int64(10) || values[0]["conditional"] != int64(22) || values[1]["target__category"] != nil || values[1]["rows"] != int64(2) || values[1]["visible_targets"] != int64(0) || values[1]["sum"] != nil || values[1]["conditional"] != int64(14) {
		t.Fatal("hidden/null join changed groups", values)
	}
	values, err = query.Having(orm.Q("target__category__isnull", true)).Values(ctx, "rows", "sum")
	if err != nil || len(values) != 1 || values[0]["rows"] != int64(2) || values[0]["sum"] != nil {
		t.Fatal("nullable joined HAVING key", values, err)
	}
	counted.queries.Store(0)
	if _, err := query.SelectRelated().Values(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("grouping invented an unscoped join", err)
	}
}
