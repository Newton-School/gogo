package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type relationRow struct {
	models.Base
	Definition    models.Schema
	ID, Tenant    int64
	Parent, Other *int64
}

func (r *relationRow) Schema() models.Schema { return r.Definition }
func relationSchemas(policy models.DeletePolicy) (models.Schema, models.Schema) {
	parent := models.Schema{AppLabel: "graph", Name: "Parent", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.BigIntegerField("tenant", models.WithStructField("Tenant"))}}
	child := models.Schema{AppLabel: "graph", Name: "Child", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.BigIntegerField("tenant", models.WithStructField("Tenant")), models.ForeignKeyField("parent", models.Relation{Target: parent.Key(), OnDelete: policy}, models.Nullable, models.WithStructField("Parent"))}}
	return parent, child
}
func setupRelations(t *testing.T, policy models.DeletePolicy, alsoRestrict bool) (*orm.Store, *relationRow, *relationRow) {
	t.Helper()
	b := openTest(t)
	parent, child := relationSchemas(policy)
	if alsoRestrict {
		child.Fields = append(child.Fields, models.ForeignKeyField("other", models.Relation{Target: parent.Key(), OnDelete: models.Restrict}, models.Nullable, models.WithStructField("Other")))
	}
	registry := &models.Registry{}
	for _, s := range []models.Schema{parent, child} {
		if err := registry.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "graph", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel(parent), migrations.CreateModel(child)}}}}
	if err := e.Apply(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	p := &relationRow{Definition: parent, Tenant: 1}
	if err := store.Save(context.Background(), p, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	pid := p.ID
	c := &relationRow{Definition: child, Tenant: 1, Parent: &pid}
	if alsoRestrict {
		c.Other = &pid
	}
	if err := store.Save(context.Background(), c, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	return store, p, c
}
func TestDeleteCollectorPoliciesAndTransactionalHooks(t *testing.T) {
	for _, policy := range []models.DeletePolicy{models.Cascade, models.Protect, models.Restrict, models.SetNull, models.DoNothing} {
		t.Run(string(policy), func(t *testing.T) {
			store, parent, child := setupRelations(t, policy, false)
			ctx := context.Background()
			record, _ := models.Bind(parent)
			collector := orm.DeleteCollector{Store: store}
			plan, err := collector.Collect(ctx, record)
			if err != nil {
				t.Fatal(err)
			}
			if policy == models.Cascade && len(plan.Objects) != 2 {
				t.Fatal("cascade graph", plan)
			}
			counts, err := collector.Execute(ctx, record)
			switch policy {
			case models.Protect, models.Restrict:
				if !errors.Is(err, orm.ErrProtectedRelation) {
					t.Fatal("protection", err)
				}
			case models.DoNothing:
				if !db.IsCode(err, db.ForeignKeyViolation) {
					t.Fatal("database FK did not guard deletion", err)
				}
			default:
				if err != nil || counts[parent.Schema().Key()] != 1 {
					t.Fatal(counts, err)
				}
				if parent.ID != 0 {
					t.Fatal("deleted primary key not reset")
				}
			}
			if policy == models.SetNull {
				loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Get(ctx)
				if err != nil || loaded.Parent != nil {
					t.Fatal(loaded, err)
				}
			}
		})
	}
	t.Run("restrict within cascade deletion set", func(t *testing.T) {
		store, parent, _ := setupRelations(t, models.Cascade, true)
		if _, err := store.Delete(context.Background(), parent); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("hook failure rolls back complete graph", func(t *testing.T) {
		store, parent, child := setupRelations(t, models.Cascade, false)
		failure := errors.New("hook failure")
		store.AfterDelete = []orm.DeleteReceiver{func(context.Context, orm.DeleteEvent) error { return failure }}
		if _, err := store.Delete(context.Background(), parent); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(context.Background())
		if err != nil || count != 1 || parent.ID == 0 {
			t.Fatal(count, err)
		}
	})
}

func TestDeleteScopeCannotCascadeHiddenChildren(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	child.Tenant = 2
	if err := store.Save(ctx, child, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	collector := orm.DeleteCollector{Store: store, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", int64(1)), nil }}
	record, _ := models.Bind(parent)
	plan, err := collector.Collect(ctx, record)
	if err != nil || len(plan.Objects) != 1 {
		t.Fatal(plan, err)
	}
	if _, err := collector.Execute(ctx, record); !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal("hidden child silently cascaded", err)
	}
	for _, schema := range []models.Schema{parent.Schema(), child.Schema()} {
		count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: schema} }).Count(ctx)
		if err != nil || count != 1 {
			t.Fatal(count, err)
		}
	}
}

func TestForeignKeyTypeFollowsUUIDTarget(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	parent := models.Schema{AppLabel: "uuid", Name: "Parent", Fields: []models.Field{models.UUIDField("id", models.Primary)}}
	child := models.Schema{AppLabel: "uuid", Name: "Child", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: parent.Key(), OnDelete: models.Cascade})}}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "uuid", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel(parent), migrations.CreateModel(child)}}}}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	const id = "a529b0fa-610a-4411-9130-79e5a38d9fd7"
	if _, err := b.Exec(ctx, "INSERT INTO uuid_parent (id) VALUES ($1)", id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, "INSERT INTO uuid_child (parent) VALUES ($1)", id); err != nil {
		t.Fatal("FK did not retain target UUID type", err)
	}
	if _, err := b.Exec(ctx, "INSERT INTO uuid_child (parent) VALUES ($1)", "9c8656a3-64fc-4b03-8e65-3e52d17d15ee"); !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal("missing foreign key accepted", err)
	}
}

func TestDeleteCollectorBoundsAndScopeChangingHook(t *testing.T) {
	t.Run("authorize freshly locked graph", func(t *testing.T) {
		store, parent, child := setupRelations(t, models.Cascade, false)
		ctx := context.Background()
		record, _ := models.Bind(parent)
		collector := orm.DeleteCollector{Store: store}
		preview, err := collector.Collect(ctx, record)
		if err != nil || len(preview.Objects) != 2 {
			t.Fatal(preview, err)
		}
		second := &relationRow{Definition: child.Schema(), Tenant: 1, Parent: child.Parent}
		if err := store.Save(ctx, second, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		denied := errors.New("graph permission denied")
		hooks := 0
		store.BeforeDelete = []orm.DeleteReceiver{func(context.Context, orm.DeleteEvent) error { hooks++; return nil }}
		collector.Authorize = func(ctx context.Context, plan orm.DeletionPlan) error {
			if !db.InTransaction(ctx, store.Backend.Alias()) || len(plan.Objects) != 3 {
				t.Fatal("authorization did not receive locked current graph", len(plan.Objects))
			}
			return denied
		}
		if _, err := collector.Execute(ctx, record); !errors.Is(err, denied) || hooks != 0 {
			t.Fatal("denial did not precede side effects", err, hooks)
		}
		count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(ctx)
		if err != nil || count != 2 {
			t.Fatal(count, err)
		}
	})
	t.Run("all affected identities count", func(t *testing.T) {
		store, parent, child := setupRelations(t, models.Protect, false)
		ctx := context.Background()
		second := &relationRow{Definition: child.Schema(), Tenant: 1, Parent: child.Parent}
		if err := store.Save(ctx, second, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
		record, _ := models.Bind(parent)
		if _, err := (orm.DeleteCollector{Store: store, MaxObjects: 2}).Collect(ctx, record); !errors.Is(err, orm.ErrDeleteLimit) {
			t.Fatal("protected objects escaped total bound", err)
		}
		if _, err := (orm.DeleteCollector{Store: store, MaxWork: 1}).Collect(ctx, record); !errors.Is(err, orm.ErrDeleteLimit) {
			t.Fatal("work budget ignored", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := (orm.DeleteCollector{Store: store}).Collect(canceled, record); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("hook cannot change object out of scope", func(t *testing.T) {
		store, parent, child := setupRelations(t, models.Cascade, false)
		ctx := context.Background()
		store.BeforeDelete = []orm.DeleteReceiver{func(ctx context.Context, event orm.DeleteEvent) error {
			if event.Record.Schema().Key() == child.Schema().Key() {
				_, err := db.ExecutorFor(ctx, store.Backend).Exec(ctx, "UPDATE graph_child SET tenant=2 WHERE id=$1", child.ID)
				return err
			}
			return nil
		}}
		collector := orm.DeleteCollector{Store: store, Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", int64(1)), nil }}
		record, _ := models.Bind(parent)
		if _, err := collector.Execute(ctx, record); !errors.Is(err, orm.ErrNotUpdated) {
			t.Fatal("scope-changing hook bypassed mutation scope", err)
		}
		loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Get(ctx)
		if err != nil || loaded.Tenant != 1 {
			t.Fatal("hook write failed to roll back", loaded, err)
		}
	})
	t.Run("query root selection bounded", func(t *testing.T) {
		store, parent, _ := setupRelations(t, models.Cascade, false)
		ctx := context.Background()
		if _, err := store.Backend.Exec(ctx, "INSERT INTO graph_parent (tenant) SELECT 1 FROM generate_series(1,10000)"); err != nil {
			t.Fatal(err)
		}
		if _, err := orm.For(store, func() *relationRow { return &relationRow{Definition: parent.Schema()} }).Delete(ctx); !errors.Is(err, orm.ErrDeleteLimit) {
			t.Fatal("unbounded root selection accepted", err)
		}
		count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: parent.Schema()} }).Count(ctx)
		if err != nil || count != 10001 {
			t.Fatal("bounded rejection mutated rows", count, err)
		}
	})
}
