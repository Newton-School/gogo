package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type manyGroupingDialect struct{ updateDialect }

func (manyGroupingDialect) SupportsFeature(name string) bool {
	return name == "grouped_queries" || name == "grouped_relation_joins"
}

func manyGroupingQuery(t *testing.T, explicit bool) Query[*models.MapRecord] {
	t.Helper()
	root := models.Schema{AppLabel: "tests", Name: "Root", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	target := models.Schema{AppLabel: "tests", Name: "Target", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	bridge := models.Schema{AppLabel: "tests", Name: "Bridge", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("source", models.Relation{Target: root.Key(), OnDelete: models.Cascade}), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Cascade}), models.IntegerField("tenant")}}
	relation := models.Relation{Target: target.Key(), RelatedName: "roots"}
	if explicit {
		relation.Through = bridge.Key()
		relation.ThroughFields = []string{"source", "target"}
	}
	root.Fields = append(root.Fields, models.ManyToManyField("targets", relation))
	registry := &models.Registry{}
	for _, s := range []models.Schema{root, target, bridge} {
		if err := registry.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	b := &updateBackend{caps: db.Capabilities{"grouped_relation_joins": true}}
	store := New(decodingBackend{Backend: b, dialect: manyGroupingDialect{}}, registry)
	return For(store, func() *models.MapRecord { r, _ := models.NewRecord(root); return r }).Annotate(map[string]ResultExpression{"total": Typed(Count(F("targets__id")), models.BigIntegerField("out"))})
}

func TestManyToManyAggregateClonesBridgePredicatesBeforeLaterCallbacks(t *testing.T) {
	query := manyGroupingQuery(t, true)
	values := []int{33}
	query = query.WithScope(func(_ context.Context, s models.Schema) (db.Predicate, error) {
		if s.Name == "Bridge" {
			return Q("tenant__in", values), nil
		}
		if s.Name == "Target" {
			values[0] = 99
			return Q("tenant", 22), nil
		}
		return Q("tenant", 11), nil
	})
	sql, args, err := query.SQL()
	if err != nil || !reflect.DeepEqual(args, []any{22, 33, 11}) || !strings.Contains(sql, "INNER JOIN") {
		t.Fatal(sql, args, err)
	}
	if len(query.selectAST.Joins) != 0 || query.prepared {
		t.Fatal("terminal evaluation changed immutable builder")
	}
	prepared, err := query.prepareRelated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clone := prepared.clone()
	clone.selectAST.Joins[0].Through.Schema.Fields[1].Relation.TargetFields = append(clone.selectAST.Joins[0].Through.Schema.Fields[1].Relation.TargetFields, "changed")
	clone.selectAST.Joins[0].Through.Where.Value.([]int)[0] = 100
	clone.selectAST.Joins[0].Schema.Fields[0].Name = "changed"
	if len(prepared.selectAST.Joins[0].Through.Schema.Fields[1].Relation.TargetFields) != 0 || prepared.selectAST.Joins[0].Through.Where.Value.([]int)[0] != 99 || prepared.selectAST.Joins[0].Schema.Fields[0].Name != "id" {
		t.Fatal("prepared clone shares bridge or target metadata")
	}
}

func TestManyToManyAggregateRequiresBothBackendAndDialectCapabilities(t *testing.T) {
	query := manyGroupingQuery(t, true)
	query.store.Backend = decodingBackend{Backend: query.store.Backend, dialect: modelGroupingDialect{}}
	if _, _, err := query.SQL(); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("backend descriptor bypassed dialect capability", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := query.SQLContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost to provider rejection", err)
	}
}

func TestManyToManyAggregateRejectsUnprovenAutomaticIntermediaries(t *testing.T) {
	query := manyGroupingQuery(t, false)
	field, _ := query.schema.Field("targets")
	target, _ := query.store.Registry.Get(field.Relation.Target)
	bridge, err := models.ImplicitThrough(query.schema, field, target)
	if err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(bridge); err == nil {
		t.Fatal("caller-supplied automatic metadata became trusted provenance")
	}
	for _, s := range []models.Schema{query.schema, target} {
		if err := registry.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	query.store.Registry = registry
	if _, _, err := query.SQL(); err == nil {
		t.Fatal("unfrozen automatic intermediary was synthesized during query")
	}
}
