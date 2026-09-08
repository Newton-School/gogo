package orm

import "github.com/Newton-School/gogo/core/db"

// SnapshotPredicate detaches a predicate's mutable plain query data using the
// same ownership rules as Query.Filter. Call it immediately after a scope
// provider returns, before retaining the result or invoking another callback.
// It does not evaluate scope, validate values, or invoke provider methods.
//
// Custom Valuer/Marshaler objects and opaque structs retain caller ownership;
// their providers must keep them read-only while the query uses them. Invalid
// predicate/expression trees become the existing invalid-tree sentinel, which
// compilation rejects rather than treating the predicate as an empty scope.
func SnapshotPredicate(predicate db.Predicate) db.Predicate {
	return clonePredicate(predicate)
}
