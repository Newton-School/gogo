package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresIteratorCompletionDeferredStatementFailure(t *testing.T) {
	backend := openTest(t)
	ctx := context.Background()
	// All objects live in this test's disposable schema. This deliberately
	// volatile SELECT fixture returns a row before autocommit checks its FK;
	// it is not an example of an application read-only view.
	for _, statement := range []string{
		`CREATE TABLE iterator_owners (id bigint PRIMARY KEY)`,
		`CREATE TABLE iterator_links (id bigint PRIMARY KEY, owner_id bigint NOT NULL REFERENCES iterator_owners(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE FUNCTION iterator_emit() RETURNS bigint LANGUAGE plpgsql VOLATILE AS $$ BEGIN INSERT INTO iterator_links (id, owner_id) VALUES (1, 99); RETURN 7; END $$`,
		`CREATE VIEW iterator_result AS SELECT iterator_emit() AS id, 19::bigint AS value`,
	} {
		if _, err := backend.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	schema := models.Schema{AppLabel: "iterator", Name: "Result", Table: "iterator_result", Unmanaged: true, Fields: []models.Field{
		models.BigAutoField("id"), models.BigIntegerField("value"),
	}}
	query := orm.For(orm.New(backend, nil), func() *models.MapRecord {
		row, err := models.NewRecord(schema)
		if err != nil {
			t.Fatal(err)
		}
		return row
	})
	assertRolledBack := func(t *testing.T) {
		t.Helper()
		var count int64
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM iterator_links`, nil, &count); err != nil || count != 0 {
			t.Fatal("failed statement retained side effects", count, err)
		}
	}
	for _, operation := range []string{"early close", "all", "get"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			switch operation {
			case "early close":
				iterator, queryErr := query.Iterator(ctx)
				err = queryErr
				if queryErr == nil {
					// A driver may deliver the deferred failure at Next or Close.
					// Both paths must remember it, including repeated Close.
					iterator.Next()
					err = iterator.Close()
					if !db.IsCode(iterator.Err(), db.ForeignKeyViolation) || !db.IsCode(iterator.Close(), db.ForeignKeyViolation) {
						t.Fatal("iterator forgot terminal database failure", iterator.Err())
					}
				}
			case "all":
				prefix, queryErr := query.All(ctx)
				err = queryErr
				if len(prefix) > 1 {
					t.Fatal("unexpected decoded prefix", len(prefix))
				}
			case "get":
				row, queryErr := query.Get(ctx)
				err = queryErr
				if row != nil || errors.Is(queryErr, orm.ErrNotFound) {
					t.Fatal("statement failure became successful detail or absence", row, queryErr)
				}
			}
			if !db.IsCode(err, db.ForeignKeyViolation) {
				t.Fatal("deferred autocommit failure was hidden", operation, err)
			}
			assertRolledBack(t)
		})
	}
	// Caller-owned transactions retain statement-versus-commit timing: completing
	// the SELECT does not claim the deferred constraint has already committed.
	err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(txctx context.Context) error {
		row, err := query.Get(txctx)
		if err != nil {
			return err
		}
		id, err := row.Get("id")
		if err != nil || id != int64(7) {
			t.Fatal("transactional statement did not return its row", id, err)
		}
		return nil
	})
	if !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal("caller commit outcome changed", err)
	}
	assertRolledBack(t)
}
