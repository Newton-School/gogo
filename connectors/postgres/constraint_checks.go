package postgres

import (
	"context"

	"github.com/Newton-School/gogo/core/db"
)

var _ db.ConstraintCheckTransaction = (*transaction)(nil)

// CheckConstraints deliberately executes on this transaction, never the pool.
// PostgreSQL checks pending deferrable constraints and constraint triggers when
// changing their mode to IMMEDIATE. It does not commit, reset sequences, or
// restore effects outside PostgreSQL's transaction boundary.
func (t *transaction) CheckConstraints(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = &db.Error{Code: db.Unavailable, Message: "Database constraint check unavailable"}
		}
	}()
	if t == nil || t.Tx == nil || ctx == nil {
		return &db.Error{Code: db.Unavailable, Message: "Database constraint check unavailable"}
	}
	_, err = t.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	return err
}
