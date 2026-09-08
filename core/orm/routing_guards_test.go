package orm

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type routingTripwireRecord struct{ calls *int }

func (r *routingTripwireRecord) Schema() models.Schema     { *r.calls++; return routedORMSchema() }
func (r *routingTripwireRecord) State() *models.State      { *r.calls++; return &models.State{} }
func (r *routingTripwireRecord) ModelState() *models.State { return r.State() }
func (r *routingTripwireRecord) Get(string) (any, error)   { *r.calls++; return 1, nil }
func (r *routingTripwireRecord) Set(string, any) error     { *r.calls++; return nil }

func TestRoutedUnsupportedTerminalsRefuseBeforeCallbacks(t *testing.T) {
	modes := []string{"sql", "sql_context", "prefetch_all", "prefetch_exists", "prefetch_values", "prefetch_aggregate", "related", "relation_predicate", "relation_order", "subquery", "get_create", "update_create", "bulk_create", "bulk_update", "store_delete", "query_delete", "collect", "execute", "unique", "constraints", "relation_validation", "relation_all", "relation_one", "relation_add", "relation_remove", "relation_clear", "relation_set"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			rules, scope, factories, recordCalls := 0, 0, 0, 0
			s, _, p, r, ctx, _ := routedORMFixture(t, routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error { rules++; return nil }})
			q := For(s, func() *models.MapRecord { factories++; row, _ := models.NewRecord(routedORMSchema()); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { scope++; return db.Predicate{}, nil })
			if factories != 1 {
				t.Fatal("builder fixture")
			}
			factories = 0
			record := &routingTripwireRecord{calls: &recordCalls}
			manager := RelationManager{Store: s, Source: record, Name: "owner"}
			var e error
			switch mode {
			case "sql":
				_, _, e = q.SQL()
			case "sql_context":
				_, _, e = q.SQLContext(ctx)
			case "prefetch_all":
				_, e = q.PrefetchRelated("children").All(ctx)
			case "prefetch_exists":
				_, e = q.PrefetchRelated("children").Exists(ctx)
			case "prefetch_values":
				_, e = q.PrefetchRelated("children").Values(ctx, "id")
			case "prefetch_aggregate":
				_, e = q.PrefetchRelated("children").Aggregate(ctx, map[string]ResultExpression{"n": Typed(Count(F("id")), models.BigIntegerField("n"))})
			case "related":
				_, e = q.SelectRelated("owner").All(ctx)
			case "relation_predicate":
				_, e = q.Filter(Q("owner__id", 1)).All(ctx)
			case "relation_order":
				_, e = q.OrderBy("owner__id").Count(ctx)
			case "subquery":
				expr := Exists(q)
				_, e = q.Filter(db.Predicate{Expression: &expr, Value: true}).All(ctx)
			case "get_create":
				_, _, e = q.GetOrCreate(ctx, UniqueKey{}, nil)
			case "update_create":
				_, _, e = q.UpdateOrCreate(ctx, UniqueKey{}, nil)
			case "bulk_create":
				_, e = BulkCreate(ctx, s, []*routingTripwireRecord{record}, BulkOptions{})
			case "bulk_update":
				_, e = BulkUpdate(ctx, s, []*routingTripwireRecord{record}, []string{"value"}, BulkOptions{})
			case "store_delete":
				_, e = s.Delete(ctx, record)
			case "query_delete":
				_, e = q.Delete(ctx)
			case "collect":
				_, e = (DeleteCollector{Store: s}).Collect(ctx, record)
			case "execute":
				_, e = (DeleteCollector{Store: s}).Execute(ctx, record)
			case "unique":
				e = s.ValidateUnique(ctx, record, nil)
			case "constraints":
				e = s.ValidateConstraints(ctx, record, nil)
			case "relation_validation":
				e = s.ValidateRelation(ctx, record, models.Field{})
			case "relation_all":
				_, e = manager.All(ctx)
			case "relation_one":
				_, e = manager.One(ctx)
			case "relation_add":
				e = manager.Add(ctx, record)
			case "relation_remove":
				e = manager.Remove(ctx, record)
			case "relation_clear":
				e = manager.Clear(ctx)
			case "relation_set":
				e = manager.Set(ctx, record)
			}
			if !errors.Is(e, db.ErrRouting) || rules != 0 || scope != 0 || factories != 0 || recordCalls != 0 || p.metadata != 0 || r.metadata != 0 || p.begins != 0 || r.begins != 0 {
				t.Fatal("unsupported terminal effects", mode, e, rules, scope, factories, recordCalls, p, r)
			}
		})
	}
}
func TestRoutedScopeCannotIntroduceRelationOrSubqueryWork(t *testing.T) {
	for _, mode := range []string{"relation", "subquery"} {
		t.Run(mode, func(t *testing.T) {
			_, _, p, r, ctx, q := routedORMFixture(t, routedORMRule{})
			calls := 0
			scoped := q.WithScope(func(context.Context, models.Schema) (db.Predicate, error) {
				calls++
				if mode == "relation" {
					return Q("owner__id", 1), nil
				}
				e := Exists(q)
				return db.Predicate{Expression: &e, Value: true}, nil
			})
			if _, e := scoped.All(ctx); !errors.Is(e, db.ErrRouting) || calls != 1 || p.poolQueries != 0 || r.poolQueries != 0 {
				t.Fatal("scope enlarged routed shape", e, calls, p, r)
			}
		})
	}
}
func TestRoutedPermissionAndConfigurationRefuseBeforeTerminalFactory(t *testing.T) {
	for _, mode := range []string{"deny", "unknown_alias", "invalid_alias", "query_error"} {
		t.Run(mode, func(t *testing.T) {
			rules, scope, factories := 0, 0, 0
			s, _, p, r, ctx, _ := routedORMFixture(t, routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error {
				rules++
				if mode == "deny" {
					return errors.New("private denial")
				}
				return nil
			}})
			q := For(s, func() *models.MapRecord { factories++; row, _ := models.NewRecord(routedORMSchema()); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { scope++; return db.Predicate{}, nil })
			factories = 0
			switch mode {
			case "unknown_alias":
				q = q.Using("missing")
			case "invalid_alias":
				q = q.Using("bad.alias")
			case "query_error":
				q.err = db.ErrRouting
			}
			if _, e := q.All(ctx); !errors.Is(e, db.ErrRouting) || scope != 0 || factories != 0 || p.poolQueries != 0 || r.poolQueries != 0 || p.metadata != 0 || r.metadata != 0 {
				t.Fatal(e, scope, factories, p, r)
			}
			if mode != "deny" && rules != 0 {
				t.Fatal("invalid query called routing policy", rules)
			}
		})
	}
}
func TestRoutedRefusesInheritedAndRelationalSaveBeforeHooks(t *testing.T) {
	for _, mode := range []string{"inheritance", "relation"} {
		t.Run(mode, func(t *testing.T) {
			hooks, grants := 0, 0
			s, _, p, r, ctx, _ := routedORMFixture(t, routedORMRule{allowFn: func(context.Context, db.RouteRequest, string) error { grants++; return nil }})
			s.BeforeSave = []SaveReceiver{func(context.Context, SaveEvent) error { hooks++; return nil }}
			schema := routedORMSchema()
			if mode == "inheritance" {
				schema.Parent = "tests.Parent"
			} else {
				schema.Fields = append(schema.Fields, models.ForeignKeyField("owner", models.Relation{Target: "tests.Parent", OnDelete: models.Cascade}))
			}
			row, e := models.NewRecord(schema)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.Save(ctx, row, SaveOptions{ForceInsert: true}); !errors.Is(e, db.ErrRouting) || hooks != 0 || grants != 0 || p.metadata != 0 || r.metadata != 0 {
				t.Fatal(e, hooks, grants, p, r)
			}
		})
	}
}

func TestRoutedInvalidStoreRefusesBeforeSaveOrRefreshModelCallbacks(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		s, _, p, r, ctx, _ := routedORMFixture(t, routedORMRule{})
		calls := 0
		record := &routingTripwireRecord{calls: &calls}
		s = s.Using("bad.alias")
		var e error
		if refresh {
			e = s.RefreshFromDB(ctx, record)
		} else {
			e = s.Save(ctx, record, SaveOptions{ForceInsert: true})
		}
		if !errors.Is(e, db.ErrRouting) || calls != 0 || p.metadata != 0 || r.metadata != 0 {
			t.Fatal("invalid store invoked model callbacks", refresh, e, calls, p, r)
		}
	}
}
func TestRoutedSaveDoesNotTrustPublicBackendNil(t *testing.T) {
	s, _, p, r, ctx, _ := routedORMFixture(t, routedORMRule{})
	s.Backend = nil
	row, _ := models.NewRecord(routedORMSchema())
	row.Set("id", int64(1))
	row.Set("value", int64(3))
	row.Set("payload", nil)
	if e := s.Save(ctx, row, SaveOptions{ForceInsert: true}); e != nil || p.poolQueries != 1 || r.poolQueries != 0 {
		t.Fatal("public Backend overrode private routing configuration", e, p, r)
	}
}
