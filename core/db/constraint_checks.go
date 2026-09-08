package db

import "context"

// ConstraintCheckTransaction is an optional capability of an already-open
// transaction. CheckConstraints checks outstanding deferred constraints now and
// leaves supported deferrable constraints in immediate mode until the caller
// explicitly changes them or finishes the transaction.
//
// A successful check is not a commit or a guarantee that later work will pass.
// Constraint triggers may execute transactional database work during the check.
// The owner must still observe Commit or Rollback, and must not treat a missing
// capability as successful validation. Wrappers must preserve this capability
// explicitly or retain the exact underlying transaction for the check.
type ConstraintCheckTransaction interface {
	Transaction
	CheckConstraints(context.Context) error
}
