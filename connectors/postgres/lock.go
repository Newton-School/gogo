package postgres

import (
	"context"
	"github.com/Newton-School/gogo/core/db"
	"time"
)

func (b *Backend) LockMigrations(ctx context.Context) (func() error, error) {
	conn, err := b.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	const lockID int64 = 30616694031491183
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var acquired bool
		err := db.QueryRow(ctx, conn, "SELECT pg_try_advisory_lock($1)", []any{lockID}, &acquired)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if acquired {
			return func() error {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var unlocked bool
				err := db.QueryRow(cleanup, conn, "SELECT pg_advisory_unlock($1)", []any{lockID}, &unlocked)
				closeErr := conn.Close()
				if err != nil {
					return err
				}
				return closeErr
			}, nil
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
