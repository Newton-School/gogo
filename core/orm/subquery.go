package orm

import (
	"context"
	"errors"
	"reflect"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// OuterRef refers to one local scalar field of the immediately enclosing
// SELECT. It is resolved only when that SELECT is compiled, never by executing
// an inner query. Aliases, relation paths and nested correlation are unsupported.
func OuterRef(name string) db.Expression { return db.Expression{Kind: "outer_ref", Name: name} }

// Subquery selects one intrinsic local field from inner.Limit(1). The inner
// query is frozen without running factories, scopes, codecs or backend methods.
// Its own scope is required when the outer query is scoped; scope is never
// inherited implicitly. Both queries must use the identical comparable backend
// and alias. Declare a nullable intrinsic output with Typed for an annotation:
// no matching inner row is SQL NULL even if the stored field is nonnullable.
func Subquery[T models.Model](inner Query[T], field string) db.Expression {
	return newSubquery(inner, field, false)
}

// Exists is a boolean expression, distinct from Query.Exists(ctx). It keeps
// inner filters and slicing but discards ordering and projection. The inner
// query executes inside the outer SQL statement, not as a second query.
func Exists[T models.Model](inner Query[T]) db.Expression { return newSubquery(inner, "", true) }

type subquerySource struct {
	sqlcompiler.UnpreparedQueryValue
	store  Store
	schema models.Schema
	query  db.Select
	scope  QueryScope
	field  string
	exists bool
	err    error
}

func newSubquery[T models.Model](inner Query[T], field string, exists bool) db.Expression {
	inner = inner.clone()
	source := &subquerySource{schema: inner.schema.Clone(), query: inner.selectAST, scope: inner.scope, field: field, exists: exists, err: inner.err}
	if inner.store == nil || nilSubqueryBackend(inner.store.Backend) {
		source.err = errors.New("orm: subquery backend is required")
	} else {
		source.store = *inner.store
	}
	if len(inner.relatedPaths) != 0 || len(inner.prefetches) != 0 || inner.prepared || inner.modelGrouping || len(inner.selectAST.Aliases) != 0 || len(inner.selectAST.Projections) != 0 || len(inner.selectAST.GroupBy) != 0 || len(inner.selectAST.Joins) != 0 || inner.selectAST.ForUpdate || inner.selectAST.Distinct || len(inner.selectAST.DistinctOn) != 0 {
		source.err = unsupportedQueryExpression("Inner subqueries support only local filters, ordering and slicing")
	}
	output := models.Field{Kind: models.Boolean}
	if exists {
		source.query.Fields, source.query.Order = nil, nil
	} else {
		var err error
		output, err = sqlcompiler.SubqueryOutput(source.schema, field)
		if err != nil {
			source.err = err
		}
		if source.query.Limit == nil || *source.query.Limit != 1 {
			source.err = unsupportedQueryExpression("Scalar Subquery requires explicit Limit(1)")
		}
		source.query.Fields = []string{field}
	}
	if source.query.Limit != nil {
		value := *source.query.Limit
		source.query.Limit = &value
	}
	if source.query.Offset != nil {
		value := *source.query.Offset
		source.query.Offset = &value
	}
	return db.Expression{Kind: "orm_subquery", Value: source, Output: &output}
}

func unsupportedQueryExpression(message string) error {
	return &db.Error{Code: db.UnsupportedFeature, Message: message}
}
func nilSubqueryBackend(backend db.Backend) bool {
	if backend == nil {
		return true
	}
	v := reflect.ValueOf(backend)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// The alias belongs to an operation, not to a later mutation of a caller's
// Store/configuration. Embedding retains optional backend behavior explicitly
// used below; the final SQL always executes through this frozen alias.
type subqueryReadBackend struct {
	db.Backend
	alias        string
	innerAliases map[*subquerySource]string
	needed       bool
}

func (b *subqueryReadBackend) Alias() string { return b.alias }
func (b *subqueryReadBackend) MaxParameters() int {
	if limiter, ok := b.Backend.(db.ParameterLimiter); ok {
		return limiter.MaxParameters()
	}
	return 500
}

// snapshotReadSubqueries precedes Context and provider callbacks. Scopes may
// themselves add an expression, so a scoped query freezes its selection too.
func (q Query[T]) snapshotReadSubqueries() Query[T] {
	if q.store.routed() {
		if e := q.checkRoutingShape(); e != nil {
			q.err = e
		}
		return q
	}
	if q.store == nil || q.err != nil {
		return q
	}
	if _, frozen := q.store.Backend.(*subqueryReadBackend); frozen {
		return q
	}
	q = q.clone()
	found := false
	seenSources := map[*subquerySource]bool{}
	sources := []*subquerySource{}
	err := sqlcompiler.WalkQueryExpressions(&q.selectAST, func(e *db.Expression, _ bool, parameter bool) error {
		if parameter && sqlcompiler.IsSubqueryExpression(*e) {
			return unsupportedQueryExpression("Query expressions cannot be encoded as annotation or parameter data")
		}
		found = found || sqlcompiler.IsSubqueryExpression(*e)
		if source, ok := e.Value.(*subquerySource); ok && source != nil && !seenSources[source] {
			seenSources[source] = true
			sources = append(sources, source)
		}
		return nil
	})
	if err != nil {
		q.err = err
		return q
	}
	if !found && q.scope == nil {
		return q
	}
	store := *q.store
	q.store, q.schema = &store, q.schema.Clone()
	if nilSubqueryBackend(store.Backend) {
		q.err = errors.New("orm: query backend is required")
		return q
	}
	// Capture the complete configuration before Alias invokes provider code.
	backend := store.Backend
	alias := backend.Alias()
	selection := &subqueryReadBackend{Backend: backend, alias: alias, innerAliases: map[*subquerySource]string{}, needed: found}
	q.store.Backend = selection
	for _, source := range sources {
		if !nilSubqueryBackend(source.store.Backend) {
			selection.innerAliases[source] = source.store.Backend.Alias()
		}
	}
	return q
}

func (q Query[T]) prepareSubqueries(ctx context.Context) (Query[T], error) {
	if q.store.routed() {
		return q, q.checkRoutingShape()
	}
	cache := map[*subquerySource]*db.Subquery{}
	owned := map[*db.Subquery]bool{}
	found := false
	err := sqlcompiler.WalkQueryExpressions(&q.selectAST, func(e *db.Expression, inner, parameter bool) error {
		if !sqlcompiler.IsSubqueryExpression(*e) {
			return nil
		}
		if parameter {
			return unsupportedQueryExpression("Query expressions cannot be bound as data")
		}
		if e.Kind == "outer_ref" {
			if !inner {
				return unsupportedQueryExpression("OuterRef requires an immediate outer SELECT")
			}
			return nil
		}
		found = true
		if inner {
			return unsupportedQueryExpression("Nested subqueries are not supported")
		}
		if e.Kind != "orm_subquery" {
			if e.Subquery == nil || !owned[e.Subquery] {
				return unsupportedQueryExpression("ORM subqueries require a lazy ORM query, not an unscoped raw SELECT")
			}
			return nil
		}
		source, ok := e.Value.(*subquerySource)
		if !ok || source == nil || e.Subquery != nil || len(e.Args) != 0 || e.Filter != nil || e.Window != nil || e.Distinct || len(e.Branches) != 0 {
			return unsupportedQueryExpression("Invalid lazy subquery expression")
		}
		if source.err != nil {
			return source.err
		}
		prepared, ok := cache[source]
		if !ok {
			outer, ok := q.store.Backend.(*subqueryReadBackend)
			if !ok {
				return errors.New("orm: subquery backend was not frozen before callbacks")
			}
			backend := source.store.Backend
			if nilSubqueryBackend(backend) || !reflect.TypeOf(backend).Comparable() || reflect.TypeOf(backend) != reflect.TypeOf(outer.Backend) || backend != outer.Backend {
				return unsupportedQueryExpression("Subqueries require the identical comparable backend")
			}
			if q.scope != nil && source.scope == nil {
				return unsupportedQueryExpression("A scoped outer query requires an explicitly scoped inner query")
			}
			alias, captured := outer.innerAliases[source]
			if !captured {
				return unsupportedQueryExpression("Subqueries must be supplied before scope callbacks run")
			}
			if outer.alias == "" || alias != outer.alias {
				return unsupportedQueryExpression("Subqueries require the same nonempty database alias")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := outer.Capabilities().Require("correlated_subqueries"); err != nil {
				return err
			}
			prepared = &db.Subquery{Schema: source.schema.Clone(), Query: cloneSubquerySelect(source.query, &cloneTree{}, 0), Field: source.field}
			if source.scope != nil {
				predicate, err := source.scope(ctx, source.schema.Clone())
				predicate = clonePredicate(predicate)
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				prepared.Query.Where = And(prepared.Query.Where, predicate)
			}
			cache[source], owned[prepared] = prepared, true
		}
		e.Value, e.Subquery = nil, prepared
		e.Kind = "subquery"
		if source.exists {
			e.Kind = "exists"
		}
		return nil
	})
	if err != nil {
		return q, err
	}
	if found {
		if backend, ok := q.store.Backend.(*subqueryReadBackend); ok {
			backend.needed = true
		}
	}
	if found && (q.modelGrouping || len(q.selectAST.GroupBy) != 0 || q.selectAST.ForUpdate || len(q.prefetches) != 0) {
		return q, unsupportedQueryExpression("Subqueries require ungrouped unlocked reads without prefetch hydration")
	}
	return q, ctx.Err()
}

func (q Query[T]) subqueryParameterLimit(args []any) error {
	backend, ok := q.store.Backend.(*subqueryReadBackend)
	if !ok || !backend.needed {
		return nil
	}
	maximum := backend.MaxParameters()
	if maximum < 1 || len(args) > maximum {
		return unsupportedQueryExpression("Query exceeds the selected backend parameter bound")
	}
	return nil
}

func cloneSubquerySelect(query db.Select, state *cloneTree, depth int) db.Select {
	if !state.enter(depth) {
		return db.Select{Where: invalidPredicate()}
	}
	query.Fields = append([]string(nil), query.Fields...)
	query.Order = append([]db.Order(nil), query.Order...)
	query.Where = clonePredicateAt(query.Where, state, depth+1)
	query.Having = clonePredicateAt(query.Having, state, depth+1)
	query.DistinctOn = append([]string(nil), query.DistinctOn...)
	query.GroupBy = append([]string(nil), query.GroupBy...)
	query.LockOf = append([]string(nil), query.LockOf...)
	if query.Limit != nil {
		value := *query.Limit
		query.Limit = &value
	}
	if query.Offset != nil {
		value := *query.Offset
		query.Offset = &value
	}
	query.Projections = append([]db.Projection(nil), query.Projections...)
	query.Aliases = append([]db.Projection(nil), query.Aliases...)
	for _, projections := range [][]db.Projection{query.Projections, query.Aliases} {
		for i := range projections {
			projections[i].Expression = cloneExpressionAt(projections[i].Expression, state, depth+1)
		}
	}
	// Joins are not supported in this slice. Preserve their presence so shape
	// validation rejects them; never silently turn a joined query into a local one.
	query.Joins = append([]db.Join(nil), query.Joins...)
	return query
}
