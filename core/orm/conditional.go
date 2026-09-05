package orm

import (
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// When pairs a row predicate with a result expression. Conditions retain the
// enclosing query's resolved aliases and scopes, including explicitly selected
// related paths. NULL predicates do not match, just like SQL WHERE conditions.
func When(condition db.Predicate, then db.Expression) db.WhenBranch {
	return db.WhenBranch{Condition: clonePredicate(condition), Then: cloneExpression(then)}
}

// Case selects the first matching branch, otherwise fallback. Use Value(nil)
// for an explicit SQL NULL fallback. The output type is explicit, and database
// conversion/overflow semantics are the same as Cast. Branch values remain SQL
// expressions; model hooks and validators do not execute inside a query.
func Case(output models.Field, fallback db.Expression, branches ...db.WhenBranch) db.Expression {
	return cloneExpression(db.Expression{Kind: "case", Output: &output, Args: []db.Expression{fallback}, Branches: branches})
}
