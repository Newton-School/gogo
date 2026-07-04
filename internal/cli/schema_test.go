package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/models"

	_ "modernc.org/sqlite"
)

func TestInspectDBPrintsExistingSchemaMetadata(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeSchemaTestEnv(t, dir, dbPath)
	db := openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE legacy_item (id integer PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	db.Close()
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"inspectdb", "--table", "legacy_item"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("inspectdb error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{`ModelName: "LegacyItem"`, `DBTable: "legacy_item"`, `Managed: &legacyItemManaged`, `field id INTEGER primary_key`, `field name TEXT`} {
		if !strings.Contains(output, want) {
			t.Fatalf("inspectdb output missing %q:\n%s", want, output)
		}
	}
}

func TestDiffSchemaReportsMissingColumnsAndPassesMatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeSchemaTestEnv(t, dir, dbPath)
	db := openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE blog_item (id integer PRIMARY KEY, name text NOT NULL)`); err != nil {
		t.Fatalf("create blog table: %v", err)
	}
	db.Close()
	t.Chdir(dir)

	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Item",
		TableName: "blog_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "name", Column: "name"},
			{Name: "slug", Column: "slug"},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}})
	var stdout bytes.Buffer
	err := root.Execute(context.Background(), []string{"diffschema", "--app", "blog"}, &stdout, &bytes.Buffer{})
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("diffschema error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "MISSING column blog_item.slug") {
		t.Fatalf("diffschema output = %q", stdout.String())
	}

	db = openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`DROP TABLE blog_item`); err != nil {
		t.Fatalf("drop blog table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE blog_item (id integer PRIMARY KEY, name text NOT NULL, slug text NOT NULL)`); err != nil {
		t.Fatalf("create matching blog table: %v", err)
	}
	db.Close()
	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"diffschema", "--app", "blog"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("diffschema match error = %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "schema matches model metadata") {
		t.Fatalf("diffschema match output = %q", stdout.String())
	}
}

func TestDiffSchemaReportsTypeAndDefaultMismatches(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeSchemaTestEnv(t, dir, dbPath)
	db := openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE blog_item (id integer PRIMARY KEY, status integer DEFAULT 1 NOT NULL)`); err != nil {
		t.Fatalf("create blog table: %v", err)
	}
	db.Close()
	t.Chdir(dir)

	defaultValue := models.DefaultValue("draft")
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Item",
		TableName: "blog_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "status", Column: "status", Kind: "text", DBDefault: defaultValue},
		},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}})
	var stdout bytes.Buffer
	err := root.Execute(context.Background(), []string{"diffschema", "--app", "blog"}, &stdout, &bytes.Buffer{})
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("diffschema error = %v, want ErrCommandFailed", err)
	}
	output := stdout.String()
	for _, want := range []string{`TYPE mismatch blog_item.status`, `DEFAULT mismatch blog_item.status`} {
		if !strings.Contains(output, want) {
			t.Fatalf("diffschema output missing %q:\n%s", want, output)
		}
	}
}

func TestNormalizePostgresColumnKindPreservesCatalogFormattedTypes(t *testing.T) {
	tests := map[string]string{
		"timestamp with time zone":    "timestamptz",
		"timestamp without time zone": "timestamp",
		"character varying(255)":      "varchar(255)",
		"numeric(20, 6)":              "numeric(20,6)",
		"vector(384)":                 "vector(384)",
		"jsonb":                       "jsonb",
		"uuid":                        "uuid",
		"double precision":            "double precision",
	}
	for input, want := range tests {
		if got := normalizePostgresColumnKind(input, 0, 0, 0); got != want {
			t.Fatalf("normalizePostgresColumnKind(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDiffSchemaReportsMissingIndexes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeSchemaTestEnv(t, dir, dbPath)
	db := openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE blog_item (id integer PRIMARY KEY, slug text NOT NULL)`); err != nil {
		t.Fatalf("create blog table: %v", err)
	}
	db.Close()
	t.Chdir(dir)

	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Item",
		TableName: "blog_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "slug", Column: "slug", Kind: "text"},
		},
		Indexes: []models.Index{{Name: "idx_blog_item_slug", Fields: []models.IndexField{models.Asc("slug")}}},
	}
	root := NewRootWithOptions(RootOptions{ProjectModels: []models.Metadata{meta}})
	var stdout bytes.Buffer
	err := root.Execute(context.Background(), []string{"diffschema", "--app", "blog"}, &stdout, &bytes.Buffer{})
	if !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("diffschema error = %v, want ErrCommandFailed", err)
	}
	if !strings.Contains(stdout.String(), "MISSING index blog_item.idx_blog_item_slug") {
		t.Fatalf("diffschema output = %q", stdout.String())
	}

	db = openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE INDEX idx_blog_item_slug ON blog_item (slug)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	db.Close()
	stdout.Reset()
	if err := root.Execute(context.Background(), []string{"diffschema", "--app", "blog"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("diffschema match error = %v\n%s", err, stdout.String())
	}
}

func writeSchemaTestEnv(t *testing.T, dir, dbPath string) {
	t.Helper()
	writeTextFile(t, filepath.Join(dir, ".env"), "GOGO_SECRET_KEY=schema-secret\nDATABASE_URL=sqlite://"+filepath.ToSlash(dbPath)+"\n")
}

func openSchemaTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}
