package postgres_test

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestPostgresNamedIndexPredicateOnlyColumnRenameFailsBeforeDDL(t *testing.T) {
	for _, mode := range []string{"rename", "explicit_column"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := models.Schema{AppLabel: "tests", Name: "PredicateRename", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title"), models.BooleanField("visible")}, Indexes: []models.Index{{Name: "visible_title", Fields: []string{"title"}, Condition: "visible"}}}
			editor := b.SchemaEditor()
			if err := editor.CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			var err error
			if mode == "rename" {
				err = editor.RenameField(ctx, b, schema, "visible", "published")
			} else {
				next := schema.Fields[2]
				next.Column = "published"
				err = editor.AlterField(ctx, b, schema, schema.Fields[2], next)
			}
			if !db.IsCode(err, db.UnsupportedFeature) {
				t.Fatal("predicate-only dependency did not fail before mutation", err)
			}
			if _, err := b.Exec(ctx, `INSERT INTO tests_predicaterename(title,visible) VALUES ('unchanged',true)`); err != nil {
				t.Fatal("rejected operation changed original column", err)
			}
			if err := editor.(db.IndexLifecycleEditor).RemoveModelIndex(ctx, b, schema, schema.Indexes[0]); err != nil {
				t.Fatal("rejected operation invalidated index ownership", err)
			}
		})
	}
}

func TestPostgresNamedIndexFieldRemovalRequiresExplicitLifecycle(t *testing.T) {
	for _, mode := range []string{"key", "included", "predicate"} {
		t.Run(mode, func(t *testing.T) {
			b := openTest(t)
			ctx := context.Background()
			schema := models.Schema{AppLabel: "tests", Name: "IndexedRemoval", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title"), models.BooleanField("visible")}, Indexes: []models.Index{{Name: "retained_title", Fields: []string{"title"}}}}
			removed := schema.Fields[1]
			switch mode {
			case "included":
				schema.Indexes[0].Include = []string{"visible"}
				removed = schema.Fields[2]
			case "predicate":
				schema.Indexes[0].Condition = "visible"
				removed = schema.Fields[2]
			}
			editor := b.SchemaEditor()
			if err := editor.CreateModel(ctx, b, schema); err != nil {
				t.Fatal(err)
			}
			if err := editor.RemoveField(ctx, b, schema, removed); !db.IsCode(err, db.UnsupportedFeature) {
				t.Fatal("field removal implicitly discarded declared index", err)
			}
			if _, err := b.Exec(ctx, `INSERT INTO tests_indexedremoval(title,visible) VALUES ('preserved',true)`); err != nil {
				t.Fatal("rejected removal changed schema", err)
			}
			if err := editor.(db.IndexLifecycleEditor).RemoveModelIndex(ctx, b, schema, schema.Indexes[0]); err != nil {
				t.Fatal("explicit lifecycle could not remove preserved index", err)
			}
			schema.Indexes = nil
			if err := editor.RemoveField(ctx, b, schema, removed); err != nil {
				t.Fatal("field could not be removed after index lifecycle", err)
			}
		})
	}
}

func TestPostgresNamedIndexDoesNotBlockNonstoredExplicitThroughRemoval(t *testing.T) {
	field := models.ManyToManyField("targets", models.Relation{Target: "tests.Target", Through: "tests.Link", ThroughFields: []string{"owner", "target"}})
	schema := models.Schema{AppLabel: "tests", Name: "Parent", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title"), field}, Indexes: []models.Index{{Name: "conditional_title", Fields: []string{"title"}, Condition: "title <> ''"}}}
	// A nil executor proves that removing explicit-through relation metadata
	// performs no parent-table DDL and leaves the separately owned bridge alone.
	if err := (postgres.SchemaEditor{}).RemoveField(context.Background(), nil, schema, field); err != nil {
		t.Fatal("unrelated parent index blocked nonstored relation removal", err)
	}
}

func TestPostgresNamedIndexNonmanagedSchemasPerformNoStorageIO(t *testing.T) {
	for _, kind := range []string{"unmanaged", "proxy", "abstract"} {
		t.Run(kind, func(t *testing.T) {
			schema := models.Schema{AppLabel: "tests", Name: "External", Fields: []models.Field{models.BigAutoField("id")}}
			schema.Unmanaged, schema.Proxy, schema.Abstract = kind == "unmanaged", kind == "proxy", kind == "abstract"
			index := models.Index{Name: "external_id", Fields: []string{"id"}}
			editor := postgres.SchemaEditor{}
			if err := editor.AddIndex(context.Background(), nil, schema, index); err != nil {
				t.Fatal("nonmanaged index creation reached storage", err)
			}
			if err := editor.RemoveModelIndex(context.Background(), nil, schema, index); err != nil {
				t.Fatal("nonmanaged index removal reached storage", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := editor.AddIndex(ctx, nil, schema, index); err != context.Canceled {
				t.Fatal("no-op hid caller cancellation", err)
			}
			if err := editor.RemoveModelIndex(ctx, nil, schema, index); err != context.Canceled {
				t.Fatal("no-op removal hid caller cancellation", err)
			}
		})
	}
}
