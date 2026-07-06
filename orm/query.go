package orm

import (
	"strings"

	"github.com/Newton-School/gogo/models"
)

// Lookup identifies a field lookup operation.
type Lookup string

const (
	LookupExact     Lookup = "exact"
	LookupIExact    Lookup = "iexact"
	LookupContains  Lookup = "contains"
	LookupIContains Lookup = "icontains"
	LookupIn        Lookup = "in"
	LookupGT        Lookup = "gt"
	LookupGTE       Lookup = "gte"
	LookupLT        Lookup = "lt"
	LookupLTE       Lookup = "lte"
	LookupRange     Lookup = "range"
	LookupStarts    Lookup = "startswith"
	LookupIStarts   Lookup = "istartswith"
	LookupEnds      Lookup = "endswith"
	LookupIEnds     Lookup = "iendswith"
	LookupDate      Lookup = "date"
	LookupYear      Lookup = "year"
	LookupMonth     Lookup = "month"
	LookupDay       Lookup = "day"
	LookupWeek      Lookup = "week"
	LookupWeekDay   Lookup = "week_day"
	LookupQuarter   Lookup = "quarter"
	LookupTime      Lookup = "time"
	LookupHour      Lookup = "hour"
	LookupMinute    Lookup = "minute"
	LookupSecond    Lookup = "second"
	LookupIsNull    Lookup = "isnull"
	LookupRegex     Lookup = "regex"
	LookupIRegex    Lookup = "iregex"
	LookupJSONPath  Lookup = "json_path"
)

// Predicate stores filter, exclude, and having query predicates.
type Predicate struct {
	Field   string
	Lookup  Lookup
	Value   any
	Negated bool
}

// JoinType identifies SQL join type.
type JoinType string

const (
	JoinInner JoinType = "inner"
	JoinLeft  JoinType = "left"
	JoinRight JoinType = "right"
)

// Join stores dialect-neutral join state.
type Join struct {
	Path   string
	Type   JoinType
	Target string
}

// ExpressionRef stores a SQL expression placeholder until expressions are implemented.
type ExpressionRef struct {
	SQL  string
	Args []any
}

func (e ExpressionRef) clone() ExpressionRef {
	e.Args = append([]any(nil), e.Args...)
	return e
}

// RelatedState stores select_related and prefetch_related paths.
type RelatedState struct {
	SelectRelated   []string
	PrefetchRelated []string
}

func (r RelatedState) clone() RelatedState {
	return RelatedState{
		SelectRelated:   append([]string(nil), r.SelectRelated...),
		PrefetchRelated: append([]string(nil), r.PrefetchRelated...),
	}
}

// LockState stores row locking query state.
type LockState struct {
	ForUpdate  bool
	NoWait     bool
	SkipLocked bool
	Of         []string
}

func (l LockState) clone() LockState {
	l.Of = append([]string(nil), l.Of...)
	return l
}

// SetOperationType identifies query set operations.
type SetOperationType string

const (
	SetUnion        SetOperationType = "union"
	SetIntersection SetOperationType = "intersection"
	SetDifference   SetOperationType = "difference"
)

// SetOperation stores UNION/INTERSECT/EXCEPT query state.
type SetOperation struct {
	Type  SetOperationType
	Query Query
	All   bool
}

func (s SetOperation) clone() SetOperation {
	s.Query = s.Query.Clone()
	return s
}

// WindowState stores window expression metadata.
type WindowState struct {
	Expression  string
	PartitionBy []string
	OrderBy     []string
	Frame       string
}

func (w WindowState) clone() WindowState {
	w.PartitionBy = append([]string(nil), w.PartitionBy...)
	w.OrderBy = append([]string(nil), w.OrderBy...)
	return w
}

// Query stores immutable dialect-neutral query state.
type Query struct {
	Model           models.Metadata
	SelectedColumns []string
	Filters         []Predicate
	Excludes        []Predicate
	Joins           []Join
	Ordering        []string
	Grouping        []string
	Having          []Predicate
	Limit           *int
	Offset          *int
	Distinct        bool
	DistinctFields  []string
	Annotations     map[string]ExpressionRef
	Aliases         map[string]ExpressionRef
	Related         RelatedState
	Locking         LockState
	SetOperations   []SetOperation
	Windows         map[string]WindowState
	Empty           bool
}

// NewQuery creates query state for a model.
func NewQuery(meta models.Metadata) Query {
	return Query{
		Model:       meta.Clone(),
		Annotations: make(map[string]ExpressionRef),
		Aliases:     make(map[string]ExpressionRef),
		Windows:     make(map[string]WindowState),
	}
}

// Clone returns a deep copy of query state.
func (q Query) Clone() Query {
	copied := q
	copied.Model = q.Model.Clone()
	copied.SelectedColumns = append([]string(nil), q.SelectedColumns...)
	copied.Filters = append([]Predicate(nil), q.Filters...)
	copied.Excludes = append([]Predicate(nil), q.Excludes...)
	copied.Joins = append([]Join(nil), q.Joins...)
	copied.Ordering = append([]string(nil), q.Ordering...)
	copied.Grouping = append([]string(nil), q.Grouping...)
	copied.Having = append([]Predicate(nil), q.Having...)
	if q.Limit != nil {
		value := *q.Limit
		copied.Limit = &value
	}
	if q.Offset != nil {
		value := *q.Offset
		copied.Offset = &value
	}
	copied.DistinctFields = append([]string(nil), q.DistinctFields...)
	copied.Annotations = cloneExpressionMap(q.Annotations)
	copied.Aliases = cloneExpressionMap(q.Aliases)
	copied.Related = q.Related.clone()
	copied.Locking = q.Locking.clone()
	copied.SetOperations = make([]SetOperation, len(q.SetOperations))
	for i, operation := range q.SetOperations {
		copied.SetOperations[i] = operation.clone()
	}
	copied.Windows = cloneWindowMap(q.Windows)
	return copied
}

// Select sets selected columns.
func (q Query) Select(columns ...string) Query {
	cloned := q.Clone()
	cloned.SelectedColumns = append([]string(nil), columns...)
	return cloned
}

// AddFilter appends a filter predicate.
func (q Query) AddFilter(predicate Predicate) Query {
	cloned := q.Clone()
	cloned.Filters = append(cloned.Filters, predicate)
	return cloned
}

// AddExclude appends an excluded predicate.
func (q Query) AddExclude(predicate Predicate) Query {
	cloned := q.Clone()
	predicate.Negated = true
	cloned.Excludes = append(cloned.Excludes, predicate)
	return cloned
}

// AddJoin appends a join.
func (q Query) AddJoin(join Join) Query {
	cloned := q.Clone()
	cloned.Joins = append(cloned.Joins, join)
	return cloned
}

// Order sets ordering.
func (q Query) Order(ordering ...string) Query {
	cloned := q.Clone()
	cloned.Ordering = append([]string(nil), ordering...)
	return cloned
}

// Reverse reverses ordering directions.
func (q Query) Reverse() Query {
	cloned := q.Clone()
	for i, ordering := range cloned.Ordering {
		if strings.HasPrefix(ordering, "-") {
			cloned.Ordering[i] = strings.TrimPrefix(ordering, "-")
			continue
		}
		cloned.Ordering[i] = "-" + ordering
	}
	return cloned
}

// Group sets grouping fields.
func (q Query) Group(grouping ...string) Query {
	cloned := q.Clone()
	cloned.Grouping = append([]string(nil), grouping...)
	return cloned
}

// AddHaving appends a HAVING predicate.
func (q Query) AddHaving(predicate Predicate) Query {
	cloned := q.Clone()
	cloned.Having = append(cloned.Having, predicate)
	return cloned
}

// LimitTo sets query limit.
func (q Query) LimitTo(limit int) Query {
	cloned := q.Clone()
	cloned.Limit = &limit
	return cloned
}

// OffsetBy sets query offset.
func (q Query) OffsetBy(offset int) Query {
	cloned := q.Clone()
	cloned.Offset = &offset
	return cloned
}

// SetDistinct sets distinct behavior.
func (q Query) SetDistinct(distinct bool, fields ...string) Query {
	cloned := q.Clone()
	cloned.Distinct = distinct
	cloned.DistinctFields = append([]string(nil), fields...)
	return cloned
}

// Annotate adds an annotation expression.
func (q Query) Annotate(alias string, expression ExpressionRef) Query {
	cloned := q.Clone()
	cloned.Annotations[alias] = expression.clone()
	return cloned
}

// Alias adds a reusable expression alias.
func (q Query) Alias(alias string, expression ExpressionRef) Query {
	cloned := q.Clone()
	cloned.Aliases[alias] = expression.clone()
	return cloned
}

// SelectRelated adds joined related loading paths.
func (q Query) SelectRelated(paths ...string) Query {
	cloned := q.Clone()
	cloned.Related.SelectRelated = append(cloned.Related.SelectRelated, paths...)
	return cloned
}

// PrefetchRelated adds separate related loading paths.
func (q Query) PrefetchRelated(paths ...string) Query {
	cloned := q.Clone()
	cloned.Related.PrefetchRelated = append(cloned.Related.PrefetchRelated, paths...)
	return cloned
}

// WithLock sets row locking state.
func (q Query) WithLock(lock LockState) Query {
	cloned := q.Clone()
	cloned.Locking = lock.clone()
	return cloned
}

// AddSetOperation appends a set operation.
func (q Query) AddSetOperation(operation SetOperation) Query {
	cloned := q.Clone()
	cloned.SetOperations = append(cloned.SetOperations, operation.clone())
	return cloned
}

// AddWindow adds a window expression.
func (q Query) AddWindow(alias string, window WindowState) Query {
	cloned := q.Clone()
	cloned.Windows[alias] = window.clone()
	return cloned
}

// None marks a query as intentionally empty.
func (q Query) None() Query {
	cloned := q.Clone()
	cloned.Empty = true
	return cloned
}

func cloneExpressionMap(values map[string]ExpressionRef) map[string]ExpressionRef {
	copied := make(map[string]ExpressionRef, len(values))
	for key, value := range values {
		copied[key] = value.clone()
	}
	return copied
}

func cloneWindowMap(values map[string]WindowState) map[string]WindowState {
	copied := make(map[string]WindowState, len(values))
	for key, value := range values {
		copied[key] = value.clone()
	}
	return copied
}
