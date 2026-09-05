package orm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

type modelGroupingDialect struct{ updateDialect }

func (modelGroupingDialect) SupportsFeature(name string) bool { return name == "grouped_queries" }

func TestModelGroupingPreservesIdentityAcrossProjectionCopies(t *testing.T) {
	b, base := updateFixture()
	base.store.Backend = decodingBackend{Backend: b, dialect: modelGroupingDialect{}}
	query := base.Annotate(map[string]ResultExpression{"total": Typed(Sum(F("value")), models.BigIntegerField("out"))}).Having(Q("total__gt", 1))
	statement, args, err := query.Only("value").OrderBy("total").SQL()
	if err != nil || !strings.Contains(statement, `GROUP BY "id", "value", "payload"`) || len(args) != 1 {
		t.Fatal("model grouping lost stored identity keys", statement, args, err)
	}
	if !query.modelGrouping || base.modelGrouping || len(query.selectAST.GroupBy) != 0 {
		t.Fatal("terminal grouping mutated builder state")
	}
	grouped := query.GroupBy("value")
	if grouped.modelGrouping || !query.modelGrouping {
		t.Fatal("explicit grouping did not independently change result mode")
	}
	if _, err := grouped.All(context.Background()); err == nil {
		t.Fatal("explicit grouped values materialized models")
	}
	if _, err := query.Delete(context.Background()); err == nil || b.begins != 0 {
		t.Fatal("model aggregate deletion started before rejection", err)
	}
	if _, err := query.Update(context.Background(), map[string]any{"value": 2}); err == nil || b.begins != 0 {
		t.Fatal("model aggregate update started before rejection", err)
	}
	wide := base.schema.Clone()
	for i := 0; i < 65; i++ {
		wide.Fields = append(wide.Fields, models.IntegerField(fmt.Sprintf("extra_%d", i)))
	}
	bounded := For(base.store, func() *models.MapRecord { r, _ := models.NewRecord(wide); return r }).Annotate(map[string]ResultExpression{"total": Typed(Count(F("*")), models.BigIntegerField("out"))})
	if _, _, err := bounded.SQL(); err == nil {
		t.Fatal("unbounded model grouping accepted")
	}
}
