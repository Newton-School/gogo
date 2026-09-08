package db

import "github.com/Newton-School/gogo/core/models"

// Subquery is a resolved, data-only SELECT expression. Schema identifies the
// inner table; Field names its sole local scalar output, or is empty for EXISTS.
// Query contains the already-scoped inner predicate. Connectors do not evaluate
// ORM scope callbacks or execute this query separately from its outer SELECT.
//
// The initial contract supports one correlation level, local fields and an
// explicitly limited scalar query. Joins, groups, windows, locking and nested
// subqueries are unsupported. Direct AST callers own authorization of both
// queries, just as they own the predicates in an ordinary Select.
type Subquery struct {
	Schema models.Schema
	Query  Select
	Field  string
}
