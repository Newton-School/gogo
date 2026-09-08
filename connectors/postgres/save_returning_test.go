package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresSaveReturningDeferredAutocommitFailure(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	missingParent := parent.ID + 10000
	table, err := store.Backend.Dialect().QuoteIdentifier(child.Schema().DBTable())
	if err != nil {
		t.Fatal(err)
	}
	var returnedID int64
	err = db.QueryRow(ctx, store.Backend, "INSERT INTO "+table+" (tenant, parent) VALUES ($1, $2) RETURNING id", []any{int64(1), missingParent}, &returnedID)
	if !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal("false successful autocommit QueryRow", err)
	}
	row := &relationRow{Definition: child.Schema(), Tenant: 1, Parent: &missingParent}
	posts := 0
	store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { posts++; return nil }}
	err = store.Save(ctx, row, orm.SaveOptions{ForceInsert: true})
	if !db.IsCode(err, db.ForeignKeyViolation) || row.ID != 0 || row.ModelState().Persisted || posts != 0 {
		t.Fatal("false successful autocommit insert", err, row.ID, row.ModelState(), posts)
	}
	count, countErr := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(ctx)
	if countErr != nil || count != 1 {
		t.Fatal(count, countErr)
	}
	child.Parent = &missingParent
	err = store.Save(ctx, child, orm.SaveOptions{ForceUpdate: true})
	if !db.IsCode(err, db.ForeignKeyViolation) || posts != 0 {
		t.Fatal("false successful autocommit update", err, posts)
	}
	loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Filter(orm.Q("id", child.ID)).Get(ctx)
	if err != nil || loaded.Parent == nil || *loaded.Parent != parent.ID {
		t.Fatal("failed update persisted", loaded, err)
	}
	// Inside explicit Atomic, a completed statement remains provisional until
	// caller commit. Preserve the existing Django-like model/signal timing.
	provisional := &relationRow{Definition: child.Schema(), Tenant: 1, Parent: &missingParent}
	err = db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(txctx context.Context) error {
		if err := store.Save(txctx, provisional, orm.SaveOptions{ForceInsert: true}); err != nil {
			return err
		}
		if provisional.ID == 0 || !provisional.ModelState().Persisted || posts != 1 {
			t.Fatal("statement timing changed", provisional, posts)
		}
		return nil
	})
	if !db.IsCode(err, db.ForeignKeyViolation) || posts != 1 {
		t.Fatal("deferred caller outcome", err, posts)
	}
	if count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(ctx); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestPostgresSaveReturningRefusesSuppressedInsert(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	table, err := store.Backend.Dialect().QuoteIdentifier(child.Schema().DBTable())
	if err != nil {
		t.Fatal(err)
	}
	// These objects live only in openTest's independently owned random schema.
	if _, err := store.Backend.Exec(ctx, "CREATE FUNCTION suppress_test_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Backend.Exec(ctx, "CREATE TRIGGER suppress_test_insert BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION suppress_test_insert()"); err != nil {
		t.Fatal(err)
	}
	row := &relationRow{Definition: child.Schema(), Tenant: 1, Parent: &parent.ID}
	posts := 0
	store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { posts++; return nil }}
	err = store.Save(ctx, row, orm.SaveOptions{ForceInsert: true})
	if err == nil || errors.Is(err, db.ErrNoRows) || row.ID != 0 || row.ModelState().Persisted || posts != 0 {
		t.Fatal("suppressed insert reported success", err, row.ID, row.ModelState(), posts)
	}
	if count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(ctx); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
