package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
)

func TestBulkCreateStableKeysConflictsAndBoundaries(t *testing.T) {
	t.Run("stable keys and skipped save receivers", func(t *testing.T) {
		store := upsertStore(t)
		ctx := context.Background()
		hooks := 0
		store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { hooks++; return nil }}
		rows := []*upsertRow{{Slug: "one", Name: "one", Tenant: 1}, {Slug: "two", Name: "two", Tenant: 1}, {Slug: "three", Name: "three", Tenant: 1}}
		notifications := 0
		outcomes, err := orm.BulkCreate(ctx, store, rows, orm.BulkOptions{BatchSize: 2, AfterCommit: func(context.Context, orm.BulkSummary) error { notifications++; return nil }})
		if err != nil || len(outcomes) != 3 || hooks != 0 || notifications != 1 {
			t.Fatal(outcomes, err, hooks, notifications)
		}
		for i, row := range rows {
			if row.ID == 0 || !outcomes[i].Written || row.ModelState().Adding() {
				t.Fatal(row, outcomes)
			}
		}
		ignored := &upsertRow{Slug: "one", Name: "ignored", Tenant: 1}
		outcomes, err = orm.BulkCreate(ctx, store, []*upsertRow{ignored}, orm.BulkOptions{IgnoreConflicts: true})
		if err != nil || outcomes[0].Written || ignored.ID != 0 || !ignored.ModelState().Adding() {
			t.Fatal("ignored conflict fabricated saved identity", ignored, outcomes, err)
		}
		updated := &upsertRow{Slug: "one", Name: "updated", Tenant: 1}
		outcomes, err = orm.BulkCreate(ctx, store, []*upsertRow{updated}, orm.BulkOptions{ConflictConstraint: "upsert_slug_key", UpdateFields: []string{"name"}})
		if err != nil || !outcomes[0].Written || updated.ID != rows[0].ID || updated.Name != "updated" {
			t.Fatal(updated, outcomes, err)
		}
	})
	for _, partial := range []bool{false, true} {
		name := "atomic"
		if partial {
			name = "explicit partial"
		}
		t.Run(name, func(t *testing.T) {
			store := upsertStore(t)
			ctx := context.Background()
			rows := []*upsertRow{{Slug: "first", Name: "shared", Tenant: 1}, {Slug: "second", Name: "shared", Tenant: 1}}
			outcomes, err := orm.BulkCreate(ctx, store, rows, orm.BulkOptions{BatchSize: 1, CommitEachBatch: partial})
			var failure *orm.BulkError
			if !errors.As(err, &failure) {
				t.Fatal("conflict not returned", err)
			}
			count, err := orm.For(store, func() *upsertRow { return &upsertRow{} }).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				if count != 1 || len(failure.CommittedBatches) != 1 || !outcomes[0].Written {
					t.Fatal(count, failure, outcomes)
				}
			} else if count != 0 || len(failure.CommittedBatches) != 0 || outcomes[0].Written {
				t.Fatal("atomic batch partially committed", count, failure, outcomes)
			}
		})
	}
}

func TestBulkUpdateExplicitFieldsMissingRowsAndAtomicFailure(t *testing.T) {
	t.Run("updates have exact per-input outcomes", func(t *testing.T) {
		store := upsertStore(t)
		ctx := context.Background()
		rows := []*upsertRow{{Slug: "one", Name: "one", Tenant: 1}, {Slug: "two", Name: "two", Tenant: 1}}
		if _, err := orm.BulkCreate(ctx, store, rows, orm.BulkOptions{}); err != nil {
			t.Fatal(err)
		}
		hooks := 0
		store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { hooks++; return nil }}
		rows[0].Name = "changed-one"
		rows[0].Tenant = 9
		rows[1].Name = "changed-two"
		missing := &upsertRow{ID: 999999, Slug: "missing", Name: "missing", Tenant: 1}
		rows = append(rows, missing)
		outcomes, err := orm.BulkUpdate(ctx, store, rows, []string{"name"}, orm.BulkOptions{BatchSize: 2})
		if err != nil || len(outcomes) != 3 || !outcomes[0].Written || !outcomes[1].Written || outcomes[2].Written || hooks != 0 {
			t.Fatal(outcomes, err, hooks)
		}
		loaded, err := orm.For(store, func() *upsertRow { return &upsertRow{} }).Filter(orm.Q("slug", "one")).Get(ctx)
		if err != nil || loaded.Name != "changed-one" || loaded.Tenant != 1 {
			t.Fatal("unlisted fields changed", loaded, err)
		}
		if _, err := orm.BulkUpdate(ctx, store, []*upsertRow{rows[0], rows[0]}, []string{"name"}, orm.BulkOptions{}); err == nil {
			t.Fatal("duplicate batch identity accepted")
		}
		if _, err := orm.BulkUpdate(ctx, store, rows, []string{"id"}, orm.BulkOptions{}); err == nil {
			t.Fatal("primary-key update accepted")
		}
	})
	for _, partial := range []bool{false, true} {
		name := "atomic"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			store := upsertStore(t)
			ctx := context.Background()
			rows := []*upsertRow{{Slug: "one", Name: "one", Tenant: 1}, {Slug: "two", Name: "two", Tenant: 1}}
			if _, err := orm.BulkCreate(ctx, store, rows, orm.BulkOptions{}); err != nil {
				t.Fatal(err)
			}
			rows[0].Name = "shared"
			rows[1].Name = "shared"
			outcomes, err := orm.BulkUpdate(ctx, store, rows, []string{"name"}, orm.BulkOptions{BatchSize: 1, CommitEachBatch: partial})
			var failure *orm.BulkError
			if !errors.As(err, &failure) {
				t.Fatal(err)
			}
			loaded, err := orm.For(store, func() *upsertRow { return &upsertRow{} }).Filter(orm.Q("slug", "one")).Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				if loaded.Name != "shared" || !outcomes[0].Written || len(failure.CommittedBatches) != 1 {
					t.Fatal(loaded, outcomes, failure)
				}
			} else if loaded.Name != "one" || outcomes[0].Written || len(failure.CommittedBatches) != 0 {
				t.Fatal("atomic update partially committed", loaded, outcomes, failure)
			}
		})
	}
}
