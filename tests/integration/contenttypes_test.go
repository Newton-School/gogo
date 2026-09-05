package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type genericAsset struct {
	models.Base
	Tenant, Name string
	ID           int64
	table        string
}

func (a *genericAsset) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "Asset", Table: a.table, PrimaryKey: []string{"tenant", "id"}, Fields: []models.Field{
		models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(100)),
		models.BigIntegerField("id", models.WithStructField("ID")),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(100)),
	}}
}

func TestPostgresContentTypesAndScopedGenericReferences(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	assetSchema := (&genericAsset{}).Schema()
	for _, schema := range []models.Schema{assetSchema, (&contenttypes.ContentType{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: contenttypes.Migrations()}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, assetSchema); err != nil {
		t.Fatal(err)
	}
	for _, asset := range []*genericAsset{{Tenant: "allowed", ID: 7, Name: "Visible fixture"}, {Tenant: "hidden", ID: 7, Name: "Private fixture"}} {
		if err := store.Save(ctx, asset, orm.SaveOptions{ForceInsert: true}); err != nil {
			t.Fatal(err)
		}
	}
	types, err := contenttypes.Sync(ctx, store, registry, nil)
	if err != nil || len(types) != 2 {
		t.Fatal("initial identity sync", len(types), err)
	}
	assetType, err := contenttypes.ForModel(ctx, store, assetSchema)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
				t.Error("concurrent sync", err)
			}
		})
	}
	wg.Wait()
	after, err := contenttypes.ByNaturalKey(ctx, store, "catalog", "asset")
	if err != nil || after.ID != assetType.ID {
		t.Fatal("identity changed across sync", err)
	}
	count, err := orm.For(store, func() *contenttypes.ContentType { return &contenttypes.ContentType{} }).Count(ctx)
	if err != nil || count != 2 {
		t.Fatal("sync duplicated identities", count, err)
	}
	resolver, err := contenttypes.NewResolver(store, []contenttypes.Target{{Factory: func() models.Model { return &genericAsset{} }, Scope: func(context.Context, models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", "allowed"), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	object, err := resolver.Resolve(ctx, contenttypes.Reference{ContentTypeID: assetType.ID, Key: map[string]any{"tenant": "allowed", "id": json.Number("7")}})
	if err != nil || object.(*genericAsset).Name != "Visible fixture" {
		t.Fatal("scoped composite reference did not resolve", err)
	}
	for _, key := range []map[string]any{
		{"tenant": "hidden", "id": int64(7)},
		{"tenant": "allowed", "id": "malformed"},
		{"id": int64(7)},
		{"tenant": "allowed", "id": int64(7), "name": "Visible fixture"},
	} {
		if _, err := resolver.Resolve(ctx, contenttypes.Reference{ContentTypeID: assetType.ID, Key: key}); !errors.Is(err, orm.ErrNotFound) {
			t.Fatalf("invalid or hidden reference leaked: %#v %v", key, err)
		}
	}
	noTargets, _ := contenttypes.NewResolver(store, nil)
	if _, err := noTargets.Resolve(ctx, contenttypes.Reference{ContentTypeID: assetType.ID, Key: map[string]any{"tenant": "allowed", "id": int64(7)}}); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("unallowlisted target became accessible", err)
	}
	if _, err := contenttypes.NewResolver(store, []contenttypes.Target{{Factory: func() models.Model { return &genericAsset{} }}}); err == nil {
		t.Fatal("missing trusted scope accepted")
	}
	var calls atomic.Int32
	unstable, err := contenttypes.NewResolver(store, []contenttypes.Target{{Factory: func() models.Model {
		if calls.Add(1) <= 2 {
			return &genericAsset{}
		}
		return &genericAsset{table: "unexpected_table"}
	}, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unstable.Resolve(ctx, contenttypes.Reference{ContentTypeID: assetType.ID, Key: map[string]any{"tenant": "allowed", "id": int64(7)}}); err == nil {
		t.Fatal("stateful factory changed its authorized model schema")
	} else {
		var databaseErr *db.Error
		if errors.As(err, &databaseErr) {
			t.Fatal("unstable schema reached database before being rejected", err)
		}
	}
	if _, err := contenttypes.Sync(ctx, store, registry, map[string]int64{assetSchema.Key(): 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err == nil {
		t.Fatal("schema version silently decreased")
	}
	stale := &contenttypes.ContentType{AppLabel: "retired", ModelName: "old", SchemaVersion: 1, Active: true}
	if err := store.Save(ctx, stale, orm.SaveOptions{ForceInsert: true}); err != nil {
		t.Fatal(err)
	}
	report, err := contenttypes.Stale(ctx, store, registry)
	if err != nil || len(report) != 1 || report[0].ID != stale.ID || !report[0].Active {
		t.Fatal("stale identity was dropped, deactivated, or unreported", err)
	}
}
