package orm

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestGroupedQueriesCloneKeysPredicatesAndNeverMaterializeOrMutateModels(t *testing.T) {
	b, base := updateFixture()
	keys := []string{"value"}
	bound := []int{1, 2}
	query := base.OrderBy("id").GroupBy(keys...).Having(Q("value__in", bound))
	keys[0] = "id"
	bound[0] = 100
	if query.selectAST.GroupBy[0] != "value" || query.selectAST.Having.Children[1].Value.([]int)[0] != 1 || len(query.selectAST.Order) != 0 || len(base.selectAST.GroupBy) != 0 {
		t.Fatal("group builder retained mutable state")
	}
	copy := query.Having(Q("value__gt", 0)).OrderBy("value")
	copy.selectAST.GroupBy[0] = "id"
	if query.selectAST.GroupBy[0] != "value" {
		t.Fatal("group query clones share keys")
	}
	ctx := context.Background()
	if _, err := query.All(ctx); err == nil {
		t.Fatal("groups materialized models")
	}
	if _, err := query.Get(ctx); err == nil {
		t.Fatal("group materialized one model")
	}
	if _, err := query.Update(ctx, map[string]any{"value": 1}); err == nil {
		t.Fatal("grouped Update accepted")
	}
	if _, err := query.Delete(ctx); err == nil || b.begins != 0 {
		t.Fatal("grouped Delete began transaction", err)
	}
	if _, _, err := query.SQLContext(ctx); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("dialect grouping capability ignored", err)
	}
	for _, invalid := range []Query[*models.MapRecord]{base.GroupBy(), base.Having(Q("value", 1))} {
		if _, _, err := invalid.SQL(); err == nil {
			t.Fatal("invalid group/HAVING shape accepted")
		}
	}
}
