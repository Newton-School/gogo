package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type upsertRow struct {
	models.Base
	ID         int64
	Slug, Name string
	Tenant     int64
	Enabled    bool
}

func (r *upsertRow) Schema() models.Schema {
	return models.Schema{AppLabel: "upsert", Name: "Row", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.TextField("slug", models.WithStructField("Slug")), models.TextField("name", models.WithStructField("Name")), models.BigIntegerField("tenant", models.WithStructField("Tenant")), models.BooleanField("enabled", models.WithStructField("Enabled"), models.WithDefault(true))}, Constraints: []models.Constraint{{Name: "upsert_slug_key", Kind: "unique", Fields: []string{"slug"}}, {Name: "upsert_name_key", Kind: "unique", Fields: []string{"name"}}}}
}
func upsertStore(t *testing.T) *orm.Store {
	t.Helper()
	b := openTest(t)
	if err := b.SchemaEditor().CreateModel(context.Background(), b, (&upsertRow{}).Schema()); err != nil {
		t.Fatal(err)
	}
	return orm.New(b, nil)
}
func TestGetOrCreateDefaultAndScopeContracts(t *testing.T) {
	store := upsertStore(t)
	ctx := context.Background()
	query := orm.For(store, func() *upsertRow { return &upsertRow{} }).Filter(orm.Q("tenant", int64(1)))
	key := orm.UniqueKey{Constraint: "upsert_slug_key", Values: map[string]any{"slug": "first"}}
	calls := 0
	defaults := orm.Defaults{"name": orm.DefaultFactory(func(context.Context) (any, error) { calls++; return "first-name", nil }), "tenant": int64(1), "enabled": false}
	row, created, err := query.GetOrCreate(ctx, key, defaults)
	if err != nil || !created || row.ID == 0 || row.Enabled || calls != 1 {
		t.Fatal(row, created, err, calls)
	}
	loaded, created, err := query.GetOrCreate(ctx, key, defaults)
	if err != nil || created || loaded.ID != row.ID || calls != 1 {
		t.Fatal(loaded, created, err, calls)
	}
	_, _, err = query.GetOrCreate(ctx, orm.UniqueKey{Constraint: "unconstrained", Values: key.Values}, defaults)
	if err == nil {
		t.Fatal("unconstrained operation accepted")
	}
	_, _, err = query.GetOrCreate(ctx, orm.UniqueKey{Constraint: "upsert_slug_key", Values: map[string]any{"slug": "other"}}, orm.Defaults{"name": "first-name", "tenant": int64(1)})
	var integrity *db.Error
	if !errors.As(err, &integrity) || integrity.Constraint != "upsert_name_key" {
		t.Fatal("unrelated conflict was swallowed", err)
	}
	outside := orm.For(store, func() *upsertRow { return &upsertRow{} }).Filter(orm.Q("tenant", int64(2)))
	_, _, err = outside.GetOrCreate(ctx, key, orm.Defaults{"name": "different-name", "tenant": int64(2)})
	if !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("outside-scope winner exposed", err)
	}
	_, _, err = query.UpdateOrCreate(ctx, key, orm.Defaults{"tenant": int64(2)})
	if !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("update escaped query scope", err)
	}
	loaded, err = query.Get(ctx)
	if err != nil || loaded.Tenant != 1 {
		t.Fatal("scope rejection did not rollback", loaded, err)
	}
}

func TestNamedUniqueRaceUsesSavepointBeforeReload(t *testing.T) {
	store := upsertStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := orm.For(store, func() *upsertRow { return &upsertRow{} })
	key := orm.UniqueKey{Constraint: "upsert_slug_key", Values: map[string]any{"slug": "race"}}
	var arrivals, defaults atomic.Int32
	ready := make(chan struct{})
	store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error {
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	type outcome struct {
		id      int64
		created bool
		err     error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			row, created, err := query.GetOrCreate(ctx, key, orm.Defaults{"name": orm.DefaultFactory(func(context.Context) (any, error) { return fmt.Sprintf("candidate-%d", defaults.Add(1)), nil }), "tenant": int64(1)})
			var id int64
			if row != nil {
				id = row.ID
			}
			results <- outcome{id, created, err}
		})
	}
	wg.Wait()
	close(results)
	ids := map[int64]bool{}
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		ids[result.id] = true
		if result.created {
			created++
		}
	}
	if len(ids) != 1 || created != 1 || defaults.Load() != 2 {
		t.Fatal(ids, created, defaults.Load())
	}
	count, err := query.Count(ctx)
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
