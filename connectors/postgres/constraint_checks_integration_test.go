package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
)

func TestConstraintCheckRunsOnOwnedTransactionAndRemainsRollbackable(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool transaction", true: "dedicated connection"}[dedicated], func(t *testing.T) {
			backend := openTest(t)
			ctx := context.Background()
			for _, query := range []string{
				"CREATE TABLE fixture_parents (id integer PRIMARY KEY)",
				"CREATE TABLE fixture_children (id integer PRIMARY KEY, parent_id integer REFERENCES fixture_parents (id) DEFERRABLE INITIALLY DEFERRED)",
				"CREATE TABLE fixture_audit (id integer NOT NULL)",
				"CREATE FUNCTION fixture_audit_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO fixture_audit VALUES (NEW.id); RETURN NULL; END $$",
				"CREATE CONSTRAINT TRIGGER fixture_audit_pending AFTER INSERT ON fixture_children DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fixture_audit_insert()",
			} {
				if _, err := backend.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			begin := backend.BeginTx
			if dedicated {
				conn, err := backend.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				begin = conn.BeginTx
			}
			for _, mode := range []string{"missing parent", "dry run", "commit", "canceled", "immediate enforcement"} {
				t.Run(mode, func(t *testing.T) {
					tx, err := begin(ctx, db.TxOptions{})
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					checker, ok := tx.(db.ConstraintCheckTransaction)
					if !ok {
						t.Fatal("provider transaction lacks constraint checks")
					}
					if err := checker.CheckConstraints(nil); !db.IsCode(err, db.Unavailable) {
						t.Fatal("nil context was accepted", err)
					}
					id := map[string]int{"missing parent": 1, "dry run": 2, "commit": 3, "canceled": 4, "immediate enforcement": 5}[mode]
					if mode != "missing parent" {
						if _, err := tx.Exec(ctx, "INSERT INTO fixture_parents VALUES ($1)", id); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := tx.Exec(ctx, "INSERT INTO fixture_children VALUES ($1,$1)", id); err != nil {
						t.Fatal("deferred insert failed before check", err)
					}
					var audit int
					if err := db.QueryRow(ctx, tx, "SELECT count(*) FROM fixture_audit WHERE id=$1", []any{id}, &audit); err != nil || audit != 0 {
						t.Fatal("constraint trigger was not deferred", audit, err)
					}
					checkCtx := ctx
					if mode == "canceled" {
						canceled, cancel := context.WithCancel(ctx)
						cancel()
						checkCtx = canceled
					}
					err = checker.CheckConstraints(checkCtx)
					switch mode {
					case "missing parent":
						if !db.IsCode(err, db.ForeignKeyViolation) || strings.Contains(err.Error(), "fixture_children") || strings.Contains(err.Error(), "parent_id") {
							t.Fatal("deferred failure not safely preserved", err)
						}
					case "canceled":
						if !db.IsCode(err, db.Canceled) {
							t.Fatal("canceled check returned success", err)
						}
					default:
						if err != nil {
							t.Fatal(err)
						}
						if err := db.QueryRow(ctx, tx, "SELECT count(*) FROM fixture_audit WHERE id=$1", []any{id}, &audit); err != nil || audit != 1 {
							t.Fatal("pending constraint trigger did not execute on owned transaction", audit, err)
						}
						if err := checker.CheckConstraints(ctx); err != nil {
							t.Fatal("second check failed", err)
						}
						if mode == "immediate enforcement" {
							_, err := tx.Exec(ctx, "INSERT INTO fixture_children VALUES (99,99)")
							if !db.IsCode(err, db.ForeignKeyViolation) {
								t.Fatal("future statement did not use immediate checking", err)
							}
						}
					}
					if mode == "commit" {
						if err := tx.Commit(); err != nil {
							t.Fatal(err)
						}
					} else if err := tx.Rollback(); err != nil {
						t.Fatal(err)
					}
					want := 0
					if mode == "commit" {
						want = 1
					}
					for _, query := range []string{"SELECT count(*) FROM fixture_parents WHERE id=$1", "SELECT count(*) FROM fixture_children WHERE id=$1", "SELECT count(*) FROM fixture_audit WHERE id=$1"} {
						var count int
						if err := db.QueryRow(ctx, backend, query, []any{id}, &count); err != nil || count != want {
							t.Fatal("check escaped transaction outcome", count, want, err)
						}
					}
					if err := checker.CheckConstraints(ctx); err == nil {
						t.Fatal("finished transaction unexpectedly checked through pool")
					}
				})
			}
		})
	}
}
