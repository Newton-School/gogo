package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestAggregateForeignKeyJoinsScopeCollectionsWithoutHydratingOrGroupingChildren(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	author := models.Schema{AppLabel: "tests", Name: "AggregateAuthor", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.TextField("name")}}
	root := models.Schema{AppLabel: "tests", Name: "AggregateParent", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("author", models.Relation{Target: author.Key(), OnDelete: models.Protect}, models.Nullable)}}
	child := models.Schema{AppLabel: "tests", Name: "AggregateChild", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("parent", models.Relation{Target: root.Key(), OnDelete: models.Cascade, RelatedName: "children"}), models.IntegerField("score"), models.JSONField("data")}}
	note := models.Schema{AppLabel: "tests", Name: "AggregateNote", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("parent", models.Relation{Target: root.Key(), OnDelete: models.Cascade, RelatedName: "notes"})}}
	registry := &models.Registry{}
	operations := []migrations.Operation{}
	for _, schema := range []models.Schema{author, root, child, note} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, migrations.CreateModel(schema))
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: operations}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	visibleAuthor := saveMap(t, store, author, map[string]any{"tenant": 22, "name": "visible"})
	hiddenAuthor := saveMap(t, store, author, map[string]any{"tenant": 99, "name": "hidden"})
	var roots []*models.MapRecord
	for _, values := range []map[string]any{{"tenant": 11, "author": mustValue(t, visibleAuthor, "id")}, {"tenant": 11, "author": mustValue(t, hiddenAuthor, "id")}, {"tenant": 11, "author": nil}, {"tenant": 99, "author": mustValue(t, visibleAuthor, "id")}} {
		roots = append(roots, saveMap(t, store, root, values))
	}
	for _, item := range []struct{ root, tenant, score int }{{0, 33, 3}, {0, 33, 5}, {0, 99, 100}, {1, 99, 100}, {3, 33, 100}} {
		saveMap(t, store, child, map[string]any{"parent": mustValue(t, roots[item.root], "id"), "tenant": item.tenant, "score": item.score, "data": map[string]any{"n": item.score}})
	}
	for _, item := range []struct{ root, tenant int }{{0, 44}, {0, 44}, {0, 44}, {0, 99}, {1, 44}} {
		saveMap(t, store, note, map[string]any{"parent": mustValue(t, roots[item.root], "id"), "tenant": item.tenant})
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	scope := func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		tenants := map[string]int{root.Key(): 11, author.Key(): 22, child.Key(): 33, note.Key(): 44}
		return orm.Q("tenant", tenants[schema.Key()]), nil
	}
	base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(root); return r }).WithScope(scope).OrderBy("id")
	integer, nullable := models.BigIntegerField("out"), models.BigIntegerField("out", models.Nullable)
	query := base.Annotate(map[string]orm.ResultExpression{
		"child_count": orm.Typed(orm.Count(orm.F("children__id")), integer),
		"total":       orm.Typed(orm.Sum(orm.F("children__score")), nullable),
		"json_total":  orm.Typed(orm.Sum(orm.Cast(orm.F("children__data__n"), integer)), nullable),
		"higher":      orm.Typed(orm.FilteredAggregate(orm.Count(orm.F("children__id")), orm.Q("children__score__gt", 3)), integer),
	})
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 3 || counted.queries.Load() != 1 {
		t.Fatal("scoped collection aggregation", rows, err)
	}
	for index, row := range rows {
		if len(row.State().Related) != 0 {
			t.Fatal("aggregate join populated an eager relation cache")
		}
		values := row.State().Annotations
		if index == 0 {
			if values["child_count"] != int64(2) || values["total"] != int64(8) || values["json_total"] != int64(8) || values["higher"] != int64(1) {
				t.Fatal("hidden child affected aggregates", values)
			}
		} else if values["child_count"] != int64(0) || values["total"] != nil || values["higher"] != int64(0) {
			t.Fatal("missing/hidden child removed parent or changed null output", values)
		}
	}
	if count, err := query.Filter(orm.Q("child_count", 0)).Count(ctx); err != nil || count != 2 {
		t.Fatal("inferred relation HAVING failed", count, err)
	}
	values, err := query.Only("id").Values(ctx, "child_count")
	if err != nil || len(values) != 3 || values[0]["child_count"] != int64(2) {
		t.Fatal("collection grouping identity changed with projection", values, err)
	}
	aggregated, err := base.Aggregate(ctx, map[string]orm.ResultExpression{"total": orm.Typed(orm.Sum(orm.F("children__score")), nullable)})
	if err != nil || aggregated["total"] != int64(8) {
		t.Fatal("scalar aggregate did not infer scoped FK joins", aggregated, err)
	}
	forward := base.Annotate(map[string]orm.ResultExpression{"authors": orm.Typed(orm.Count(orm.F("author__id")), integer)})
	rows, err = forward.All(ctx)
	if err != nil || len(rows) != 3 || rows[0].State().Annotations["authors"] != int64(1) || rows[1].State().Annotations["authors"] != int64(0) {
		t.Fatal("forward FK aggregate escaped target scope", err)
	}
	rows, err = query.SelectRelated("author").All(ctx)
	if err != nil || len(rows) != 3 || rows[0].State().Annotations["child_count"] != int64(2) {
		t.Fatal("explicit eager join changed aggregate grouping", err)
	}
	for index, row := range rows {
		loaded, present := orm.RelatedOne(row, "author")
		if !present || index == 0 && loaded == nil || index > 0 && loaded != nil {
			t.Fatal("aggregate/eager hydration became mixed")
		}
	}
	multiple := base.Annotate(map[string]orm.ResultExpression{
		"children_rows":     orm.Typed(orm.Count(orm.F("children__id")), integer),
		"notes_rows":        orm.Typed(orm.Count(orm.F("notes__id")), integer),
		"children_distinct": orm.Typed(orm.DistinctAggregate(orm.Count(orm.F("children__id"))), integer),
		"notes_distinct":    orm.Typed(orm.DistinctAggregate(orm.Count(orm.F("notes__id"))), integer),
	})
	rows, err = multiple.All(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatal(err)
	}
	first := rows[0].State().Annotations
	if first["children_rows"] != int64(6) || first["notes_rows"] != int64(6) || first["children_distinct"] != int64(2) || first["notes_distinct"] != int64(3) {
		t.Fatal("multiple joins lost SQL multiplication or distinct semantics", first)
	}
	nested := base.Annotate(map[string]orm.ResultExpression{"nested": orm.Typed(orm.Count(orm.F("children__parent__author__id")), integer)})
	rows, err = nested.All(ctx)
	if err != nil || len(rows) != 3 || rows[0].State().Annotations["nested"] != int64(2) || rows[1].State().Annotations["nested"] != int64(0) {
		t.Fatal("finite reverse-forward paths lost independent aliases or scope", err)
	}
	counted.queries.Store(0)
	denied := errors.New("scope unavailable")
	if _, err := query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == child.Key() {
			return db.Predicate{}, denied
		}
		return scope(ctx, schema)
	}).All(ctx); !errors.Is(err, denied) || counted.queries.Load() != 0 {
		t.Fatal("failed relation scope reached SQL", err)
	}
	if _, err := base.Annotate(map[string]orm.ResultExpression{"unscoped_scalar": orm.Typed(orm.F("children__score"), integer)}).All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("row annotation inferred a collection join", err)
	}
	deep := base.Annotate(map[string]orm.ResultExpression{"too_deep": orm.Typed(orm.Count(orm.F(strings.Repeat("children__parent__", 5)+"id")), integer)})
	if _, err := deep.All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("unbounded automatic relation path reached SQL", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	if _, err := query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == child.Key() {
			cancel()
		}
		return scope(ctx, schema)
	}).All(canceled); !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
		t.Fatal("relation scope cancellation was lost", err)
	}
	cancel()
}

func TestAggregateReverseForeignKeyUsesDeclaredNonPrimaryTargetField(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	root := models.Schema{AppLabel: "tests", Name: "AggregateCode", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code", func(f *models.Field) { f.Unique = true })}}
	child := models.Schema{AppLabel: "tests", Name: "AggregateCodeChild", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: root.Key(), TargetFields: []string{"code"}, OnDelete: models.Cascade, RelatedName: "children"})}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{root, child} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(root), migrations.CreateModel(child)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	saveMap(t, store, root, map[string]any{"code": "first"})
	saveMap(t, store, root, map[string]any{"code": "second"})
	saveMap(t, store, child, map[string]any{"parent": "first"})
	rows, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(root); return r }).Annotate(map[string]orm.ResultExpression{"children_count": orm.Typed(orm.Count(orm.F("children__id")), models.BigIntegerField("out"))}).OrderBy("id").All(ctx)
	if err != nil || len(rows) != 2 || rows[0].State().Annotations["children_count"] != int64(1) || rows[1].State().Annotations["children_count"] != int64(0) {
		t.Fatal("reverse join ignored its declared unique target field", err)
	}
}

func TestAggregateManyToManyJoinFailsClosedBeforeQueries(t *testing.T) {
	b := openTest(t)
	source, target := m2mSchemas()
	registry := &models.Registry{}
	for _, schema := range []models.Schema{source, target} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	counted := &countedBackend{Backend: b}
	query := orm.For(orm.New(counted, registry), func() *models.MapRecord { r, _ := models.NewRecord(source); return r }).Annotate(map[string]orm.ResultExpression{"tags_count": orm.Typed(orm.Count(orm.F("tags__id")), models.BigIntegerField("out"))})
	if _, err := query.All(context.Background()); !db.IsCode(err, db.UnsupportedFeature) || counted.queries.Load() != 0 {
		t.Fatal("unscoped intermediary aggregation executed", err)
	}
}
