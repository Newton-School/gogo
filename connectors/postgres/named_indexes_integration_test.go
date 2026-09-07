package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func TestPostgresNamedIndexDetectApplyReplacementRollbackAndReverse(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "NamedIndex", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", models.WithColumn("stored_title"))}}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	indexed := schema.Clone()
	indexed.Indexes = []models.Index{{Name: "named_title", Fields: []string{"title"}, Include: []string{"id"}}}
	operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{indexed}, migrations.DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	add := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, add}}
	if statements, err := e.SQL(ctx, add.Key(), false); err != nil || len(statements) != 1 || !strings.Contains(statements[0].SQL, "COMMENT ON INDEX") || !strings.Contains(statements[0].SQL, "stored_title") {
		t.Fatal("atomic named index preview", statements, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	readIndex := func() (int64, bool, string) {
		t.Helper()
		var oid int64
		var unique bool
		var marker string
		if err := db.QueryRow(ctx, b, `SELECT c.oid::bigint,i.indisunique,COALESCE(obj_description(c.oid,'pg_class'),'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_index i ON i.indexrelid=c.oid WHERE n.nspname=current_schema() AND c.relname='named_title'`, nil, &oid, &unique, &marker); err != nil {
			t.Fatal(err)
		}
		return oid, unique, marker
	}
	original, unique, marker := readIndex()
	if unique || !strings.HasPrefix(marker, "gogo:named-index:v1:") {
		t.Fatal("created index lacks declared ownership", unique, marker)
	}
	if _, err := b.Exec(ctx, `INSERT INTO tests_namedindex(stored_title) VALUES ('same'),('same')`); err != nil {
		t.Fatal(err)
	}
	replacement := indexed.Clone()
	replacement.Indexes[0].Unique = true
	operations, err = migrations.Detect([]models.Schema{indexed}, []models.Schema{replacement}, migrations.DetectOptions{})
	if err != nil || len(operations) != 2 || operations[0].Kind != "remove_index" || operations[1].Kind != "add_index" {
		t.Fatal(operations, err)
	}
	change := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{add.Key()}, Operations: operations}
	e.Migrations = append(e.Migrations, change)
	if err := e.Apply(ctx, ""); err == nil {
		t.Fatal("duplicate rows accepted unique replacement")
	}
	if current, unique, currentMarker := readIndex(); current != original || unique || currentMarker != marker {
		t.Fatal("failed replacement did not restore the original index", current, unique, currentMarker)
	}
	if history, err := e.History(ctx); err != nil || len(history) != 2 {
		t.Fatal("failed replacement changed migration history", history, err)
	}
	if _, err := b.Exec(ctx, `UPDATE tests_namedindex SET stored_title='other' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if current, unique, currentMarker := readIndex(); current == original || !unique || currentMarker == marker {
		t.Fatal("replacement shape not installed", current, unique, currentMarker)
	}
	if err := e.Reverse(ctx, add.Key()); err != nil {
		t.Fatal(err)
	}
	if _, unique, currentMarker := readIndex(); unique || currentMarker != marker {
		t.Fatal("reverse did not restore prior declared definition", unique, currentMarker)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := db.QueryRow(ctx, b, `SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='named_title')`, nil, &present); err != nil || present {
		t.Fatal("reverse add retained named index", present, err)
	}
}

func TestPostgresNamedIndexColumnRenameRefreshesOwnership(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical", true: "logical_only"}[explicit], func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			field := models.TextField("title")
			if explicit {
				field.Column = "stored_title"
			}
			schema := models.Schema{AppLabel: "tests", Name: "NamedRename", Fields: []models.Field{models.BigAutoField("id"), field}, Indexes: []models.Index{{Name: "title_index", Fields: []string{"title"}}}}
			initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
			rename := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.RenameField(schema, "title", "heading")}}
			e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, rename}}
			if err := e.Apply(ctx, initial.Key()); err != nil {
				t.Fatal(err)
			}
			before := inspectFieldIndexes(t, b, schema.DBTable(), field.DBColumn())
			if err := e.Apply(ctx, rename.Key()); err != nil {
				t.Fatal(err)
			}
			state, err := e.State(rename.Key())
			if err != nil {
				t.Fatal(err)
			}
			renamed, _ := state[0].Field("heading")
			after := inspectFieldIndexes(t, b, schema.DBTable(), renamed.DBColumn())
			if len(before) != 1 || len(after) != 1 || before[0].oid != after[0].oid || (before[0].marker == after[0].marker) != explicit {
				t.Fatal("rename index identity/marker", before, after)
			}
			remove := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{rename.Key()}, Operations: []migrations.Operation{migrations.RemoveIndex(state[0], state[0].Indexes[0])}}
			e.Migrations = append(e.Migrations, remove)
			if err := e.Apply(ctx, ""); err != nil {
				t.Fatal("post-rename removal rejected refreshed ownership", err)
			}
			if err := e.Reverse(ctx, initial.Key()); err != nil {
				t.Fatal("remove and rename could not reverse", err)
			}
			if restored := inspectFieldIndexes(t, b, schema.DBTable(), field.DBColumn()); len(restored) != 1 || restored[0].marker != before[0].marker {
				t.Fatal("reverse did not restore index descriptor", restored)
			}
		})
	}
}

func TestPostgresNamedIndexExplicitColumnAlterationReverses(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "NamedColumn", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", models.WithColumn("old_title"))}, Indexes: []models.Index{{Name: "title_index", Fields: []string{"title"}}}}
	next := schema.Clone()
	next.Fields[1].Column = "new_title"
	operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{next}, migrations.DetectOptions{})
	if err != nil || len(operations) != 1 || operations[0].Kind != "alter_field" {
		t.Fatal(operations, err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	before := inspectFieldIndexes(t, b, schema.DBTable(), "old_title")
	if err := e.Apply(ctx, change.Key()); err != nil {
		t.Fatal(err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "new_title"); len(indexes) != 1 || indexes[0].oid != before[0].oid || indexes[0].marker == before[0].marker {
		t.Fatal("explicit column update did not preserve and refresh index", indexes)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal("reverse resolved old-column schema instead of current storage", err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "old_title"); len(indexes) != 1 || indexes[0] != before[0] {
		t.Fatal("explicit column reversal lost original descriptor", indexes)
	}
}

func TestPostgresNamedIndexMultipleColumnChangesUseSequentialSnapshots(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "NamedColumns", Fields: []models.Field{models.BigAutoField("id"), models.TextField("left"), models.TextField("right")}, Indexes: []models.Index{{Name: "two_columns_index", Fields: []string{"left", "right"}}}}
	next := schema.Clone()
	next.Fields[1].Column, next.Fields[2].Column = "stored_left", "stored_right"
	operations, err := migrations.Detect([]models.Schema{schema}, []models.Schema{next}, migrations.DetectOptions{})
	if err != nil || len(operations) != 2 || operations[1].Schema.Fields[1].DBColumn() != "stored_left" {
		t.Fatal("later field operation lost earlier storage change", operations, err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	observe := func() observedFieldIndex {
		t.Helper()
		var value observedFieldIndex
		if err := db.QueryRow(ctx, b, `SELECT c.oid::bigint,c.relname,obj_description(c.oid,'pg_class') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='two_columns_index'`, nil, &value.oid, &value.name, &value.marker); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := observe()
	if err := e.Apply(ctx, change.Key()); err != nil {
		t.Fatal("composite index could not follow both column changes", err)
	}
	if current := observe(); current.oid != before.oid || current.marker == before.marker {
		t.Fatal("multiple columns replaced index identity", current)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal("sequential reverse snapshots mismatched current columns", err)
	}
	if current := observe(); current != before {
		t.Fatal("sequential reversal did not restore original descriptor", current)
	}
}

func TestPostgresNamedIndexDriftNeverRemovesForeignDefinition(t *testing.T) {
	for _, mode := range []string{"unmarked", "missing", "descending", "predicate", "expression", "method", "unique", "included", "foreign_table", "constraint_owned"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			index := models.Index{Name: "guarded_named_index", Fields: []string{"title"}}
			schema := models.Schema{AppLabel: "tests", Name: "NamedDrift", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title")}, Indexes: []models.Index{index}}
			editor := b.SchemaEditor()
			if err := editor.CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			owned := inspectFieldIndexes(t, b, schema.DBTable(), "title")[0]
			if mode == "unmarked" {
				if _, err := b.Exec(ctx, "COMMENT ON INDEX guarded_named_index IS NULL"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := b.Exec(ctx, "DROP INDEX guarded_named_index"); err != nil {
					t.Fatal(err)
				}
				if mode != "missing" {
					sql := "CREATE INDEX guarded_named_index ON tests_nameddrift (title)"
					switch mode {
					case "descending":
						sql = "CREATE INDEX guarded_named_index ON tests_nameddrift (title DESC)"
					case "predicate":
						sql += " WHERE title<>''"
					case "expression":
						sql = "CREATE INDEX guarded_named_index ON tests_nameddrift (lower(title))"
					case "method":
						sql = "CREATE INDEX guarded_named_index ON tests_nameddrift USING hash (title)"
					case "unique":
						sql = "CREATE UNIQUE INDEX guarded_named_index ON tests_nameddrift (title)"
					case "included":
						sql += " INCLUDE(id)"
					case "foreign_table":
						if _, err := b.Exec(ctx, "CREATE TABLE foreign_named_table(title text)"); err != nil {
							t.Fatal(err)
						}
						sql = "CREATE INDEX guarded_named_index ON foreign_named_table (title)"
					case "constraint_owned":
						sql = "ALTER TABLE tests_nameddrift ADD CONSTRAINT guarded_named_index UNIQUE(title)"
					}
					if _, err := b.Exec(ctx, sql); err != nil {
						t.Fatal(err)
					}
					if _, err := b.Exec(ctx, "COMMENT ON INDEX guarded_named_index IS '"+owned.marker+"'"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := editor.(db.IndexLifecycleEditor).RemoveModelIndex(ctx, b, schema, index); err == nil {
				t.Fatal("foreign or missing definition accepted")
			}
			var present bool
			if err := db.QueryRow(ctx, b, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='guarded_named_index')`, nil, &present); err != nil || present != (mode != "missing") {
				t.Fatal("foreign index was removed", present, err)
			}
		})
	}
}

func TestPostgresNamedIndexPredicateNullSemanticsAndTemporaryShadow(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	noDistinct := false
	index := models.Index{Name: "named_expression", Fields: []string{"title"}, Include: []string{"id"}, Unique: true, NullsDistinct: &noDistinct, Condition: "title IS NULL OR title LIKE '%ready%'"}
	schema := models.Schema{AppLabel: "tests", Name: "NamedExpression", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		executor := db.ExecutorFor(ctx, b)
		for _, sql := range []string{"CREATE TEMP TABLE foreign_named_shadow(title text) ON COMMIT DROP", "CREATE INDEX named_expression ON foreign_named_shadow(title)", "COMMENT ON INDEX pg_temp.named_expression IS 'foreign temporary index'"} {
			if _, err := executor.Exec(ctx, sql); err != nil {
				return err
			}
		}
		if err := b.SchemaEditor().AddIndex(ctx, executor, schema, index); err != nil {
			return err
		}
		var unique, nullsNotDistinct bool
		var definition string
		if err := db.QueryRow(ctx, executor, `SELECT i.indisunique,i.indnullsnotdistinct,pg_get_indexdef(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_index i ON i.indexrelid=c.oid WHERE n.nspname=current_schema() AND c.relname='named_expression'`, nil, &unique, &nullsNotDistinct, &definition); err != nil {
			return err
		}
		if !unique || !nullsNotDistinct || !strings.Contains(definition, "%ready%") {
			t.Fatal("named predicate/null semantics lost", unique, nullsNotDistinct, definition)
		}
		if err := b.SchemaEditor().(db.IndexLifecycleEditor).RemoveModelIndex(ctx, executor, schema, index); err != nil {
			return err
		}
		var marker string
		if err := db.QueryRow(ctx, executor, `SELECT obj_description(c.oid,'pg_class') FROM pg_class c WHERE c.relnamespace=pg_my_temp_schema() AND c.relname='named_expression'`, nil, &marker); err != nil {
			return err
		}
		if marker != "foreign temporary index" {
			t.Fatal("temporary index was claimed or removed", marker)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
