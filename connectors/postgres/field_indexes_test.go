package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type indexRecorder struct {
	db.Executor
	statements []string
}

func (r *indexRecorder) Exec(_ context.Context, sql string, _ ...any) (db.Result, error) {
	r.statements = append(r.statements, sql)
	return nil, nil
}
func indexedText(name string) models.Field {
	return models.TextField(name, func(f *models.Field) { f.DBIndex = true })
}

func TestImplicitFieldIndexNamesAndSuppression(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Indexed", Fields: []models.Field{models.BigAutoField("id"), indexedText("title")}}
	first, err := implicitFieldIndex(schema, schema.Fields[1])
	if err != nil || first == nil || len(first.name) > 63 {
		t.Fatal(first, err)
	}
	long := schema.Clone()
	long.Table = strings.Repeat("a", 63)
	long.Fields[1].Column = strings.Repeat("b", 63)
	a, err := implicitFieldIndex(long, long.Fields[1])
	if err != nil || len(a.name) != 63 {
		t.Fatal(a, err)
	}
	long.Fields[1].Column = strings.Repeat("b", 62) + "c"
	b, err := implicitFieldIndex(long, long.Fields[1])
	if err != nil || a.name == b.name {
		t.Fatal("truncated identifier collision", a, b, err)
	}
	for _, kind := range []string{"primary", "declared_primary", "unique", "one_to_one"} {
		t.Run(kind, func(t *testing.T) {
			s := schema.Clone()
			f := s.Fields[1]
			switch kind {
			case "primary":
				s.Fields[0].PrimaryKey = false
				f.PrimaryKey = true
			case "declared_primary":
				s.PrimaryKey = []string{f.Name}
			case "unique":
				f.Unique = true
			case "one_to_one":
				f.Kind = models.OneToOne
			}
			s.Fields[1] = f
			if index, err := implicitFieldIndex(s, f); err != nil || index != nil {
				t.Fatal("redundant scalar index", index, err)
			}
		})
	}
	// A multi-column primary key does not provide a standalone index for every
	// column, so requested individual indexes remain explicit.
	schema.PrimaryKey = []string{"id", "title"}
	if index, err := implicitFieldIndex(schema, schema.Fields[1]); err != nil || index == nil {
		t.Fatal("composite key incorrectly suppressed requested index", err)
	}
}

func TestImplicitFieldIndexPreflightAndMetadataOnlyAlterations(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Indexed", Fields: []models.Field{models.BigAutoField("id"), indexedText("title")}}
	editor := SchemaEditor{}
	index, _ := implicitFieldIndex(schema, schema.Fields[1])
	for _, kind := range []string{"index_name", "table_name", "collation", "tablespace", "unstored"} {
		t.Run(kind, func(t *testing.T) {
			s := schema.Clone()
			e := editor
			switch kind {
			case "index_name":
				s.Indexes = []models.Index{{Name: index.name, Fields: []string{"title"}}}
			case "table_name":
				e.schemas = map[string]models.Schema{"other.Other": {AppLabel: "other", Name: "Other", Table: index.name}}
			case "collation":
				s.Fields[1].Collation = "C"
			case "tablespace":
				s.Fields[1].Tablespace = "space"
			case "unstored":
				s.Fields[1].Kind = models.ManyToMany
				s.Fields[1].Relation = &models.Relation{Target: schema.Key()}
			}
			recorder := &indexRecorder{}
			if err := e.CreateModel(context.Background(), recorder, s); err == nil || len(recorder.statements) != 0 {
				t.Fatal("invalid implicit index produced DDL", err, recorder.statements)
			}
		})
	}
	old := schema.Fields[1]
	next := old
	next.AllowUnicode = true
	next.Label = "Changed label"
	next.Blank = true
	recorder := &indexRecorder{}
	if err := editor.AlterField(context.Background(), recorder, schema, old, next); err != nil || len(recorder.statements) != 0 {
		t.Fatal("validation-only metadata rewrote storage", err, recorder.statements)
	}
	for _, change := range []func(*models.Field){func(f *models.Field) { f.Unique = true }, func(f *models.Field) { f.Collation = "C" }, func(f *models.Field) { f.Tablespace = "space" }} {
		next := old
		change(&next)
		recorder := &indexRecorder{}
		if err := editor.AlterField(context.Background(), recorder, schema, old, next); !db.IsCode(err, db.UnsupportedFeature) || len(recorder.statements) != 0 {
			t.Fatal("unsupported index/constraint metadata was ignored", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.CreateModel(ctx, recorder, schema); err == nil || len(recorder.statements) != 0 {
		t.Fatal("canceled metadata operation recorded DDL", err)
	}
}

func TestImplicitFieldIndexIgnoresUnmanagedImplicitMetadata(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Indexed", Fields: []models.Field{models.BigAutoField("id"), indexedText("title")}}
	for _, kind := range []string{"unmanaged", "abstract", "proxy"} {
		t.Run(kind, func(t *testing.T) {
			other := models.Schema{AppLabel: "tests", Name: "Legacy", Tablespace: "externally_managed", Fields: []models.Field{models.BigAutoField("id"), indexedText("title")}}
			other.Unmanaged, other.Abstract, other.Proxy = kind == "unmanaged", kind == "abstract", kind == "proxy"
			editor := SchemaEditor{schemas: map[string]models.Schema{other.Key(): other}}
			recorder := &indexRecorder{}
			if err := editor.CreateModel(context.Background(), recorder, schema); err != nil || len(recorder.statements) != 2 {
				t.Fatal("unrelated non-created model blocked indexed DDL", err, recorder.statements)
			}
			index, _ := implicitFieldIndex(schema, schema.Fields[1])
			other.Table = index.name
			editor.schemas[other.Key()] = other
			recorder = &indexRecorder{}
			if err := editor.CreateModel(context.Background(), recorder, schema); err == nil || len(recorder.statements) != 0 {
				t.Fatal("external relation namespace collision was ignored", err, recorder.statements)
			}
		})
	}
}
