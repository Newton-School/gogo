package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/migrations"
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
	for _, want := range []string{
		`var legacyItemManaged = false`,
		`var LegacyItemMetadata = models.Metadata{`,
		`ModelName: "LegacyItem"`,
		`DBTable: "legacy_item"`,
		`Managed: &legacyItemManaged`,
		`Fields: []models.FieldMeta{`,
		`{Name: "id", Column: "id", Kind: "bigint", ColumnTypes: map[string]string{"sqlite": "INTEGER"}, PrimaryKey: true}`,
		`{Name: "name", Column: "name", Kind: "text", ColumnTypes: map[string]string{"sqlite": "TEXT"}}`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("inspectdb output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "field id INTEGER") {
		t.Fatalf("inspectdb still emitted thin field output:\n%s", output)
	}
}

func TestInspectDBPrintsIndexesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.sqlite3")
	writeSchemaTestEnv(t, dir, dbPath)
	db := openSchemaTestDB(t, dbPath)
	if _, err := db.Exec(`CREATE TABLE legacy_item (id integer PRIMARY KEY, name text NOT NULL DEFAULT 'draft')`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_legacy_item_name ON legacy_item (name)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	db.Close()
	t.Chdir(dir)

	var stdout bytes.Buffer
	if err := NewRoot().Execute(context.Background(), []string{"inspectdb", "--table", "legacy_item"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("inspectdb error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		`DBDefault: models.DefaultSQL("'draft'")`,
		`Indexes: []models.Index{`,
		`{Name: "idx_legacy_item_name", Fields: []models.IndexField{models.Asc("name")}}`,
	} {
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

func TestTableSchemaFromMetadataConvertsAdvancedUniqueConstraintsToIndexes(t *testing.T) {
	meta := models.Metadata{
		AppLabel:  "blog",
		ModelName: "Item",
		TableName: "blog_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "bigint", PrimaryKey: true},
			{Name: "slug", Column: "slug", Kind: "text"},
			{Name: "deleted_at", Column: "deleted_at", Kind: "timestamptz", Null: true},
		},
		Constraints: []models.Constraint{
			models.Unique("uniq_blog_item_slug", "slug"),
			models.UniqueExpression("uniq_blog_item_lower_slug", "LOWER(slug)").
				WithCondition("deleted_at IS NULL"),
		},
	}

	schema := tableSchemaFromMetadata(meta, "blog_item")
	if len(schema.Indexes) != 1 {
		t.Fatalf("indexes = %#v, want one advanced unique index", schema.Indexes)
	}
	index := schema.Indexes[0]
	if index.Name != "uniq_blog_item_lower_slug" || !index.Unique || index.Expressions[0] != "LOWER(slug)" || index.ConditionSQL != "deleted_at IS NULL" {
		t.Fatalf("advanced unique index = %#v", index)
	}
	if len(schema.Constraints) != 1 || schema.Constraints[0].Name != "uniq_blog_item_slug" || schema.Constraints[0].Type != "unique" {
		t.Fatalf("constraints = %#v, want simple unique table constraint", schema.Constraints)
	}
}

func TestTableSchemaFromMetadataIncludesRelationForeignKeys(t *testing.T) {
	registry := models.NewRegistry()
	for _, meta := range []models.Metadata{
		{
			AppLabel:  "accounts",
			ModelName: "User",
			TableName: "accounts_user",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
			},
		},
		{
			AppLabel:  "blog",
			ModelName: "Post",
			TableName: "blog_post",
			Fields: []models.FieldMeta{
				{Name: "id", Column: "id", Kind: "uuid", PrimaryKey: true},
				{Name: "author", Column: "author_id", Kind: "uuid", RelationTarget: "accounts.User", DeleteBehavior: "cascade"},
			},
		},
	} {
		if err := registry.RegisterMetadata(meta); err != nil {
			t.Fatalf("RegisterMetadata(%s) error = %v", meta.Label(), err)
		}
	}
	post, _ := registry.Lookup("blog.Post")

	schema := tableSchemaFromMetadata(post, "blog_post", registry)
	if len(schema.Constraints) != 1 {
		t.Fatalf("constraints = %#v, want relation FK", schema.Constraints)
	}
	fk := schema.Constraints[0]
	if fk.Name != "fk_blog_post_author_id" || fk.Type != "foreign_key" || fk.Fields[0] != "author_id" || fk.ReferencesTable != "accounts_user" || fk.ReferencesColumns[0] != "id" || fk.OnDelete != "CASCADE" {
		t.Fatalf("relation foreign key = %#v", fk)
	}

	actual := migrations.TableSchema{
		Name: "blog_post",
		Columns: []migrations.ColumnSchema{
			{Name: "id", NormalizedKind: "uuid", PrimaryKey: true},
			{Name: "author_id", NormalizedKind: "uuid"},
		},
	}
	output := cliSchemaDiffOutput(migrations.CompareTableShape(schema, actual))
	if !strings.Contains(output, "MISSING constraint blog_post.fk_blog_post_author_id") {
		t.Fatalf("diff output = %s", output)
	}
}

func cliSchemaDiffOutput(diffs []migrations.SchemaDifference) string {
	lines := make([]string, len(diffs))
	for i, diff := range diffs {
		lines[i] = diff.String()
	}
	return strings.Join(lines, "\n")
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
