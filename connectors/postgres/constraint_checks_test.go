package postgres

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/db"
)

func TestConstraintCheckRejectsMissingTransaction(t *testing.T) {
	for _, tx := range []*transaction{nil, {}} {
		for _, ctx := range []context.Context{nil, context.Background()} {
			if err := tx.CheckConstraints(ctx); !db.IsCode(err, db.Unavailable) {
				t.Fatal("missing transaction did not fail safely", err)
			}
		}
	}
}
