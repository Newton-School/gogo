package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestModelAggregateAnnotationsPreserveTypedIdentityProjectionAndSave(t *testing.T) {
	_, store := setupProducts(t)
	ctx := context.Background()
	for range 2 {
		if err := store.Save(ctx, &product{Name: "same", Amount: "1.00"}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	query := orm.For(store, func() *product { return &product{} }).Annotate(map[string]orm.ResultExpression{
		"rows":    orm.Typed(orm.Count(orm.F("*")), models.BigIntegerField("out")),
		"maximum": orm.Typed(orm.Max(orm.F("amount")), models.DecimalField("out", 10, 2)),
		"derived": orm.Typed(orm.Add(orm.F("rows"), orm.Value(2)), models.BigIntegerField("out")),
	}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || rows[0].ID == rows[1].ID || !rows[0].ModelState().Persisted {
		t.Fatal("aggregate model identity/hydration failed", err)
	}
	for _, row := range rows {
		if row.ModelState().Annotations["rows"] != int64(1) || row.ModelState().Annotations["maximum"] != "1.00" || row.ModelState().Annotations["derived"] != int64(3) {
			t.Fatal("typed model aggregate output failed")
		}
	}
	values, err := query.Values(ctx, "rows")
	if err != nil || len(values) != 2 || len(values[0]) != 1 || values[0]["rows"] != int64(1) {
		t.Fatal("omitting PK collapsed model aggregate rows", values, err)
	}
	deferred, err := query.Only("amount").Filter(orm.Q("id", rows[0].ID)).Get(ctx)
	if err != nil || deferred.ID != rows[0].ID || !deferred.ModelState().Deferred["name"] || deferred.ModelState().Annotations["rows"] != int64(1) {
		t.Fatal("model aggregate lost deferred field state", err)
	}
	deferred.Amount = "2.00"
	deferred.ModelState().Annotations["rows"] = "not a stored field"
	if err := store.Save(ctx, deferred, orm.SaveOptions{}); err != nil {
		t.Fatal("annotation snapshot entered persistence", err)
	}
	loaded, err := orm.For(store, func() *product { return &product{} }).Filter(orm.Q("id", rows[0].ID)).Get(ctx)
	if err != nil || loaded.Name != "same" || loaded.Amount != "2.00" {
		t.Fatal("deferred aggregate model Save changed unrelated fields", err)
	}
	if count, err := query.Having(orm.Q("maximum__gt", "1.00")).Count(ctx); err != nil || count != 1 {
		t.Fatal("model aggregate HAVING count failed", count, err)
	}
	if count, err := query.Limit(1).Count(ctx); err != nil || count != 1 {
		t.Fatal("model aggregate paged count failed", count, err)
	}
	if exists, err := query.Having(orm.Q("rows__gt", 10)).Exists(ctx); err != nil || exists {
		t.Fatal("model aggregate empty existence failed", exists, err)
	}
	iterator, err := query.Iterator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for iterator.Next() {
		seen++
	}
	if err := iterator.Err(); err != nil || seen != 2 {
		t.Fatal("model aggregate iterator failed", err)
	}
	counted := &countedBackend{Backend: store.Backend}
	store.Backend = counted
	for _, invalid := range []orm.Query[*product]{query.Distinct(), query.DistinctOn("id"), query.SelectForUpdate(false, false), query.Annotate(map[string]orm.ResultExpression{"bad": orm.Typed(orm.Sum(orm.F("maximum")), models.DecimalField("out", 10, 2))})} {
		if _, err := invalid.All(ctx); err == nil || counted.queries.Load() != 0 {
			t.Fatal("unsupported model group executed SQL", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := query.Values(canceled); !errors.Is(err, context.Canceled) || counted.queries.Load() != 0 {
		t.Fatal("model aggregate ignored cancellation", err)
	}
}

func TestModelAggregateAnnotationsCountMissingAndScopedRelatedRowsAsZero(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	root := models.Schema{AppLabel: "tests", Name: "AggregateRoot", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.BigIntegerField("tenant", models.WithStructField("Tenant"))}}
	profile := models.Schema{AppLabel: "tests", Name: "AggregateProfile", Fields: []models.Field{
		models.BigAutoField("id"), models.BigIntegerField("tenant"),
		models.OneToOneField("root", models.Relation{Target: root.Key(), OnDelete: models.Cascade, RelatedName: "profile"}),
		models.IntegerField("score", models.Nullable), models.JSONField("data", models.Nullable),
	}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{root, profile} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(root), migrations.CreateModel(profile)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	var records []*models.MapRecord
	for _, tenant := range []int64{11, 11, 11, 99} {
		records = append(records, saveMap(t, store, root, map[string]any{"tenant": tenant}))
	}
	for _, item := range []struct {
		index  int
		tenant int64
	}{{0, 22}, {1, 99}, {3, 22}} {
		saveMap(t, store, profile, map[string]any{"tenant": item.tenant, "root": mustValue(t, records[item.index], "id"), "score": 7, "data": map[string]any{"n": int64(9007199254740993)}})
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	integer := models.BigIntegerField("out")
	query := orm.For(store, func() *relationRow { return &relationRow{Definition: root} }).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == root.Key() {
			return orm.Q("tenant", int64(11)), nil
		}
		return orm.Q("tenant", int64(22)), nil
	}).SelectRelated("profile").Annotate(map[string]orm.ResultExpression{
		"profiles":      orm.Typed(orm.Count(orm.F("profile__id")), integer),
		"score":         orm.Typed(orm.Sum(orm.F("profile__score")), models.BigIntegerField("out", models.Nullable)),
		"default_score": orm.Typed(orm.Func("COALESCE", orm.Sum(orm.F("profile__score")), orm.Value(0)), integer),
		"json_number":   orm.Typed(orm.F("profile__data__n"), models.JSONField("out", models.Nullable)),
	}).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 3 || counted.queries.Load() != 1 {
		t.Fatal("scoped model aggregate joins failed", err)
	}
	for i, row := range rows {
		bound, _ := models.Bind(row)
		related, loaded := orm.RelatedOne(bound, "profile")
		if !loaded {
			t.Fatal("aggregate model join was not hydrated")
		}
		if i == 0 {
			if related == nil || row.ModelState().Annotations["profiles"] != int64(1) || row.ModelState().Annotations["score"] != int64(7) || row.ModelState().Annotations["json_number"] != json.Number("9007199254740993") {
				t.Fatal("visible profile aggregate incorrect")
			}
		} else if related != nil || row.ModelState().Annotations["profiles"] != int64(0) || row.ModelState().Annotations["score"] != nil || row.ModelState().Annotations["default_score"] != int64(0) || row.ModelState().Annotations["json_number"] != nil {
			t.Fatal("missing or hidden profile influenced aggregate")
		}
	}
	missing, err := query.Having(orm.Q("profiles", 0)).All(ctx)
	if err != nil || len(missing) != 2 {
		t.Fatal("zero child HAVING removed root identity", err)
	}
	values, err := query.Values(ctx, "profiles", "profile__data__n")
	if err != nil || len(values) != 3 || len(values[0]) != 2 || values[0]["profile__data__n"] != json.Number("9007199254740993") || values[1]["profile__data__n"] != nil {
		t.Fatal("grouped JSON transform Values failed", values, err)
	}
	counted.queries.Store(0)
	if _, err := query.SelectRelated().All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("aggregate reference invented an unscoped join", err)
	}
}
