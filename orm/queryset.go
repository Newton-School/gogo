package orm

import (
	"fmt"

	"github.com/Newton-School/gogo/models"
)

// QueryMode describes queryset output mode.
type QueryMode string

const (
	QueryModeModels     QueryMode = "models"
	QueryModeValues     QueryMode = "values"
	QueryModeValuesList QueryMode = "values_list"
)

// QuerySetState stores queryset-only operation metadata.
type QuerySetState struct {
	Mode           QueryMode
	DateField      string
	DateKind       string
	DateOrder      string
	DateTimeZone   string
	OnlyFields     []string
	DeferredFields []string
}

func (s QuerySetState) clone() QuerySetState {
	s.OnlyFields = append([]string(nil), s.OnlyFields...)
	s.DeferredFields = append([]string(nil), s.DeferredFields...)
	return s
}

// QuerySet stores lazy query operations for a model.
type QuerySet struct {
	query    Query
	compiler Compiler
	using    string
	state    QuerySetState
}

// NewQuerySet creates a queryset for a model.
func NewQuerySet(meta models.Metadata, compiler Compiler) QuerySet {
	return QuerySet{
		query:    NewQuery(meta),
		compiler: compiler,
		using:    DefaultDatabase,
		state:    QuerySetState{Mode: QueryModeModels},
	}
}

// Query returns copied query state.
func (qs QuerySet) Query() Query {
	return qs.query.Clone()
}

// State returns copied queryset state.
func (qs QuerySet) State() QuerySetState {
	return qs.state.clone()
}

// UsingAlias returns the selected database alias.
func (qs QuerySet) UsingAlias() string {
	return qs.using
}

func (qs QuerySet) clone() QuerySet {
	qs.query = qs.query.Clone()
	qs.state = qs.state.clone()
	return qs
}

func (qs QuerySet) All() QuerySet {
	return qs.clone()
}

func (qs QuerySet) None() QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.None()
	return cloned
}

func (qs QuerySet) Filter(predicates ...Predicate) QuerySet {
	cloned := qs.clone()
	for _, predicate := range predicates {
		cloned.query = cloned.query.AddFilter(predicate)
	}
	return cloned
}

func (qs QuerySet) Exclude(predicates ...Predicate) QuerySet {
	cloned := qs.clone()
	for _, predicate := range predicates {
		cloned.query = cloned.query.AddExclude(predicate)
	}
	return cloned
}

func (qs QuerySet) OrderBy(ordering ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Order(ordering...)
	return cloned
}

func (qs QuerySet) Reverse() QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Reverse()
	return cloned
}

func (qs QuerySet) Distinct(fields ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.SetDistinct(true, fields...)
	return cloned
}

func (qs QuerySet) Union(other QuerySet) QuerySet {
	return qs.addSetOperation(SetUnion, other, false)
}

func (qs QuerySet) Intersection(other QuerySet) QuerySet {
	return qs.addSetOperation(SetIntersection, other, false)
}

func (qs QuerySet) Difference(other QuerySet) QuerySet {
	return qs.addSetOperation(SetDifference, other, false)
}

func (qs QuerySet) Values(fields ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Select(fields...)
	cloned.state.Mode = QueryModeValues
	return cloned
}

func (qs QuerySet) ValuesList(fields ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Select(fields...)
	cloned.state.Mode = QueryModeValuesList
	return cloned
}

func (qs QuerySet) Dates(field, kind, order string) QuerySet {
	cloned := qs.clone()
	cloned.state.DateField = field
	cloned.state.DateKind = kind
	cloned.state.DateOrder = order
	return cloned
}

func (qs QuerySet) DateTimes(field, kind, timezone, order string) QuerySet {
	cloned := qs.Dates(field, kind, order)
	cloned.state.DateTimeZone = timezone
	return cloned
}

func (qs QuerySet) Only(fields ...string) QuerySet {
	cloned := qs.clone()
	cloned.state.OnlyFields = append([]string(nil), fields...)
	return cloned
}

func (qs QuerySet) Defer(fields ...string) QuerySet {
	cloned := qs.clone()
	cloned.state.DeferredFields = append([]string(nil), fields...)
	return cloned
}

func (qs QuerySet) SelectRelated(paths ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.SelectRelated(paths...)
	return cloned
}

func (qs QuerySet) PrefetchRelated(paths ...string) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.PrefetchRelated(paths...)
	return cloned
}

func (qs QuerySet) Annotate(alias string, expression ExpressionRef) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Annotate(alias, expression)
	return cloned
}

func (qs QuerySet) Alias(alias string, expression ExpressionRef) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.Alias(alias, expression)
	return cloned
}

func (qs QuerySet) Aggregate(aggregates ...AggregateExpression) (CompiledSQL, error) {
	return qs.compiler.CompileAggregate(qs.query, aggregates...)
}

func (qs QuerySet) First() QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.LimitTo(1)
	return cloned
}

func (qs QuerySet) Last() QuerySet {
	return qs.Reverse().First()
}

func (qs QuerySet) Latest(field string) QuerySet {
	return qs.OrderBy("-" + field).First()
}

func (qs QuerySet) Earliest(field string) QuerySet {
	return qs.OrderBy(field).First()
}

func (qs QuerySet) Get(predicates ...Predicate) (QuerySet, error) {
	return qs.Filter(predicates...).limit(2), nil
}

func (qs QuerySet) InBulk(field string, values []any) QuerySet {
	return qs.Filter(Predicate{Field: field, Lookup: LookupIn, Value: values})
}

func (qs QuerySet) Create(values map[string]any) (CompiledSQL, error) {
	return qs.compiler.CompileInsert(qs.query.Model, values, nil)
}

func (qs QuerySet) GetOrCreate(lookup map[string]any, defaults map[string]any) (GetOrCreatePlan, error) {
	filtered := qs.filterExactMap(lookup).limit(2)
	get, err := filtered.Iterator()
	if err != nil {
		return GetOrCreatePlan{}, err
	}
	values := mergeMaps(lookup, defaults)
	create, err := qs.Create(values)
	if err != nil {
		return GetOrCreatePlan{}, err
	}
	return GetOrCreatePlan{Get: get, Create: create}, nil
}

func (qs QuerySet) UpdateOrCreate(lookup map[string]any, defaults map[string]any) (UpdateOrCreatePlan, error) {
	filtered := qs.filterExactMap(lookup).limit(2)
	get, err := filtered.Iterator()
	if err != nil {
		return UpdateOrCreatePlan{}, err
	}
	update, err := filtered.Update(defaults)
	if err != nil {
		return UpdateOrCreatePlan{}, err
	}
	create, err := qs.Create(mergeMaps(lookup, defaults))
	if err != nil {
		return UpdateOrCreatePlan{}, err
	}
	return UpdateOrCreatePlan{Get: get, Update: update, Create: create}, nil
}

func (qs QuerySet) BulkCreate(rows []map[string]any) ([]CompiledSQL, error) {
	compiled := make([]CompiledSQL, len(rows))
	for i, row := range rows {
		sql, err := qs.Create(row)
		if err != nil {
			return nil, err
		}
		compiled[i] = sql
	}
	return compiled, nil
}

func (qs QuerySet) BulkUpdate(pkField string, rows []map[string]any) ([]CompiledSQL, error) {
	compiled := make([]CompiledSQL, len(rows))
	for i, row := range rows {
		pk, ok := row[pkField]
		if !ok {
			return nil, fmt.Errorf("%w: missing primary key %s", ErrInvalidQuery, pkField)
		}
		values := make(map[string]any, len(row)-1)
		for key, value := range row {
			if key != pkField {
				values[key] = value
			}
		}
		sql, err := qs.Filter(Predicate{Field: pkField, Lookup: LookupExact, Value: pk}).Update(values)
		if err != nil {
			return nil, err
		}
		compiled[i] = sql
	}
	return compiled, nil
}

func (qs QuerySet) Update(values map[string]any) (CompiledSQL, error) {
	return qs.compiler.CompileUpdate(qs.query, values)
}

func (qs QuerySet) Delete() (CompiledSQL, error) {
	return qs.compiler.CompileDelete(qs.query)
}

func (qs QuerySet) Count() (CompiledSQL, error) {
	return qs.compiler.CompileCount(qs.query)
}

func (qs QuerySet) Exists() (CompiledSQL, error) {
	return qs.compiler.CompileExists(qs.query)
}

func (qs QuerySet) Contains(field string, value any) (CompiledSQL, error) {
	return qs.Filter(Predicate{Field: field, Lookup: LookupExact, Value: value}).Exists()
}

func (qs QuerySet) Using(alias string) QuerySet {
	cloned := qs.clone()
	cloned.using = alias
	return cloned
}

func (qs QuerySet) SelectForUpdate(lock LockState) QuerySet {
	cloned := qs.clone()
	lock.ForUpdate = true
	cloned.query = cloned.query.WithLock(lock)
	return cloned
}

func (qs QuerySet) ComplexFilter(expression Expression) QuerySet {
	if filter, ok := expression.(FilterExpression); ok {
		return qs.Filter(Predicate{Field: filter.Field, Lookup: filter.Lookup, Value: filter.Value})
	}
	return qs
}

func (qs QuerySet) Iterator() (CompiledSQL, error) {
	return qs.compiler.CompileSelect(qs.query)
}

func (qs QuerySet) Explain() (CompiledSQL, error) {
	compiled, err := qs.Iterator()
	if err != nil {
		return CompiledSQL{}, err
	}
	compiled.SQL = "EXPLAIN " + compiled.SQL
	return compiled, nil
}

func (qs QuerySet) Raw(sql string, args ...any) RawQuery {
	return RawQuery{SQL: sql, Args: append([]any(nil), args...)}
}

func (qs QuerySet) addSetOperation(kind SetOperationType, other QuerySet, all bool) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.AddSetOperation(SetOperation{Type: kind, Query: other.query.Clone(), All: all})
	return cloned
}

func (qs QuerySet) limit(limit int) QuerySet {
	cloned := qs.clone()
	cloned.query = cloned.query.LimitTo(limit)
	return cloned
}

func (qs QuerySet) filterExactMap(values map[string]any) QuerySet {
	filtered := qs
	for key, value := range values {
		filtered = filtered.Filter(Predicate{Field: key, Lookup: LookupExact, Value: value})
	}
	return filtered
}

// RawQuery stores raw query text and args.
type RawQuery struct {
	SQL    string
	Args   []any
	Unsafe bool
}

// GetOrCreatePlan stores compiled get/create statements.
type GetOrCreatePlan struct {
	Get    CompiledSQL
	Create CompiledSQL
}

// UpdateOrCreatePlan stores compiled get/update/create statements.
type UpdateOrCreatePlan struct {
	Get    CompiledSQL
	Update CompiledSQL
	Create CompiledSQL
}

func mergeMaps(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}
