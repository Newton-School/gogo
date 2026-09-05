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
	q.modelGrouping = false
	q.selectAST.Order = nil
	return q
}

// Having filters explicit or implicit model groups after aggregation. Filter
// automatically separates ordinary rows from aggregate-dependent conditions.
// Mixed OR/negated subtrees stay connected; their nonaggregate fields must be
// valid group keys. Trusted QueryScope predicates always remain before grouping.
func (q Query[T]) Having(predicates ...db.Predicate) Query[T] {
	q = q.clone()
	if len(q.selectAST.GroupBy) == 0 && !q.modelGrouping {
		q.err = errors.New("orm: Having requires GroupBy keys or aggregate annotations")
		return q
	}
	children := append([]db.Predicate{q.selectAST.Having}, predicates...)
	for i := range children {
		children[i] = clonePredicate(children[i])
	}
	q.selectAST.Having = db.Predicate{Connector: "AND", Children: children}
	return q
}

// prepareModelGrouping runs after scoped eager planning, before projection
// selection. Values after Annotate retains per-model cardinality even if it
// omits the PK from the returned map. Only/Defer never change grouping identity.
func (q Query[T]) prepareModelGrouping() (Query[T], error) {
	if !q.modelGrouping {
		return q, nil
	}
	q = q.clone()
	keys := []string{}
	for _, field := range q.schema.Fields {
		if field.IsStored() {
			keys = append(keys, field.Name)
		}
	}
	for _, join := range q.selectAST.Joins {
		for _, field := range join.Schema.Fields {
			if field.IsStored() {
				keys = append(keys, join.Path+"__"+field.Name)
			}
		}
	}
	if len(keys) == 0 || len(keys) > 64 {
		return q, errors.New("orm: model aggregate grouping requires between one and 64 stored root/join fields")
	}
	q.selectAST.GroupBy = keys
	return q, nil
}
