// Package orm provides context-aware, immutable queries and model persistence.
package orm

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
	"strings"
	"time"
)

var ErrNotFound = errors.New("orm: object does not exist")
var ErrMultipleObjects = errors.New("orm: multiple objects returned")
var ErrNotUpdated = errors.New("orm: forced update did not affect a row")

type Store struct {
	Backend                   db.Backend
	Registry                  *models.Registry
	BeforeSave, AfterSave     []SaveReceiver
	BeforeDelete, AfterDelete []DeleteReceiver
	// Timezone supplies the current IANA time zone for date-scoped validation.
	// Nil uses UTC. It is consulted per operation, not cached across requests.
	Timezone func(context.Context) *time.Location
}

func New(backend db.Backend, registry *models.Registry) *Store {
	return &Store{Backend: backend, Registry: registry}
}

// Query snapshots built-in slices, maps and plain-data parameters. Custom
// provider values (Valuer/Marshaler or opaque structs) remain caller-owned and
// must not be mutated while a query using them can execute. They are never
// evaluated while building a query or copied by inspecting private state.
type Query[T models.Model] struct {
	store         *Store
	factory       func() T
	schema        models.Schema
	selectAST     db.Select
	err           error
	relatedPaths  []string
	joined        []joinedRelation
	scope         QueryScope
	prepared      bool
	prefetches    []Prefetch
	eagerLimit    int
	modelGrouping bool
}

func For[T models.Model](store *Store, factory func() T) Query[T] {
	q := Query[T]{store: store, factory: factory}
	if store == nil || store.Backend == nil || factory == nil {
		q.err = errors.New("orm: backend and model factory required")
		return q
	}
	model := factory()
	record, err := models.Bind(model)
	if err != nil {
		q.err = err
		return q
	}
	q.schema = record.Schema()
	q.selectAST.Table = q.schema.DBTable()
	for _, f := range q.schema.Fields {
		if f.IsStored() {
			q.selectAST.Fields = append(q.selectAST.Fields, f.Name)
		}
	}
	return q.OrderBy(q.schema.Ordering...)
}
func (q Query[T]) clone() Query[T] {
	q.selectAST.Fields = append([]string(nil), q.selectAST.Fields...)
	q.selectAST.Order = append([]db.Order(nil), q.selectAST.Order...)
	q.selectAST.DistinctOn = append([]string(nil), q.selectAST.DistinctOn...)
	q.selectAST.Projections = cloneProjections(q.selectAST.Projections)
	q.selectAST.Aliases = cloneProjections(q.selectAST.Aliases)
	q.selectAST.GroupBy = append([]string(nil), q.selectAST.GroupBy...)
	q.selectAST.Where = clonePredicate(q.selectAST.Where)
	q.selectAST.Having = clonePredicate(q.selectAST.Having)
	q.selectAST.Joins = append([]db.Join(nil), q.selectAST.Joins...)
	for i := range q.selectAST.Joins {
		q.selectAST.Joins[i].Schema = q.selectAST.Joins[i].Schema.Clone()
		q.selectAST.Joins[i].Where = clonePredicate(q.selectAST.Joins[i].Where)
		if through := q.selectAST.Joins[i].Through; through != nil {
			copy := *through
			copy.Schema = through.Schema.Clone()
			copy.Where = clonePredicate(through.Where)
			q.selectAST.Joins[i].Through = &copy
		}
	}
	q.selectAST.LockOf = append([]string(nil), q.selectAST.LockOf...)
	q.relatedPaths = append([]string(nil), q.relatedPaths...)
	q.joined = append([]joinedRelation(nil), q.joined...)
	var err error
	q.prefetches, err = clonePrefetches(q.prefetches, 0)
	if q.err == nil {
		q.err = err
	}
	return q
}
func (q Query[T]) Filter(predicates ...db.Predicate) Query[T] {
	q = q.clone()
	children := append([]db.Predicate{q.selectAST.Where}, predicates...)
	for i := range children {
		children[i] = clonePredicate(children[i])
	}
	q.selectAST.Where = db.Predicate{Connector: "AND", Children: children}
	return q
}
func (q Query[T]) Exclude(predicates ...db.Predicate) Query[T] {
	p := db.Predicate{Connector: "AND", Children: append([]db.Predicate(nil), predicates...), Negated: true}
	return q.Filter(p)
}
func (q Query[T]) OrderBy(names ...string) Query[T] {
	q = q.clone()
	q.selectAST.Order = nil
	for _, name := range names {
		q.selectAST.Order = append(q.selectAST.Order, db.Order{Field: strings.TrimPrefix(name, "-"), Desc: strings.HasPrefix(name, "-")})
	}
	return q
}
func (q Query[T]) Reverse() Query[T] {
	q = q.clone()
	for i := range q.selectAST.Order {
		q.selectAST.Order[i].Desc = !q.selectAST.Order[i].Desc
	}
	return q
}
func (q Query[T]) Limit(limit int) Query[T]   { q = q.clone(); q.selectAST.Limit = &limit; return q }
func (q Query[T]) Offset(offset int) Query[T] { q = q.clone(); q.selectAST.Offset = &offset; return q }
func (q Query[T]) Distinct() Query[T]         { q = q.clone(); q.selectAST.Distinct = true; return q }
func (q Query[T]) DistinctOn(fields ...string) Query[T] {
	q = q.clone()
	q.selectAST.DistinctOn = append([]string(nil), fields...)
	return q
}
func (q Query[T]) Only(fields ...string) Query[T] {
	q = q.clone()
	q.selectAST.Fields = append([]string(nil), fields...)
	for _, pk := range q.schema.PKFields() {
		present := false
		for _, name := range q.selectAST.Fields {
			present = present || name == pk.Name
		}
		if !present {
			q.selectAST.Fields = append(q.selectAST.Fields, pk.Name)
		}
	}
	return q
}
func (q Query[T]) Defer(fields ...string) Query[T] {
	q = q.clone()
	exclude := map[string]bool{}
	for _, name := range fields {
		if field, ok := q.schema.Field(name); !ok || !field.IsStored() {
			q.err = errors.New("orm: Defer requires stored model fields")
			return q
		}
		exclude[name] = true
	}
	q.selectAST.Fields = nil
	for _, f := range q.schema.Fields {
		if f.IsStored() && !exclude[f.Name] {
			q.selectAST.Fields = append(q.selectAST.Fields, f.Name)
		}
	}
	return q.Only(q.selectAST.Fields...)
}
func (q Query[T]) SelectForUpdate(noWait, skipLocked bool) Query[T] {
	q = q.clone()
	q.selectAST.ForUpdate = true
	q.selectAST.NoWait = noWait
	q.selectAST.SkipLocked = skipLocked
	return q
}

// SelectForUpdateOf explicitly limits row locks to self and/or selected to-one
// paths. SelectForUpdate never silently excludes joined tables from locking.
func (q Query[T]) SelectForUpdateOf(paths ...string) Query[T] {
	q = q.clone()
	q.selectAST.ForUpdate = true
	q.selectAST.LockOf = append([]string(nil), paths...)
	return q
}
func (q Query[T]) SelectForNoKeyUpdate() Query[T] {
	q = q.clone()
	q.selectAST.ForUpdate = true
	q.selectAST.NoKey = true
	return q
}
func (q Query[T]) SQL() (string, []any, error) {
	return q.SQLContext(context.Background())
}

// SQLContext resolves request-scoped predicates without executing a query.
func (q Query[T]) SQLContext(ctx context.Context) (string, []any, error) {
	q = q.snapshotReadSubqueries()
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if q.err != nil {
		return "", nil, q.err
	}
	if err := q.checkModelProjection(); err != nil {
		return "", nil, err
	}
	var err error
	q, err = q.prepareRelated(ctx)
	if err != nil {
		return "", nil, err
	}
	q, err = q.prepareAnnotations(ctx)
	if err != nil {
		return "", nil, err
	}
	q, err = q.prepareModelGrouping()
	if err != nil {
		return "", nil, err
	}
	q, err = q.prepareSubqueries(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(q.selectAST.GroupBy) > 0 && !q.modelGrouping {
		q.selectAST.Fields = append([]string(nil), q.selectAST.GroupBy...)
	}
	statement, args, err := sqlcompiler.Select(q.store.Backend.Dialect(), q.schema, q.selectAST)
	if err == nil {
		err = q.subqueryParameterLimit(args)
	}
	if canceled := ctx.Err(); canceled != nil {
		return "", nil, canceled
	}
	if err != nil {
		return "", nil, err
	}
	return statement, args, err
}
func (q Query[T]) Iterator(ctx context.Context) (*Iterator[T], error) {
	q = q.snapshotReadSubqueries()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.err != nil {
		return nil, q.err
	}
	if len(q.selectAST.GroupBy) > 0 && !q.modelGrouping {
		return nil, errors.New("orm: grouped results require Values, not model instances")
	}
	if err := q.checkModelProjection(); err != nil {
		return nil, err
	}
	if len(q.prefetches) > 0 {
		return nil, errors.New("orm: collection prefetch requires All; streaming prefetch needs an explicit chunked query")
	}
	if q.selectAST.ForUpdate && !db.InTransaction(ctx, q.store.Backend.Alias()) {
		return nil, errors.New("orm: SelectForUpdate requires Atomic")
	}
	q, err := q.prepareRelated(ctx)
	if err != nil {
		return nil, err
	}
	q, err = q.prepareAnnotations(ctx)
	if err != nil {
		return nil, err
	}
	q, err = q.prepareModelGrouping()
	if err != nil {
		return nil, err
	}
	q, err = q.prepareSubqueries(ctx)
	if err != nil {
		return nil, err
	}
	statement, args, err := sqlcompiler.Select(q.store.Backend.Dialect(), q.schema, q.selectAST)
	if err == nil {
		err = q.subqueryParameterLimit(args)
	}
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.ExecutorFor(ctx, q.store.Backend).Query(ctx, statement, args...)
	if nilSaveRows(rows) {
		if err == nil {
			err = errors.New("orm: backend returned no row reader")
		}
		return nil, errors.Join(err, ctx.Err())
	}
	iterator := &Iterator[T]{rows: rows, query: q, ctx: ctx, err: err}
	// A failed Query may still return a resource that belongs to its caller.
	// Install cleanup before consulting user-provided context callbacks.
	accepted := false
	defer func() {
		if !accepted {
			if cause := recover(); cause != nil {
				defer func() { _ = recover(); panic(cause) }()
			}
			iterator.Close()
		}
	}()
	if canceled := ctx.Err(); canceled != nil {
		iterator.err = errors.Join(iterator.err, canceled)
	}
	if iterator.err != nil {
		return nil, iterator.Close()
	}
	accepted = true
	return iterator, nil
}

type Iterator[T models.Model] struct {
	rows   db.Rows
	ctx    context.Context
	query  Query[T]
	value  T
	err    error
	closed bool
}

func (i *Iterator[T]) Next() bool {
	if i.closed || i.err != nil {
		return false
	}
	published := false
	defer func() {
		// Exhaustion, read/decoder failure and provider panics all release the
		// reader. Panics keep their existing propagation behavior.
		if !published {
			if cause := recover(); cause != nil {
				i.err = errors.Join(i.err, errors.New("orm: row reader panicked"))
				defer func() { _ = recover(); panic(cause) }()
			}
			i.Close()
		}
	}()
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	if !i.rows.Next() {
		return false
	}
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	model := i.query.factory()
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	record, err := models.Bind(model)
	if err != nil {
		i.err = errors.Join(i.err, err)
		return false
	}
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	values := make([]any, len(i.query.selectAST.Fields)+len(i.query.selectAST.Projections))
	dest := make([]any, len(values))
	for index := range values {
		dest[index] = &values[index]
	}
	if err := i.rows.Scan(dest...); err != nil {
		i.err = errors.Join(i.err, err)
		return false
	}
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	record.State().Deferred = map[string]bool{}
	for _, f := range i.query.schema.Fields {
		if f.IsStored() {
			record.State().Deferred[f.Name] = true
		}
	}
	for index, name := range i.query.selectAST.Fields {
		if strings.Contains(name, "__") && len(i.query.joined) > 0 {
			continue
		}
		f, _ := i.query.schema.Field(name)
		value, err := i.query.store.decodeField(f, values[index])
		if err != nil {
			i.err = errors.Join(i.err, err)
		}
		if canceled := i.ctx.Err(); canceled != nil {
			i.err = errors.Join(i.err, canceled)
		}
		if i.err != nil || i.closed {
			return false
		}
		if err := record.Set(name, value); err != nil {
			i.err = errors.Join(i.err, err)
		}
		if canceled := i.ctx.Err(); canceled != nil {
			i.err = errors.Join(i.err, canceled)
		}
		if i.err != nil || i.closed {
			return false
		}
	}
	if err := i.query.attachJoined(record, values); err != nil {
		i.err = errors.Join(i.err, err)
	}
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	record.State().Annotations = nil
	if len(i.query.selectAST.Projections) > 0 {
		record.State().Annotations = make(map[string]any, len(i.query.selectAST.Projections))
		for index, projection := range i.query.selectAST.Projections {
			value, err := i.query.store.decodeAnnotation(i.ctx, projection, values[len(i.query.selectAST.Fields)+index])
			if err != nil {
				i.err = errors.Join(i.err, err)
			}
			if canceled := i.ctx.Err(); canceled != nil {
				i.err = errors.Join(i.err, canceled)
			}
			if i.err != nil || i.closed {
				return false
			}
			record.State().Annotations[projection.Alias] = value
		}
	}
	record.State().Persisted = true
	record.State().Database = i.query.store.Backend.Alias()
	if canceled := i.ctx.Err(); canceled != nil {
		i.err = errors.Join(i.err, canceled)
	}
	if i.err != nil || i.closed {
		return false
	}
	i.value = model
	published = true
	return true
}
func (i *Iterator[T]) Value() T   { return i.value }
func (i *Iterator[T]) Err() error { return i.err }

// Close releases the reader once and retains every observed read, cleanup and
// cancellation error. Repeated calls return the remembered outcome.
func (i *Iterator[T]) Close() error {
	if i.closed {
		return i.err
	}
	i.closed = true
	// Finish each bounded cleanup step even if a provider panics, then propagate
	// the first panic unchanged. Store returned errors before the next callback
	// so later panics cannot erase them. Recovered callers still see failure.
	var firstPanic any
	step := func(callback func() error) {
		defer func() {
			if cause := recover(); cause != nil {
				if firstPanic == nil {
					firstPanic = cause
				}
				i.err = errors.Join(i.err, errors.New("orm: row reader cleanup panicked"))
			}
		}()
		if failure := callback(); failure != nil {
			i.err = errors.Join(i.err, failure)
		}
	}
	step(i.rows.Err)
	step(i.rows.Close)
	step(i.rows.Err)
	step(i.ctx.Err)
	if firstPanic != nil {
		panic(firstPanic)
	}
	return i.err
}

// All returns the successfully decoded prefix together with any terminal
// error. Callers must check err before treating that prefix as a query result.
func (q Query[T]) All(ctx context.Context) ([]T, error) {
	q = q.snapshotReadSubqueries()
	if len(q.prefetches) > 0 {
		return q.allPrefetched(ctx)
	}
	iterator, err := q.Iterator(ctx)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	result := []T{}
	for iterator.Next() {
		result = append(result, iterator.Value())
	}
	return result, iterator.Err()
}
func (q Query[T]) Get(ctx context.Context) (T, error) {
	var zero T
	rows, err := q.Limit(2).All(ctx)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrNotFound
	}
	if len(rows) > 1 {
		return zero, ErrMultipleObjects
	}
	return rows[0], nil
}
func (q Query[T]) First(ctx context.Context) (T, error) {
	var zero T
	rows, err := q.Limit(1).All(ctx)
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrNotFound
	}
	return rows[0], nil
}
func (q Query[T]) Last(ctx context.Context) (T, error) {
	if len(q.selectAST.Order) == 0 {
		names := []string{}
		for _, f := range q.schema.PKFields() {
			names = append(names, f.Name)
		}
		q = q.OrderBy(names...)
	}
	return q.Reverse().First(ctx)
}
func (q Query[T]) Count(ctx context.Context) (int64, error) {
	q = q.snapshotReadSubqueries()
	if q.err != nil {
		return 0, q.err
	}
	if q.selectAST.ForUpdate && !db.InTransaction(ctx, q.store.Backend.Alias()) {
		return 0, errors.New("orm: SelectForUpdate requires Atomic")
	}
	statement, args, err := q.SQLContext(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.QueryRow(ctx, db.ExecutorFor(ctx, q.store.Backend), "SELECT COUNT(*) FROM ("+statement+") AS gogo_count", args, &count)
	return count, err
}
func (q Query[T]) Exists(ctx context.Context) (bool, error) {
	q = q.clone()
	q.prefetches = nil
	_, err := q.First(ctx)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
func (q Query[T]) Values(ctx context.Context, fields ...string) ([]map[string]any, error) {
	q = q.snapshotReadSubqueries()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.err != nil {
		return nil, q.err
	}
	if q.selectAST.ForUpdate && !db.InTransaction(ctx, q.store.Backend.Alias()) {
		return nil, errors.New("orm: SelectForUpdate requires Atomic")
	}
	q = q.clone()
	q.prefetches = nil
	// Resolve explicit scoped joins before selecting Values columns. Eager
	// hydration columns must not leak into the default root-values projection.
	selected := append([]string(nil), q.selectAST.Fields...)
	if len(q.selectAST.GroupBy) > 0 && !q.modelGrouping {
		selected = append([]string(nil), q.selectAST.GroupBy...)
	}
	if len(fields) > 0 {
		selected = append([]string(nil), fields...)
	} else {
		for _, projection := range q.selectAST.Projections {
			selected = append(selected, projection.Alias)
		}
	}
	q, err := q.prepareRelated(ctx)
	if err != nil {
		return nil, err
	}
	q, err = q.prepareAnnotations(ctx)
	if err != nil {
		return nil, err
	}
	q, err = q.prepareModelGrouping()
	if err != nil {
		return nil, err
	}
	q, err = q.prepareSubqueries(ctx)
	if err != nil {
		return nil, err
	}
	aliases := map[string]db.Projection{}
	for _, alias := range q.selectAST.Aliases {
		aliases[alias.Alias] = alias
	}
	q.selectAST.Fields, q.selectAST.Projections = nil, nil
	for _, name := range selected {
		if alias, ok := aliases[name]; ok {
			q.selectAST.Projections = append(q.selectAST.Projections, alias)
		} else {
			q.selectAST.Fields = append(q.selectAST.Fields, name)
		}
	}
	outputs := make([]models.Field, len(q.selectAST.Fields))
	for i, name := range q.selectAST.Fields {
		outputs[i], err = sqlcompiler.SelectOutputField(q.schema, q.selectAST, name)
		if err != nil {
			return nil, err
		}
	}
	statement, args, err := sqlcompiler.Select(q.store.Backend.Dialect(), q.schema, q.selectAST)
	if err == nil {
		err = q.subqueryParameterLimit(args)
	}
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.ExecutorFor(ctx, q.store.Backend).Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(q.selectAST.Fields)+len(q.selectAST.Projections))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		record := map[string]any{}
		for i, name := range q.selectAST.Fields {
			value, err := q.store.decodeField(outputs[i], values[i])
			if err != nil {
				return nil, err
			}
			record[name] = value
		}
		for i, projection := range q.selectAST.Projections {
			value, err := q.store.decodeAnnotation(ctx, projection, values[len(q.selectAST.Fields)+i])
			if err != nil {
				return nil, err
			}
			record[projection.Alias] = value
		}
		results = append(results, record)
	}
	return results, rows.Err()
}

type Ref[V any] struct{ Name string }

func Field[V any](name string) Ref[V]    { return Ref[V]{Name: name} }
func (f Ref[V]) Eq(value V) db.Predicate { return db.Predicate{Field: f.Name, Value: value} }
func (f Ref[V]) GT(value V) db.Predicate {
	return db.Predicate{Field: f.Name, Lookup: "gt", Value: value}
}
func (f Ref[V]) In(values ...V) db.Predicate {
	return db.Predicate{Field: f.Name, Lookup: "in", Value: append([]V(nil), values...)}
}
func Q(name string, value any) db.Predicate {
	parts := strings.Split(name, "__")
	lookup := "exact"
	field := name
	if len(parts) >= 2 && isLookup(parts[len(parts)-1]) {
		lookup = parts[len(parts)-1]
		field = strings.Join(parts[:len(parts)-1], "__")
	}
	return db.Predicate{Field: field, Lookup: lookup, Value: value}
}
func isLookup(name string) bool {
	switch name {
	case "exact", "iexact", "gt", "gte", "lt", "lte", "isnull", "in", "range", "contains", "icontains", "startswith", "istartswith", "endswith", "iendswith", "regex", "iregex", "contained_by", "has_key", "has_keys", "has_any_keys":
		return true
	}
	return false
}
func And(predicates ...db.Predicate) db.Predicate {
	return db.Predicate{Connector: "AND", Children: append([]db.Predicate(nil), predicates...)}
}
func Or(predicates ...db.Predicate) db.Predicate {
	return db.Predicate{Connector: "OR", Children: append([]db.Predicate(nil), predicates...)}
}
func Not(predicate db.Predicate) db.Predicate {
	predicate.Negated = !predicate.Negated
	return predicate
}
func F(name string) db.Expression   { return db.Expression{Kind: "field", Name: name} }
func Value(value any) db.Expression { return db.Expression{Kind: "value", Value: value} }
func Func(name string, args ...db.Expression) db.Expression {
	return db.Expression{Kind: "function", Name: name, Args: append([]db.Expression(nil), args...)}
}
func Add(left, right db.Expression) db.Expression {
	return db.Expression{Kind: "binary", Name: "+", Args: []db.Expression{left, right}}
}
func (q Query[T]) String() string {
	sql, _, err := q.SQL()
	if err != nil {
		return fmt.Sprintf("Query(error=%v)", err)
	}
	return sql
}
