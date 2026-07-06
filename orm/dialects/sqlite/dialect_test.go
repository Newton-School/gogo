package sqlite

import (
	"errors"
	"testing"

	"github.com/Newton-School/gogo/orm/dialects"
)

func TestSQLiteDialectRendering(t *testing.T) {
	dialect := New()
	if dialect.Name() != "sqlite" {
		t.Fatalf("Name() = %q", dialect.Name())
	}
	if got := dialect.Placeholder(3); got != "?" {
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
	if _, err := dialect.LockClause(dialects.LockOptions{ForUpdate: true}); !errors.Is(err, dialects.ErrUnsupportedFeature) {
		t.Fatalf("LockClause() error = %v, want ErrUnsupportedFeature", err)
	}
	if got, ok := dialect.ColumnType("json"); !ok || got != "text" {
		t.Fatalf("ColumnType(json) = (%q, %v)", got, ok)
	}
}

func TestSQLiteDateJSONAndSavepoints(t *testing.T) {
	dialect := New()
	dateSQL, err := dialect.DateExtract("year", `"created_at"`)
	if err != nil {
		t.Fatalf("DateExtract() error = %v", err)
	}
	if dateSQL != `CAST(strftime('%Y', "created_at") AS INTEGER)` {
		t.Fatalf("DateExtract() = %q", dateSQL)
	}
	jsonSQL, err := dialect.JSONLookup(`"data"`, []string{"owner", "email"})
	if err != nil {
		t.Fatalf("JSONLookup() error = %v", err)
	}
	if jsonSQL != `json_extract("data", '$.owner.email')` {
		t.Fatalf("JSONLookup() = %q", jsonSQL)
	}
	if got := dialect.RollbackToSavepointSQL("sp1"); got != `ROLLBACK TO SAVEPOINT "sp1"` {
		t.Fatalf("RollbackToSavepointSQL() = %q", got)
	}
}
