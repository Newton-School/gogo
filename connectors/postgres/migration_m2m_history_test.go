package postgres_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func TestAutomaticIntermediaryHistoricalModelDeletion(t *testing.T) {
	for _, both := range []bool{false, true} {
		name := "source"
		if both {
			name = "both endpoints"
		}
		t.Run(name, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			source, target := m2mSchemas()
			through, err := models.ImplicitThrough(source, source.Fields[2], target)
			if err != nil {
				t.Fatal(err)
			}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
			removed := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.DeleteModel(source)}}
			if both {
				removed.Operations = append(removed.Operations, migrations.DeleteModel(target))
			}
			engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, removed}}
			if err := engine.Apply(ctx, initial.Key()); err != nil {
				t.Fatal(err)
			}
			preview, err := engine.SQL(ctx, removed.Key(), false)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview) != len(removed.Operations)+1 || !strings.Contains(preview[0].SQL, `DROP TABLE "`+through.DBTable()+`"`) {
				t.Errorf("automatic intermediary must drop first and exactly once: %+v", preview)
			}
			if err := engine.Apply(ctx, removed.Key()); err != nil {
				t.Fatal(err)
			}
			tables, err := b.Introspector().Tables(ctx, b)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if both {
				want = 1
			}
			if len(tables) != want {
				t.Fatal(tables)
			}
			if _, err := engine.SQL(ctx, removed.Key(), true); err == nil {
				t.Fatal("destructive model deletion was reported reversible")
			}
		})
	}
}

func TestAutomaticIntermediaryHistoricalFieldAndTargetRemoval(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	removed := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.RemoveField(source, source.Fields[2]), migrations.DeleteModel(target)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, removed}}
	preview, err := engine.SQL(ctx, removed.Key(), false)
	if err != nil || len(preview) != 2 || !strings.Contains(preview[0].SQL, "tests_post_tags") {
		t.Fatal(preview, err)
	}
	if err := engine.Apply(ctx, removed.Key()); err != nil {
		t.Fatal(err)
	}
	state, err := engine.State(removed.Key())
	if err != nil || len(state) != 1 || len(state[0].Fields) != 2 {
		t.Fatal(state, err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || !reflect.DeepEqual(tables, []string{"gogo_migrations", source.DBTable()}) {
		t.Fatal(tables, err)
	}
}

func TestAutomaticIntermediaryTransientFieldLifecycle(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "new field", true: "replacement then source deletion"}[replacement], func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			source, target := m2mSchemas()
			field := source.Fields[2]
			without := source.Clone()
			without.Fields = without.Fields[:2]
			start := without
			operations := []migrations.Operation{migrations.AddField(without, field), migrations.RemoveField(source, field)}
			if replacement {
				start = source
				operations = []migrations.Operation{migrations.RemoveField(source, field), migrations.AddField(without, field), migrations.DeleteModel(source)}
			}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(start), migrations.CreateModel(target)}}
			change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
			engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
			preview, err := engine.SQL(ctx, change.Key(), false)
			wantSQL, wantTables := 0, 3
			if replacement {
				wantSQL, wantTables = 2, 2
			}
			if err != nil || len(preview) != wantSQL {
				t.Fatal(preview, err)
			}
			if err := engine.Apply(ctx, change.Key()); err != nil {
				t.Fatal(err)
			}
			tables, err := b.Introspector().Tables(ctx, b)
			if err != nil || len(tables) != wantTables {
				t.Fatal(tables, err)
			}
		})
	}
}

func TestAutomaticIntermediaryHistoricalDeletionRollbackAndRetry(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.DeleteModel(source), migrations.RunSQL("SELECT 1 / 0", nil, "SELECT 1", nil)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := engine.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := engine.Apply(ctx, change.Key()); err == nil || !strings.Contains(err.Error(), "22012") {
			t.Fatal("did not reach later DDL failure", err)
		}
		tables, err := b.Introspector().Tables(ctx, b)
		if err != nil || len(tables) != 4 {
			t.Fatal("rollback lost historical tables", tables, err)
		}
		history, err := engine.History(ctx)
		if err != nil || len(history) != 1 || history[0].Key != initial.Key() {
			t.Fatal(history, err)
		}
	}
	engine.Migrations[1].Operations = engine.Migrations[1].Operations[:1]
	if err := engine.Apply(ctx, change.Key()); err != nil {
		t.Fatal(err)
	}
	history, err := engine.History(ctx)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	if err := engine.Reverse(ctx, initial.Key()); err == nil || !strings.Contains(err.Error(), "irreversible") {
		t.Fatal("destructive removal reported recoverable", err)
	}
}

func TestAutomaticIntermediaryHistoricalSourceReplacement(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	replacement := source.Clone()
	replacement.Table = "replacement_posts"
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.DeleteModel(source), migrations.CreateModel(replacement)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	preview, err := engine.SQL(ctx, change.Key(), false)
	if err != nil || len(preview) != 4 || !strings.Contains(preview[0].SQL, "tests_post_tags") || !strings.Contains(preview[3].SQL, "replacement_posts_tags") {
		t.Fatal("old and new physical histories were merged", preview, err)
	}
	if err := engine.Apply(ctx, change.Key()); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || !reflect.DeepEqual(tables, []string{"gogo_migrations", "replacement_posts", "replacement_posts_tags", "tests_tag"}) {
		t.Fatal(tables, err)
	}
}

func TestAutomaticIntermediaryHistoricalSelfRelation(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	symmetric := true
	source := models.Schema{AppLabel: "tests", Name: "Node", Table: "custom_nodes", Fields: []models.Field{models.CharField("key", models.WithMaxLength(20)), models.ManyToManyField("neighbors", models.Relation{Target: "tests.Node", Symmetrical: &symmetric})}, PrimaryKey: []string{"key"}}
	source.Fields[1].Column = "custom_neighbors"
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	if err := engine.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.SQL(ctx, initial.Key(), true)
	if err != nil || len(preview) != 2 || !strings.Contains(preview[0].SQL, "custom_nodes_custom_neighbors") {
		t.Fatal(preview, err)
	}
	if err := engine.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal(err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || !reflect.DeepEqual(tables, []string{"gogo_migrations"}) {
		t.Fatal(tables, err)
	}
}

func TestAutomaticIntermediaryTransitionPreservesExplicitBridge(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	source, target := m2mSchemas()
	bridge := models.Schema{AppLabel: "tests", Name: "ExplicitLink", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("post", models.Relation{Target: source.Key()}), models.ForeignKeyField("tag", models.Relation{Target: target.Key()})}}
	source.Fields[2].Relation.Through = bridge.Key()
	source.Fields[2].Relation.ThroughFields = []string{"post", "tag"}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(source), migrations.CreateModel(target), migrations.CreateModel(bridge)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.DeleteModel(source)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := engine.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	preview, err := engine.SQL(ctx, change.Key(), false)
	if err != nil || len(preview) != 1 || strings.Contains(preview[0].SQL, bridge.DBTable()) {
		t.Fatal(preview, err)
	}
	if err := engine.Apply(ctx, change.Key()); err == nil || !strings.Contains(err.Error(), "2BP01") {
		t.Fatal("retained explicit bridge was not protected", err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 4 {
		t.Fatal(tables, err)
	}
	history, err := engine.History(ctx)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
}
