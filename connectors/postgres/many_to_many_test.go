package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
)

func m2mSchemas() (models.Schema, models.Schema) {
	target := models.Schema{AppLabel: "tests", Name: "Tag", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	source := models.Schema{AppLabel: "tests", Name: "Post", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ManyToManyField("tags", models.Relation{Target: target.Key(), RelatedName: "posts"})}}
	return source, target
}
func saveMap(t *testing.T, store *orm.Store, schema models.Schema, values map[string]any) *models.MapRecord {
	t.Helper()
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		if err := record.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(context.Background(), record, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAutomaticIntermediaryMigrationAndScopedDeletion(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	preview, err := engine.SQL(ctx, initial.Key(), false)
	if err != nil || len(preview) != 3 {
		t.Fatal(preview, err)
	}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{source, target} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	store.Registry = registry
	post := saveMap(t, store, source, map[string]any{"tenant": 1})
	tag := saveMap(t, store, target, map[string]any{"tenant": 2})
	postID, _ := post.Get("id")
	tagID, _ := tag.Get("id")
	field, _ := source.Field("tags")
	through, _ := models.ImplicitThrough(source, field, target)
	join := saveMap(t, store, through, map[string]any{"source_id": postID, "target_id": tagID})
	collector := orm.DeleteCollector{Store: store, Scope: func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.AutoCreatedBy != "" {
			t.Fatal("tenant scope called for internal intermediary")
		}
		return orm.Q("tenant", 1), nil
	}}
	if _, err := collector.Collect(ctx, join); err == nil {
		t.Fatal("automatic intermediary accepted as root")
	}
	plan, err := collector.Collect(ctx, post)
	if err != nil || len(plan.Objects) != 1 || len(plan.JoinRemovals) != 1 {
		t.Fatal(plan, err)
	}
	if plan.JoinRemovals[0].Endpoint.Schema().Key() != source.Key() || plan.JoinRemovals[0].Field != "source_id" {
		t.Fatal(plan)
	}
	denied := errors.New("policy denied")
	collector.Authorize = func(_ context.Context, plan orm.DeletionPlan) error {
		if len(plan.JoinRemovals) != 1 {
			t.Fatal(plan)
		}
		return denied
	}
	if _, err := collector.Execute(ctx, post); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	collector.Authorize = nil
	counts, err := collector.Execute(ctx, post)
	if err != nil || len(counts) != 1 || counts[source.Key()] != 1 {
		t.Fatal(counts, err)
	}
	for _, check := range []struct {
		schema models.Schema
		count  int64
	}{{target, 1}, {through, 0}} {
		count, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(check.schema); return r }).Count(ctx)
		if err != nil || count != check.count {
			t.Fatal(check.schema.Key(), count, err)
		}
	}
	if err := engine.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 1 || tables[0] != "gogo_migrations" {
		t.Fatal(tables, err)
	}
}

func TestAutomaticIntermediaryAddAndRemoveField(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	field, _ := source.Field("tags")
	source.Fields = source.Fields[:2]
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	second := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.AddField(source, field)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, second}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 3 {
		t.Fatal(tables, err)
	}
}
