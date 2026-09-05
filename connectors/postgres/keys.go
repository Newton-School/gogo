package postgres

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func (b *Backend) ReserveAutoKeys(ctx context.Context, executor db.Executor, schema models.Schema, field models.Field, count int) ([]any, error) {
	if !field.IsAuto() || count < 1 || count > 10000 {
		return nil, errors.New("postgres: invalid auto-key reservation")
	}
	table, err := b.Dialect().QuoteIdentifier(schema.DBTable())
	if err != nil {
		return nil, err
	}
	// Sequence increments, like PostgreSQL identity INSERTs, are not rolled back.
	rows, err := executor.Query(ctx, "SELECT nextval(pg_get_serial_sequence($1,$2)) FROM generate_series(1,$3)", table, field.DBColumn(), count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]any, 0, count)
	for rows.Next() {
		var key int64
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) != count {
		return nil, errors.New("postgres: incomplete key reservation")
	}
	return keys, nil
}
