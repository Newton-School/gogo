package orm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func scopeSnapshotQuery(t *testing.T) Query[*models.MapRecord] {
	t.Helper()
	first := models.Schema{AppLabel: "tests", Name: "First", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	second := models.Schema{AppLabel: "tests", Name: "Second", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	root := models.Schema{AppLabel: "tests", Name: "Scoped", Fields: []models.Field{
		models.BigAutoField("id"), models.IntegerField("tenant"), models.JSONField("payload"),
		models.ForeignKeyField("first", models.Relation{Target: first.Key(), OnDelete: models.Cascade}),
		models.ForeignKeyField("second", models.Relation{Target: second.Key(), OnDelete: models.Cascade}),
	}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{root, first, second} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	return For(New(&updateBackend{}, registry), func() *models.MapRecord {
		record, _ := models.NewRecord(root)
		return record
	})
}

// The first Err after a scope return models another application-owned callback.
// It runs once so later ordinary cancellation checks remain deterministic.
type scopeSnapshotContext struct {
	context.Context
	after func()
}

func (c *scopeSnapshotContext) Err() error {
	if callback := c.after; callback != nil {
		c.after = nil
		callback()
	}
	return c.Context.Err()
}

func TestScopeSnapshotRootBeforeLaterTargetCallback(t *testing.T) {
	ids := []int{11}
	payload := map[string]any{"allowed": []int{11}}
	children := []db.Predicate{Q("tenant__in", ids), Q("payload", payload)}
	query := scopeSnapshotQuery(t).SelectRelated("first").WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Name == "Scoped" {
			return db.Predicate{Connector: "AND", Children: children}, nil
		}
		ids[0] = 91
		children[0].Value = []int{92}
		payload["allowed"].([]int)[0] = 93
		payload["added"] = true
		return Q("tenant", 22), nil
	})
	statement, args, err := query.SQL()
	if err != nil || !reflect.DeepEqual(args, []any{22, 11, `{"allowed":[11]}`}) || !strings.Contains(statement, "LEFT JOIN") {
		t.Fatal("later target changed the root scope", statement, args, err)
	}
}

func TestScopeSnapshotEagerTargetBeforeLaterTargetCallback(t *testing.T) {
	ids := []int{22}
	children := []db.Predicate{Q("tenant__in", ids)}
	query := scopeSnapshotQuery(t).SelectRelated("first", "second").WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		switch schema.Name {
		case "Scoped":
			return Q("tenant", 11), nil
		case "First":
			return db.Predicate{Connector: "AND", Children: children}, nil
		default:
			ids[0] = 91
			children[0].Value = []int{92}
			return Q("tenant", 33), nil
		}
	})
	statement, args, err := query.SQL()
	if err != nil || !reflect.DeepEqual(args, []any{22, 33, 11}) || strings.Count(statement, "LEFT JOIN") != 2 {
		t.Fatal("later target changed an earlier eager scope", statement, args, err)
	}
}

func TestScopeSnapshotRootAndEagerTargetBeforeContextCallback(t *testing.T) {
	for _, target := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "eager target"}[target], func(t *testing.T) {
			ctx := &scopeSnapshotContext{Context: context.Background()}
			ids := []int{22}
			query := scopeSnapshotQuery(t)
			if target {
				query = query.SelectRelated("first")
			}
			query = query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
				if schema.Name == "Scoped" && target {
					return Q("tenant", 11), nil
				}
				ctx.after = func() { ids[0] = 99 }
				return Q("tenant__in", ids), nil
			})
			statement, args, err := query.SQLContext(ctx)
			want := []any{22}
			if target {
				want = append(want, 11)
			}
			if err != nil || !reflect.DeepEqual(args, want) || ids[0] != 99 {
				t.Fatal("context callback changed scope before its snapshot", statement, args, err)
			}
		})
	}
}

func TestScopeSnapshotAggregateScopesBeforeContextCallback(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggregate target", true: "explicit bridge"}[bridge], func(t *testing.T) {
			ctx := &scopeSnapshotContext{Context: context.Background()}
			ids := []int{33}
			query := manyGroupingQuery(t, bridge).WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
				if schema.Name == "Root" {
					return Q("tenant", 11), nil
				}
				if schema.Name == "Bridge" || !bridge {
					ctx.after = func() { ids[0] = 99 }
					return Q("tenant__in", ids), nil
				}
				return Q("tenant", 22), nil
			})
			statement, args, err := query.SQLContext(ctx)
			want := []any{33, 11}
			if bridge {
				want = []any{22, 33, 11}
			}
			if err != nil || !reflect.DeepEqual(args, want) || ids[0] != 99 || !strings.Contains(statement, "INNER JOIN") {
				t.Fatal("context callback changed aggregate scope before its snapshot", statement, args, err)
			}
		})
	}
}

func TestSnapshotPredicatePreservesPlainDataAndProviderOwnership(t *testing.T) {
	values := []int{7}
	payload := map[string]any{"ids": values}
	provider := &queryValuer{}
	predicate := And(Q("tenant__in", values), Q("payload", payload), Q("opaque", provider))
	snapshot := SnapshotPredicate(predicate)
	values[0] = 99
	payload["added"] = true
	predicate.Children[0].Field = "changed"
	if snapshot.Children[0].Field != "tenant" || snapshot.Children[0].Value.([]int)[0] != 7 || snapshot.Children[1].Value.(map[string]any)["ids"].([]int)[0] != 7 {
		t.Fatal("public snapshot retains mutable scope data", snapshot)
	}
	if _, exists := snapshot.Children[1].Value.(map[string]any)["added"]; exists {
		t.Fatal("public snapshot retains a source map")
	}
	if snapshot.Children[2].Value != provider || provider.calls != 0 {
		t.Fatal("snapshot copied or evaluated an opaque provider")
	}
}

func TestSnapshotPredicateInvalidTreeNeverBecomesUnscoped(t *testing.T) {
	cycle := []db.Predicate{{Connector: "AND"}}
	cycle[0].Children = cycle
	wide := db.Predicate{Connector: "AND", Children: make([]db.Predicate, 8193)}
	for _, predicate := range []db.Predicate{cycle[0], wide} {
		snapshot := SnapshotPredicate(predicate)
		if snapshot.Expression == nil || snapshot.Expression.Kind != "invalid_tree" {
			t.Fatal("invalid predicate lost its rejecting sentinel")
		}
		if _, _, err := scopeSnapshotQuery(t).Filter(snapshot).SQL(); err == nil {
			t.Fatal("invalid snapshot compiled as unrestricted SQL")
		}
	}
}
