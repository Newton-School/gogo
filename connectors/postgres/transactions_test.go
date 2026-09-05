package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"testing"
)

type aliasedBackend struct {
	db.Backend
	name string
}

func (b aliasedBackend) Alias() string { return b.name }

func TestInterleavedDatabaseAliasesKeepWritesInTheirTransaction(t *testing.T) {
	a := openTest(t)
	b := aliasedBackend{Backend: a, name: "secondary"}
	ctx := context.Background()
	if _, err := a.Exec(ctx, "CREATE TABLE alias_writes (id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	aborted := errors.New("rollback B")
	err := db.Atomic(ctx, a, db.AtomicOptions{}, func(ctxA context.Context) error {
		if _, err := db.ExecutorFor(ctxA, a).Exec(ctxA, "INSERT INTO alias_writes VALUES (1)"); err != nil {
			return err
		}
		err := db.Atomic(ctxA, b, db.AtomicOptions{}, func(ctxB context.Context) error {
			if _, err := db.ExecutorFor(ctxB, b).Exec(ctxB, "INSERT INTO alias_writes VALUES (2)"); err != nil {
				return err
			}
			if err := db.Atomic(ctxB, a, db.AtomicOptions{}, func(ctxABA context.Context) error {
				_, err := db.ExecutorFor(ctxABA, b).Exec(ctxABA, "INSERT INTO alias_writes VALUES (3)")
				return err
			}); err != nil {
				return err
			}
			return aborted
		})
		if !errors.Is(err, aborted) {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var id int
	if err := db.QueryRow(ctx, a, "SELECT count(*), min(id) FROM alias_writes", nil, &count, &id); err != nil || count != 1 || id != 1 {
		t.Fatal("B writes escaped rollback", count, id, err)
	}
}
