package orm

import (
	"context"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// NewRouted opts into terminal-operation routing. Backend is deliberately an
// unbound, non-dispatching facade; use supported Store/Query operations instead
// of extracting it for services, migrations or raw SQL. The router and pools
// remain application-owned; this constructor acquires no connection.
func NewRouted(router *db.Router, registry *models.Registry) (*Store, error) {
	if router == nil || !router.Configured() {
		return nil, db.ErrRouting
	}
	frozen := *router
	return &Store{Backend: frozen.RoutingBackend(), Registry: registry, routing: &frozen}, nil
}

// Using returns a separate explicitly selected routed Store. Selection does
// not bypass placement permission. Legacy stores do not support this method.
func (s *Store) Using(alias string) *Store {
	if s == nil {
		return &Store{routingErr: db.ErrRouting}
	}
	copy := *s
	if s.routing == nil || len(alias) == 0 || len(alias) > 128 || !models.ValidIdentifier(alias) {
		copy.routingErr = db.ErrRouting
	}
	copy.routingAlias, copy.routeBinding = alias, nil
	if copy.routing != nil {
		copy.Backend = copy.routing.RoutingBackend()
	}
	return &copy
}
func (q Query[T]) Using(alias string) Query[T] {
	q = q.clone()
	q.store = q.store.Using(alias)
	return q
}

func (s *Store) refuseRouting() error {
	if s != nil && (s.routing != nil || s.routingErr != nil) {
		return db.ErrRouting
	}
	return nil
}
func (s *Store) routed() bool { return s != nil && (s.routing != nil || s.routingErr != nil) }

func routingSchema(schema models.Schema, write bool) error {
	if schema.Parent != "" || schema.Abstract || schema.Proxy || schema.Unmanaged {
		return db.ErrRouting
	}
	if write {
		for _, f := range schema.Fields {
			if f.Relation != nil || f.Kind == models.ForeignKey || f.Kind == models.OneToOne || f.Kind == models.ManyToMany {
				return db.ErrRouting
			}
		}
	}
	return nil
}

func (s *Store) bindRoute(ctx context.Context, schema models.Schema, operation db.RouteOperation, primary bool) (*Store, error) {
	if s == nil {
		return nil, db.ErrRouting
	}
	copy := *s
	if copy.routingErr != nil {
		return nil, db.ErrRouting
	}
	if copy.routing == nil {
		return &copy, nil
	}
	if e := routingSchema(schema, operation == db.RouteWrite); e != nil {
		return nil, e
	}
	if copy.routeBinding == nil {
		binding, e := copy.routing.Resolve(ctx, db.RouteRequest{Model: schema.Key(), Alias: copy.routingAlias, Operation: operation, PrimaryRequired: primary})
		if e != nil {
			return nil, e
		}
		copy.routeBinding = binding
	}
	copy.Backend = copy.routeBinding.Backend()
	if operation == db.RouteWrite {
		if e := copy.routeBinding.BeforeWrite(copy.routeBinding.Context()); e != nil {
			return nil, e
		}
	}
	return &copy, nil
}

// Atomic selects one primary transaction. Every model terminal inside still
// receives its own routing permission. Unmanaged or cross-alias ambient
// transactions are refused; legacy Store transactions retain db.Atomic behavior.
func (s *Store) Atomic(ctx context.Context, options db.AtomicOptions, fn func(context.Context) error) error {
	if s == nil || s.routingErr != nil || fn == nil {
		return db.ErrRouting
	}
	copy := *s
	if copy.routing == nil {
		return db.Atomic(ctx, copy.Backend, options, fn)
	}
	return copy.routing.Atomic(ctx, copy.routingAlias, options, func(txctx context.Context, _ *db.RouteBinding) error { return fn(txctx) })
}
func (s *Store) operationAtomic(ctx context.Context, options db.AtomicOptions, fn func(context.Context) error) error {
	if s.routeBinding != nil {
		return s.routeBinding.Atomic(ctx, options, fn)
	}
	return db.Atomic(ctx, s.Backend, options, fn)
}

// checkRoutingShape is method-free. It precedes scope/codec/provider calls,
// and runs again after scope. Opaque ordinary parameter payloads retain their
// existing ownership contract; the shared walker refuses concealed subqueries.
func (q Query[T]) checkRoutingShape() error {
	if !q.store.routed() {
		return nil
	}
	if q.store.routingErr != nil {
		return db.ErrRouting
	}
	if e := routingSchema(q.schema, false); e != nil {
		return e
	}
	if len(q.relatedPaths) > 0 || len(q.prefetches) > 0 || len(q.selectAST.Joins) > 0 {
		return db.ErrRouting
	}
	ast := q.clone().selectAST
	local := func(name string) bool {
		if name == "" || name == "*" || name == "self" {
			return true
		}
		if _, ok := q.schema.Field(name); ok {
			return true
		}
		for _, a := range ast.Aliases {
			if a.Alias == name {
				return true
			}
		}
		if root, _, ok := strings.Cut(name, "__"); ok {
			f, exists := q.schema.Field(root)
			return exists && f.Relation == nil && f.Kind != models.ForeignKey && f.Kind != models.OneToOne && f.Kind != models.ManyToMany
		}
		// Unknown names are left to the ordinary compiler, but never interpreted
		// as a relation path or sent to a relation resolver.
		return !strings.Contains(name, "__")
	}
	for _, names := range [][]string{ast.Fields, ast.GroupBy, ast.DistinctOn, ast.LockOf} {
		for _, name := range names {
			if !local(name) {
				return db.ErrRouting
			}
		}
	}
	for _, order := range ast.Order {
		if !local(order.Field) {
			return db.ErrRouting
		}
	}
	nodes := 0
	var predicate func(db.Predicate, int) error
	predicate = func(p db.Predicate, depth int) error {
		nodes++
		if nodes > 8192 || depth > 64 || !local(p.Field) {
			return db.ErrRouting
		}
		for _, child := range p.Children {
			if e := predicate(child, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := predicate(ast.Where, 0); e != nil {
		return e
	}
	if e := predicate(ast.Having, 0); e != nil {
		return e
	}
	return sqlcompiler.WalkQueryExpressions(&ast, func(e *db.Expression, _ bool, parameter bool) error {
		if sqlcompiler.IsSubqueryExpression(*e) {
			return db.ErrRouting
		}
		if parameter {
			return nil
		}
		if e.Kind == "field" && !local(e.Name) {
			return db.ErrRouting
		}
		if e.Filter != nil {
			if err := predicate(*e.Filter, 0); err != nil {
				return err
			}
		}
		for _, branch := range e.Branches {
			if err := predicate(branch.Condition, 0); err != nil {
				return err
			}
		}
		return nil
	})
}
func (q Query[T]) route(ctx context.Context, operation db.RouteOperation) (Query[T], context.Context, error) {
	if q.store.routed() && q.err != nil {
		return q, ctx, q.err
	}
	if e := q.checkRoutingShape(); e != nil {
		return q, ctx, e
	}
	if !q.store.routed() {
		return q, ctx, nil
	}
	q = q.clone()
	q.schema = q.schema.Clone()
	var e error
	q.store, e = q.store.bindRoute(ctx, q.schema, operation, q.selectAST.ForUpdate)
	if e == nil {
		ctx = q.store.routeBinding.Context()
	}
	return q, ctx, e
}
