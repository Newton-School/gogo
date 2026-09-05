package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresIdentifierLimitRejectsTruncatedIdentityCollisions(t *testing.T) {
	dialect := postgres.Dialect{}
	prefix := strings.Repeat("a", 63)
	if got, err := dialect.QuoteIdentifier(prefix); err != nil || got != `"`+prefix+`"` {
		t.Fatal("63-byte identifier rejected", err)
	}
	for _, name := range []string{prefix + "x", prefix + "y", strings.Repeat("a", 4096)} {
		if got, err := dialect.QuoteIdentifier(name); err == nil || got != "" || strings.Contains(err.Error(), name) {
			t.Fatal("overlong identifier accepted or exposed", err)
		}
	}
}

func TestPostgresAnnotationSharedLongPrefixFailsBeforeSQL(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	counted := &countedBackend{Backend: b}
	store := orm.New(counted, nil)
	schema := models.Schema{AppLabel: "tests", Name: "Identifier", Fields: []models.Field{models.BigAutoField("id")}}
	base := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r })
	prefix := strings.Repeat("a", 63)
	query := base.Annotate(map[string]orm.ResultExpression{
		prefix + "x": orm.Typed(orm.F("id"), models.BigIntegerField("out")),
		prefix + "y": orm.Typed(orm.F("id"), models.BigIntegerField("out")),
	}).OrderBy(prefix + "y")
	if _, err := query.All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("truncated alias collision reached SQL", err)
	}
	if _, err := query.DistinctOn(prefix+"y").Values(ctx, prefix+"x", prefix+"y"); err == nil || counted.queries.Load() != 0 {
		t.Fatal("truncated DISTINCT output identity reached SQL", err)
	}
}
