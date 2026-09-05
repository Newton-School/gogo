package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
)

func TestSaveGuardRejectsPairedHookIdentityRetarget(t *testing.T) {
	_, store := setupProducts(t)
	ctx := context.Background()
	first := &product{Name: "authorized", Amount: "1.00"}
	other := &product{Name: "other", Amount: "2.00"}
	for _, record := range []*product{first, other} {
		if err := store.Save(ctx, record, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	authorizedID := first.ID
	denied := errors.New("write identity denied")
	afterCalls := 0
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error { return event.Record.Set("id", other.ID) }}
	store.AfterSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		afterCalls++
		return event.Record.Set("id", authorizedID)
	}}
	first.Name = "attempted overwrite"
	guard := func(_ context.Context, record models.Record) error {
		if mustValue(t, record, "id") != authorizedID {
			return denied
		}
		return nil
	}
	if err := store.Save(ctx, first, orm.SaveOptions{Guard: guard}); !errors.Is(err, denied) {
		t.Fatal("retargeted SQL accepted", err)
	}
	if afterCalls != 0 {
		t.Fatal("post-save hook ran after denied write")
	}
	loaded, err := orm.For(store, func() *product { return &product{} }).Filter(orm.Q("id", other.ID)).Get(ctx)
	if err != nil || loaded.Name != "other" {
		t.Fatal("unauthorized row changed", loaded, err)
	}
}

func TestSaveGuardSeesPreparedFieldsAndFallbackBoundaries(t *testing.T) {
	_, store := setupProducts(t)
	ctx := context.Background()
	calls := 0
	record := &product{ID: 9001, Name: "fallback", Amount: "1.00"}
	guard := func(_ context.Context, full models.Record) error {
		calls++
		if full.Schema().Key() != record.Schema().Key() {
			t.Fatal("guard did not receive full model")
		}
		if calls == 2 && record.Created.IsZero() {
			t.Fatal("insert timestamp not prepared before final guard")
		}
		return nil
	}
	if err := store.Save(ctx, record, orm.SaveOptions{Guard: guard}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("update-to-insert fallback did not guard both statements", calls)
	}
	calls = 0
	if err := store.Save(ctx, record, orm.SaveOptions{UpdateFields: []string{}, Guard: guard}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("empty UpdateFields invoked write guard")
	}
}
