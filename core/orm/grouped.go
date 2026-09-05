package orm

import (
	"errors"

	"github.com/Newton-School/gogo/core/db"
)

// GroupBy builds explicit grouped values, not persisted model instances. It
// resets ordering; apply OrderBy after grouping. Group keys currently name
// stored root fields or fields on explicitly selected scoped joins. Typed
// aggregate annotations, Values, SQLContext and Count operate on these groups.
// Model materialization, row locks and mutations are not group operations.
func (q Query[T]) GroupBy(fields ...string) Query[T] {
	q = q.clone()
	if len(fields) == 0 || len(fields) > 64 {
		q.err = errors.New("orm: GroupBy requires between one and 64 keys")
		return q
	}
	q.selectAST.GroupBy = append([]string(nil), fields...)
	q.selectAST.Order = nil
	return q
}

// Having filters groups after aggregation. Filter remains a pre-group row
// predicate and cannot contain aggregate references; it is never silently
// moved across the grouping boundary.
func (q Query[T]) Having(predicates ...db.Predicate) Query[T] {
	q = q.clone()
	if len(q.selectAST.GroupBy) == 0 {
		q.err = errors.New("orm: Having requires explicit GroupBy keys")
		return q
	}
	children := append([]db.Predicate{q.selectAST.Having}, predicates...)
	for i := range children {
		children[i] = clonePredicate(children[i])
	}
	q.selectAST.Having = db.Predicate{Connector: "AND", Children: children}
	return q
}
