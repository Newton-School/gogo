package sqlcompiler

import (
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// SplitAggregateFilters partitions application predicates without compiling
// SQL or evaluating callbacks. Alias dependencies (including JSON transforms)
// and both expression operands are resolved under one depth/work budget.
// Trusted root/join scope must be applied separately, after this partition.
func SplitAggregateFilters(schema models.Schema, predicate db.Predicate, aliases []db.Projection) (db.Predicate, db.Predicate, error) {
	detector := aggregateDetector{aliases: make(map[string]db.Projection, len(aliases)), schema: schema}
	for _, alias := range aliases {
		detector.aliases[alias.Alias] = alias
	}
	node, err := detector.predicate(predicate, 0)
	if err != nil {
		return db.Predicate{}, db.Predicate{}, err
	}
	where, having := node.split(false)
	var rows, groups db.Predicate
	if where != nil {
		rows = *where
	}
	if having != nil {
		groups = *having
	}
	return rows, groups, nil
}

func (n *aggregatePredicate) split(inheritedNegation bool) (*db.Predicate, *db.Predicate) {
	if !n.aggregate {
		return &n.value, nil
	}
	negated := inheritedNegation != n.value.Negated
	connector := n.value.Connector
	if connector == "" {
		connector = "AND"
	}
	// Splitting these subtrees would turn a disjunction into a conjunction.
	// Keep the entire connected expression after grouping; grouped validation
	// rejects any row field which is not a legal key at that boundary.
	connected := connector == "XOR" || connector == "AND" && negated || connector == "OR" && !negated
	if len(n.children) == 0 || connected {
		return nil, &n.value
	}
	where, having := n.value, n.value
	where.Children, having.Children = nil, nil
	for _, child := range n.children {
		rows, groups := child.split(negated)
		if rows != nil {
			where.Children = append(where.Children, *rows)
		}
		if groups != nil {
			having.Children = append(having.Children, *groups)
		}
	}
	var rows, groups *db.Predicate
	if len(where.Children) > 0 {
		rows = &where
	}
	if len(having.Children) > 0 {
		groups = &having
	}
	return rows, groups
}
