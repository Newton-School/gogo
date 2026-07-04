package postgres

import (
	"strings"
	"testing"

	"github.com/cybersaksham/gogo/orm/dialects"
)

func TestPostgresDialectRendering(t *testing.T) {
	dialect := New()
	if dialect.Name() != "postgres" {
		t.Fatalf("Name() = %q", dialect.Name())
	}
	if got := dialect.Placeholder(3); got != "$3" {
		t.Fatalf("Placeholder(3) = %q", got)
	}
	if got := dialect.QuoteIdent(`user"name`); got != `"user""name"` {
		t.Fatalf("QuoteIdent() = %q", got)
	}
	if !dialect.SupportsReturning() || !dialect.SupportsUpsert() {
		t.Fatalf("returning/upsert support not enabled")
	}
	if got := dialect.LimitOffset(dialects.LimitOffset{Limit: dialects.Int(10), Offset: dialects.Int(5)}); got != "LIMIT 10 OFFSET 5" {
		t.Fatalf("LimitOffset() = %q", got)
	}
	lock, err := dialect.LockClause(dialects.LockOptions{ForUpdate: true, Of: []string{"blog_post"}, SkipLocked: true})
	if err != nil {
		t.Fatalf("LockClause() error = %v", err)
	}
	if lock != `FOR UPDATE OF "blog_post" SKIP LOCKED` {
		t.Fatalf("LockClause() = %q", lock)
	}
	if got, ok := dialect.ColumnType("json"); !ok || got != "jsonb" {
		t.Fatalf("ColumnType(json) = (%q, %v)", got, ok)
	}
}

func TestPostgresDateJSONAndSavepoints(t *testing.T) {
	dialect := New()
	dateSQL, err := dialect.DateExtract("year", `"created_at"`)
	if err != nil {
		t.Fatalf("DateExtract() error = %v", err)
	}
	if dateSQL != `EXTRACT(YEAR FROM "created_at")` {
		t.Fatalf("DateExtract() = %q", dateSQL)
	}
	jsonSQL, err := dialect.JSONLookup(`"data"`, []string{"owner", "email"})
	if err != nil {
		t.Fatalf("JSONLookup() error = %v", err)
	}
	if jsonSQL != `"data" #> '{owner,email}'` {
		t.Fatalf("JSONLookup() = %q", jsonSQL)
	}
	if got := dialect.SavepointSQL("sp1"); got != `SAVEPOINT "sp1"` {
		t.Fatalf("SavepointSQL() = %q", got)
	}
	if got := dialect.ReleaseSavepointSQL("sp1"); got != `RELEASE SAVEPOINT "sp1"` {
		t.Fatalf("ReleaseSavepointSQL() = %q", got)
	}
}

func TestPostgresDialectColumnShapeIntrospectionSQL(t *testing.T) {
	sql := New().SchemaIntrospection().ColumnsSQL
	if sql == "" {
		t.Fatal("ColumnsSQL is empty")
	}
	for _, want := range []string{"pg_attribute", "pg_class", "pg_namespace", "pg_type", "format_type", "pg_get_expr", "table_schema", "table_name", "column_name", "formatted_type", "udt_name", "column_default", "collation_name", "identity", "primary_key", "ordinal_position"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("ColumnsSQL missing %q: %s", want, sql)
		}
	}
}

func TestPostgresDialectIndexAndConstraintIntrospectionSQL(t *testing.T) {
	introspection := New().SchemaIntrospection()
	for name, sql := range map[string]string{
		"IndexesSQL":     introspection.IndexesSQL,
		"ConstraintsSQL": introspection.ConstraintsSQL,
	} {
		if sql == "" {
			t.Fatalf("%s is empty", name)
		}
	}
	for _, want := range []string{"pg_index", "pg_class", "pg_get_indexdef", "index_name", "method", "is_unique", "is_primary", "condition_sql"} {
		if !strings.Contains(introspection.IndexesSQL, want) {
			t.Fatalf("IndexesSQL missing %q: %s", want, introspection.IndexesSQL)
		}
	}
	for _, want := range []string{"pg_constraint", "pg_get_constraintdef", "constraint_name", "constraint_type", "referenced_table", "referenced_columns", "check_sql", "condition_sql"} {
		if !strings.Contains(introspection.ConstraintsSQL, want) {
			t.Fatalf("ConstraintsSQL missing %q: %s", want, introspection.ConstraintsSQL)
		}
	}
}
