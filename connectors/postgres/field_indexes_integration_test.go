package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresFieldIndexRelationRenamesDetectApplyReverse(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "ZTarget", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code", models.UniqueValue)}}
	owner := models.Schema{AppLabel: "tests", Name: "AOwner", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}), models.ManyToManyField("targets", models.Relation{Target: target.Key(), TargetFields: []string{"code"}, Through: "tests.YLink", ThroughFields: []string{"owner", "target"}})}}
	link := models.Schema{AppLabel: "tests", Name: "YLink", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("owner", models.Relation{Target: owner.Key()}), models.ForeignKeyField("target", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}, dbIndexed)}}
	before := []models.Schema{owner, link, target}
	after := []models.Schema{owner.Clone(), link.Clone(), target.Clone()}
	after[0].Fields[1].Relation.TargetFields[0] = "key"
	after[0].Fields[2].Relation.TargetFields[0] = "key"
	after[0].Fields[2].Relation.ThroughFields[1] = "endpoint"
	after[1].Fields[2].Name = "endpoint"
	after[1].Fields[2].Relation.TargetFields[0] = "key"
	after[2].Fields[1].Name = "key"
	operations, err := migrations.Detect(before, after, migrations.DetectOptions{Renames: map[string]string{link.Key() + ".target": "endpoint", target.Key() + ".code": "key"}})
	if err != nil || len(operations) != 2 {
		t.Fatal("unexpected derived relation alteration", operations, err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(owner), migrations.CreateModel(link)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`INSERT INTO tests_ztarget(code) VALUES ('kept')`, `INSERT INTO tests_aowner(target) VALUES ('kept')`, `INSERT INTO tests_ylink(owner,target) VALUES (1,'kept')`} {
		if _, err := b.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	original := inspectFieldIndexes(t, b, link.DBTable(), "target")
	if len(original) != 1 {
		t.Fatal("initial bridge index missing", original)
	}
	readRelations := func(stateKey, column string) {
		t.Helper()
		state, err := e.State(stateKey)
		if err != nil {
			t.Fatal(err)
		}
		registry := &models.Registry{}
		for _, schema := range state {
			if err := registry.Register(schema); err != nil {
				t.Fatal(err)
			}
		}
		if err := registry.Freeze(); err != nil {
			t.Fatal(err)
		}
		definition, _ := registry.Get(owner.Key())
		record, err := models.NewRecord(definition)
		if err != nil {
			t.Fatal(err)
		}
		if err := record.Set("id", int64(1)); err != nil {
			t.Fatal(err)
		}
		store := orm.New(b, registry)
		for _, name := range []string{"target", "targets"} {
			manager := orm.RelationManager{Store: store, Source: record, Name: name}
			rows, err := manager.All(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatal("historical relation stopped resolving", name, err, len(rows))
			}
		}
		indexes := inspectFieldIndexes(t, b, link.DBTable(), column)
		if len(indexes) != 1 || indexes[0].oid != original[0].oid {
			t.Fatal("bridge index identity changed", indexes)
		}
	}
	readRelations(initial.Key(), "target")
	if statements, err := e.SQL(ctx, change.Key(), false); err != nil || len(statements) != 2 {
		t.Fatal("relation rename SQL preview", statements, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	readRelations(change.Key(), "endpoint")
	if statements, err := e.SQL(ctx, change.Key(), true); err != nil || len(statements) != 2 {
		t.Fatal("relation reverse SQL preview", statements, err)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	readRelations(initial.Key(), "target")
}

func dbIndexed(f *models.Field) { f.DBIndex = true }

type observedFieldIndex struct {
	name, marker string
	oid          int64
}

func inspectFieldIndexes(t *testing.T, b db.Executor, table, column string) []observedFieldIndex {
	t.Helper()
	rows, err := b.Query(context.Background(), `SELECT ic.relname,COALESCE(obj_description(ic.oid,'pg_class'),''),ic.oid::bigint FROM pg_index ix JOIN pg_class ic ON ic.oid=ix.indexrelid JOIN pg_class tc ON tc.oid=ix.indrelid JOIN pg_namespace ns ON ns.oid=tc.relnamespace JOIN pg_attribute a ON a.attrelid=tc.oid AND a.attnum=ix.indkey[0] WHERE tc.relname=$1 AND ns.nspname=current_schema() AND a.attname=$2 AND ix.indnatts=1 ORDER BY ic.relname`, table, column)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexes := []observedFieldIndex{}
	for rows.Next() {
		var i observedFieldIndex
		if err := rows.Scan(&i.name, &i.marker, &i.oid); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, i)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return indexes
}

func TestPostgresFieldIndexTemporaryNamespaceShadowIsNeverOwned(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "IndexShadow", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", dbIndexed)}}
	editor := b.SchemaEditor()
	if err := editor.CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	oldName := inspectFieldIndexes(t, b, schema.DBTable(), "title")[0].name
	if err := editor.RenameField(ctx, b, schema, "title", "heading"); err != nil {
		t.Fatal(err)
	}
	newName := inspectFieldIndexes(t, b, schema.DBTable(), "heading")[0].name
	renamed := schema.Clone()
	renamed.Fields[1].Name = "heading"
	if err := editor.RenameField(ctx, b, renamed, "heading", "title"); err != nil {
		t.Fatal(err)
	}
	disabled := schema.Fields[1]
	disabled.DBIndex = false
	if err := editor.AlterField(ctx, b, schema, schema.Fields[1], disabled); err != nil {
		t.Fatal(err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		executor := db.ExecutorFor(ctx, b)
		if _, err := executor.Exec(ctx, `CREATE TEMP TABLE unrelated_index_shadow(title text) ON COMMIT DROP`); err != nil {
			return err
		}
		for _, name := range []string{oldName, newName} {
			quoted, _ := b.Dialect().QuoteIdentifier(name)
			if _, err := executor.Exec(ctx, "CREATE INDEX "+quoted+" ON unrelated_index_shadow(title)"); err != nil {
				return err
			}
			if _, err := executor.Exec(ctx, "COMMENT ON INDEX pg_temp."+quoted+" IS 'externally owned temporary index'"); err != nil {
				return err
			}
		}
		assertForeign := func() error {
			for _, name := range []string{oldName, newName} {
				var marker string
				if err := db.QueryRow(ctx, executor, `SELECT obj_description(c.oid,'pg_class') FROM pg_class c WHERE c.relnamespace=pg_my_temp_schema() AND c.relname=$1`, []any{name}, &marker); err != nil {
					return err
				}
				if marker != "externally owned temporary index" {
					return fmt.Errorf("temporary index was remarked: %s", name)
				}
			}
			return nil
		}
		if err := editor.AlterField(ctx, executor, schema, disabled, schema.Fields[1]); err != nil {
			return err
		}
		if err := assertForeign(); err != nil {
			return err
		}
		if err := editor.RenameField(ctx, executor, schema, "title", "heading"); err != nil {
			return err
		}
		if indexes := inspectFieldIndexes(t, executor, schema.DBTable(), "heading"); len(indexes) != 1 || indexes[0].name != newName || !strings.HasPrefix(indexes[0].marker, "gogo:field-index:v1:") {
			return fmt.Errorf("current-schema index was not renamed and marked: %v", indexes)
		}
		if err := assertForeign(); err != nil {
			return err
		}
		cleared := renamed.Fields[1]
		cleared.DBIndex = false
		if err := editor.AlterField(ctx, executor, renamed, renamed.Fields[1], cleared); err != nil {
			return err
		}
		if indexes := inspectFieldIndexes(t, executor, schema.DBTable(), "heading"); len(indexes) != 0 {
			return fmt.Errorf("current-schema index was not dropped: %v", indexes)
		}
		if err := editor.AlterField(ctx, executor, renamed, cleared, renamed.Fields[1]); err != nil {
			return err
		}
		if _, err := executor.Exec(ctx, `CREATE TEMP TABLE tests_indexshadow(heading text) ON COMMIT DROP`); err != nil {
			return err
		}
		if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
			return editor.RenameField(ctx, db.ExecutorFor(ctx, b), renamed, "heading", "title")
		}); err == nil {
			return fmt.Errorf("temporary table shadow passed current-schema identity guard")
		}
		if indexes := inspectFieldIndexes(t, executor, schema.DBTable(), "heading"); len(indexes) != 1 || indexes[0].name != newName {
			return fmt.Errorf("table shadow changed persistent index: %v", indexes)
		}
		return assertForeign()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresFieldIndexesCreateToggleRenameAndReverse(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Indexed", Fields: []models.Field{models.BigAutoField("id", dbIndexed), models.TextField("title", dbIndexed), models.TextField("unique_name", models.UniqueValue, dbIndexed)}}
	first := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{first}}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"id", "unique_name"} {
		if indexes := inspectFieldIndexes(t, b, schema.DBTable(), column); len(indexes) != 1 || indexes[0].marker != "" {
			t.Fatal("redundant unique/PK index", column, indexes)
		}
	}
	indexes := inspectFieldIndexes(t, b, schema.DBTable(), "title")
	if len(indexes) != 1 || !strings.HasPrefix(indexes[0].marker, "gogo:field-index:v1:") {
		t.Fatal("missing owned index", indexes)
	}
	original := indexes[0]
	if _, err := b.Exec(ctx, `INSERT INTO tests_indexed(title,unique_name) VALUES('kept','one')`); err != nil {
		t.Fatal(err)
	}
	rename := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{first.Key()}, Operations: []migrations.Operation{migrations.RenameField(schema, "title", "heading")}}
	e.Migrations = append(e.Migrations, rename)
	if preview, err := e.SQL(ctx, rename.Key(), false); err != nil || len(preview) != 1 || !strings.Contains(preview[0].SQL, "obj_description") || !strings.Contains(preview[0].SQL, "ALTER INDEX") {
		t.Fatal("guarded rename preview", preview, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	indexes = inspectFieldIndexes(t, b, schema.DBTable(), "heading")
	if len(indexes) != 1 || indexes[0].oid != original.oid || indexes[0].name == original.name || indexes[0].marker == original.marker {
		t.Fatal("rename rebuilt/lost implicit index identity", indexes)
	}
	state, err := e.State("")
	if err != nil {
		t.Fatal(err)
	}
	heading, ok := state[0].Field("heading")
	if !ok || heading.Column != "" || !heading.DBIndex {
		t.Fatal("logical rename changed explicit column semantics", state)
	}
	disabled := heading
	disabled.DBIndex = false
	toggle := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{rename.Key()}, Operations: []migrations.Operation{migrations.AlterField(state[0], heading, disabled)}}
	e.Migrations = append(e.Migrations, toggle)
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "heading"); len(indexes) != 0 {
		t.Fatal("DBIndex false retained index", indexes)
	}
	if err := e.Reverse(ctx, rename.Key()); err != nil {
		t.Fatal(err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "heading"); len(indexes) != 1 {
		t.Fatal("reverse did not restore index", indexes)
	}
	if err := e.Reverse(ctx, first.Key()); err != nil {
		t.Fatal(err)
	}
	indexes = inspectFieldIndexes(t, b, schema.DBTable(), "title")
	if len(indexes) != 1 || indexes[0].name != original.name || indexes[0].marker != original.marker {
		t.Fatal("reverse rename did not restore descriptor identity", indexes)
	}
	var value string
	if err := db.QueryRow(ctx, b, `SELECT title FROM tests_indexed`, nil, &value); err != nil || value != "kept" {
		t.Fatal("index operations lost stored value", value, err)
	}
}

func TestPostgresFieldIndexOwnershipAndDefinitionDriftFailClosed(t *testing.T) {
	for _, mode := range []string{"unmarked", "missing", "descending", "partial", "opclass", "method", "collation", "expression", "included", "unique"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := models.Schema{AppLabel: "tests", Name: "IndexDrift", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", dbIndexed)}}
			editor := b.SchemaEditor()
			if err := editor.CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			owned := inspectFieldIndexes(t, b, schema.DBTable(), "title")[0]
			name, _ := b.Dialect().QuoteIdentifier(owned.name)
			if mode == "unmarked" {
				if _, err := b.Exec(ctx, "COMMENT ON INDEX "+name+" IS NULL"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := b.Exec(ctx, "DROP INDEX "+name); err != nil {
					t.Fatal(err)
				}
				if mode != "missing" {
					definition, prefix := "(title)", "CREATE INDEX "
					method := ""
					switch mode {
					case "descending":
						definition = "(title DESC)"
					case "partial":
						definition = "(title) WHERE title<>''"
					case "opclass":
						definition = "(title text_pattern_ops)"
					case "method":
						method = " USING hash"
					case "collation":
						definition = `(title COLLATE "C")`
					case "expression":
						definition = "(lower(title))"
					case "included":
						definition = "(title) INCLUDE (id)"
					case "unique":
						prefix = "CREATE UNIQUE INDEX "
					}
					if _, err := b.Exec(ctx, prefix+name+" ON tests_indexdrift"+method+" "+definition); err != nil {
						t.Fatal(err)
					}
					if _, err := b.Exec(ctx, "COMMENT ON INDEX "+name+" IS '"+owned.marker+"'"); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := schema.Fields[1]
			after := before
			after.DBIndex = false
			after.Column = "changed"
			if err := editor.AlterField(ctx, b, schema, before, after); err == nil {
				t.Fatal("drifted index was dropped/renamed")
			}
			var count int64
			if err := db.QueryRow(ctx, b, `SELECT count(*) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1 AND a.attname='title' AND NOT a.attisdropped`, []any{schema.DBTable()}, &count); err != nil || count != 1 {
				t.Fatal("ownership failure changed source column", count, err)
			}
			var exists bool
			if err := db.QueryRow(ctx, b, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1)`, []any{owned.name}, &exists); err != nil || exists != (mode != "missing") {
				t.Fatal(fmt.Sprint("foreign index was removed: ", mode), exists, err)
			}
		})
	}
}

func TestPostgresSlugIndexAddFieldMetadataOnlyAndTransactionalRollback(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "SlugIndex", Fields: []models.Field{models.BigAutoField("id"), models.SlugField("slug", models.WithColumn("stored_slug"))}}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	indexes := inspectFieldIndexes(t, b, schema.DBTable(), "stored_slug")
	if len(indexes) != 1 {
		t.Fatal("default slug index absent", indexes)
	}
	original := indexes[0]
	var typ string
	if err := db.QueryRow(ctx, b, `SELECT format_type(a.atttypid,a.atttypmod) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1 AND a.attname='stored_slug'`, []any{schema.DBTable()}, &typ); err != nil || typ != "character varying(50)" {
		t.Fatal("default slug database length", typ, err)
	}
	rename := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.RenameField(schema, "slug", "heading")}}
	e.Migrations = append(e.Migrations, rename)
	if statements, err := e.SQL(ctx, rename.Key(), false); err != nil || len(statements) != 0 {
		t.Fatal("logical custom-column rename touched storage", statements, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	state, err := e.State("")
	if err != nil {
		t.Fatal(err)
	}
	heading, _ := state[0].Field("heading")
	if heading.DBColumn() != "stored_slug" {
		t.Fatal("custom column lost", heading)
	}
	next := heading
	next.AllowUnicode = true
	next.Label = "Heading"
	next.Blank = true
	metadata := migrations.Migration{App: "tests", Name: "0003", Dependencies: []string{rename.Key()}, Operations: []migrations.Operation{migrations.AlterField(state[0], heading, next)}}
	e.Migrations = append(e.Migrations, metadata)
	if statements, err := e.SQL(ctx, metadata.Key(), false); err != nil || len(statements) != 0 {
		t.Fatal("validator metadata rewrote varchar/index", statements, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	indexes = inspectFieldIndexes(t, b, schema.DBTable(), "stored_slug")
	if len(indexes) != 1 || indexes[0] != original {
		t.Fatal("metadata update rebuilt storage", indexes)
	}
	state, err = e.State("")
	if err != nil {
		t.Fatal(err)
	}
	extra := models.TextField("extra", models.Nullable, dbIndexed)
	add := migrations.Migration{App: "tests", Name: "0004", Dependencies: []string{metadata.Key()}, Operations: []migrations.Operation{migrations.AddField(state[0], extra), migrations.RunSQL("SELECT 1/0", nil, "SELECT 1", nil)}}
	e.Migrations = append(e.Migrations, add)
	if err := e.Apply(ctx, ""); err == nil {
		t.Fatal("expected migration failure")
	}
	var exists bool
	if err := db.QueryRow(ctx, b, `SELECT EXISTS(SELECT 1 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1 AND a.attname='extra' AND NOT a.attisdropped)`, []any{schema.DBTable()}, &exists); err != nil || exists {
		t.Fatal("failed migration retained added column/index", exists, err)
	}
	if history, err := e.History(ctx); err != nil || len(history) != 3 {
		t.Fatal("failed index migration wrote history", history, err)
	}
	e.Migrations[3].Operations = e.Migrations[3].Operations[:1]
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "extra"); len(indexes) != 1 || !strings.HasPrefix(indexes[0].marker, "gogo:field-index:v1:") {
		t.Fatal("added field missing owned index", indexes)
	}
	if err := e.Reverse(ctx, metadata.Key()); err != nil {
		t.Fatal(err)
	}
	if indexes := inspectFieldIndexes(t, b, schema.DBTable(), "extra"); len(indexes) != 0 {
		t.Fatal("reversed add retained implicit index", indexes)
	}
}
