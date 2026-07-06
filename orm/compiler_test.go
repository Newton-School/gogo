package orm

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
	"github.com/Newton-School/gogo/orm/dialects/sqlite"
)

func TestCompileSelectGoldenSQL(t *testing.T) {
	query := NewQuery(testCompilerModel()).
		Select("id", "title").
		AddFilter(Predicate{Field: "active", Lookup: LookupExact, Value: true}).
		AddFilter(Predicate{Field: "title", Lookup: LookupContains, Value: "go"}).
		Order("-created_at").
		LimitTo(5).
		OffsetBy(10)

	pg, err := NewCompiler(postgres.New()).CompileSelect(query)
	if err != nil {
		t.Fatalf("postgres CompileSelect() error = %v", err)
	}
	if pg.SQL != `SELECT "id", "title" FROM "blog_post" WHERE "active" = $1 AND "title" LIKE $2 ORDER BY "created_at" DESC LIMIT 5 OFFSET 10` {
		t.Fatalf("postgres SQL = %q", pg.SQL)
	}
	if !reflect.DeepEqual(pg.Args, []any{true, "%go%"}) {
		t.Fatalf("postgres args = %#v", pg.Args)
	}

	sqliteSQL, err := NewCompiler(sqlite.New()).CompileSelect(query)
	if err != nil {
		t.Fatalf("sqlite CompileSelect() error = %v", err)
	}
	if sqliteSQL.SQL != `SELECT "id", "title" FROM "blog_post" WHERE "active" = ? AND "title" LIKE ? ORDER BY "created_at" DESC LIMIT 5 OFFSET 10` {
		t.Fatalf("sqlite SQL = %q", sqliteSQL.SQL)
	}
}

func TestCompileMutationsCountExistsAggregateAndSetOperations(t *testing.T) {
	compiler := NewCompiler(postgres.New())
	meta := testCompilerModel()

	inserted, err := compiler.CompileInsert(meta, map[string]any{"title": "Go", "active": true}, []string{"id"})
	if err != nil {
		t.Fatalf("CompileInsert() error = %v", err)
	}
	if inserted.SQL != `INSERT INTO "blog_post" ("active", "title") VALUES ($1, $2) RETURNING "id"` {
		t.Fatalf("insert SQL = %q", inserted.SQL)
	}
	if !reflect.DeepEqual(inserted.Args, []any{true, "Go"}) {
		t.Fatalf("insert args = %#v", inserted.Args)
	}

	filtered := NewQuery(meta).AddFilter(Predicate{Field: "id", Lookup: LookupExact, Value: int64(1)})
	updated, err := compiler.CompileUpdate(filtered, map[string]any{"title": "Updated"})
	if err != nil {
		t.Fatalf("CompileUpdate() error = %v", err)
	}
	if updated.SQL != `UPDATE "blog_post" SET "title" = $1 WHERE "id" = $2` {
		t.Fatalf("update SQL = %q", updated.SQL)
	}
	deleted, err := compiler.CompileDelete(filtered)
	if err != nil {
		t.Fatalf("CompileDelete() error = %v", err)
	}
	if deleted.SQL != `DELETE FROM "blog_post" WHERE "id" = $1` {
		t.Fatalf("delete SQL = %q", deleted.SQL)
	}
	counted, err := compiler.CompileCount(filtered)
	if err != nil {
		t.Fatalf("CompileCount() error = %v", err)
	}
	if counted.SQL != `SELECT COUNT(*) FROM "blog_post" WHERE "id" = $1` {
		t.Fatalf("count SQL = %q", counted.SQL)
	}
	exists, err := compiler.CompileExists(filtered)
	if err != nil {
		t.Fatalf("CompileExists() error = %v", err)
	}
	if exists.SQL != `SELECT EXISTS(SELECT 1 FROM "blog_post" WHERE "id" = $1 LIMIT 1)` {
		t.Fatalf("exists SQL = %q", exists.SQL)
	}
	aggregate, err := compiler.CompileAggregate(filtered, CountAll().As("total"))
	if err != nil {
		t.Fatalf("CompileAggregate() error = %v", err)
	}
	if aggregate.SQL != `SELECT COUNT(*) AS "total" FROM "blog_post" WHERE "id" = $1` {
		t.Fatalf("aggregate SQL = %q", aggregate.SQL)
	}

	union := NewQuery(meta).Select("id").AddSetOperation(SetOperation{Type: SetUnion, Query: NewQuery(meta).Select("id").AddFilter(Predicate{Field: "active", Lookup: LookupExact, Value: false})})
	compiledUnion, err := compiler.CompileSelect(union)
	if err != nil {
		t.Fatalf("CompileSelect(union) error = %v", err)
	}
	if compiledUnion.SQL != `SELECT "id" FROM "blog_post" UNION SELECT "id" FROM "blog_post" WHERE "active" = $1` {
		t.Fatalf("union SQL = %q", compiledUnion.SQL)
	}
}

func TestCompilerValidatesInvalidQueryState(t *testing.T) {
	compiler := NewCompiler(postgres.New())
	cases := []Query{
		NewQuery(testCompilerModel()).Select("missing"),
		NewQuery(testCompilerModel()).Order("-"),
		NewQuery(testCompilerModel()).Annotate("", ExpressionRef{SQL: "COUNT(*)"}),
		NewQuery(testCompilerModel()).AddJoin(Join{Path: "author", Type: JoinInner, Target: "auth.User"}).AddJoin(Join{Path: "author", Type: JoinLeft, Target: "accounts.User"}),
	}
	for _, query := range cases {
		if _, err := compiler.CompileSelect(query); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("CompileSelect(%#v) error = %v, want ErrInvalidQuery", query, err)
		}
	}
}

func TestCompilerReportsUnsupportedDialectFeature(t *testing.T) {
	query := NewQuery(testCompilerModel()).WithLock(LockState{ForUpdate: true})
	_, err := NewCompiler(sqlite.New()).CompileSelect(query)
	if !errors.Is(err, ErrUnsupportedDialectFeature) {
		t.Fatalf("CompileSelect() error = %v, want ErrUnsupportedDialectFeature", err)
	}
}

func testCompilerModel() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", PrimaryKey: true},
			{Name: "title"},
			{Name: "created_at"},
			{Name: "views"},
			{Name: "active"},
		},
	}
}
