package orm_test

import (
	"context"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type replicaRule struct{}

func (replicaRule) Select(context.Context, db.RouteRequest) (string, error) { return "replica", nil }
func (replicaRule) Allow(_ context.Context, q db.RouteRequest, alias string) error {
	if q.Operation == db.RouteTransaction && alias == "primary" {
		return nil
	}
	if q.Model == "catalog.Item" && (alias == "primary" || alias == "replica") {
		return nil
	}
	return db.ErrRouting
}

// Compile-only composition: applications inject opened, correctly named
// providers and own their cleanup. Placement does not replace tenant policy.
func Example_orm_routingNewRouted() {
	example := func(ctx context.Context, primary, replica db.Backend, tenant int64) error {
		router, err := db.NewRouter(db.RouterConfig{Default: "primary", EnableReplicas: true, Databases: []db.RoutedDatabase{{Alias: "primary", Backend: primary}, {Alias: "replica", Primary: "primary", Backend: replica}}, Rules: []db.RoutingRule{replicaRule{}}})
		if err != nil {
			return err
		}
		store, err := orm.NewRouted(router, nil)
		if err != nil {
			return err
		}
		ctx, err = router.WithConsistency(ctx)
		if err != nil {
			return err
		}
		schema := models.Schema{AppLabel: "catalog", Name: "Item", Fields: []models.Field{models.BigAutoField("id"), models.BigIntegerField("tenant"), models.CharField("title")}}
		query := orm.For(store, func() *models.MapRecord { row, _ := models.NewRecord(schema); return row }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", tenant), nil })
		_, err = query.OrderBy("id").Limit(25).All(ctx)
		if err != nil {
			return err
		}
		return store.Atomic(ctx, db.AtomicOptions{Durable: true}, func(txctx context.Context) error {
			// Add application write/object authorization before a mutation. Even a
			// read here is pinned to primary and retains the tenant scope.
			_, err := query.Count(txctx)
			return err
		})
	}
	_ = example
}
