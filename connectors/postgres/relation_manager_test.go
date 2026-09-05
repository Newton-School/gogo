package postgres_test

import (
	"context"
	"errors"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"testing"
)

func TestForwardAndReverseRelationManagers(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	parentRecord, _ := models.Bind(parent)
	childRecord, _ := models.Bind(child)
	reverse := orm.RelationManager{Store: store, Source: parentRecord, Name: "child_set"}
	rows, err := reverse.All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	forward := orm.RelationManager{Store: store, Source: childRecord, Name: "parent"}
	found, err := forward.One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := found.Get("id")
	if id != parent.ID {
		t.Fatal(id)
	}
	second := &relationRow{Definition: parent.Schema(), Tenant: 1}
	if err := store.Save(ctx, second, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	secondRecord, _ := models.Bind(second)
	child.ModelState().Related = map[string]any{"parent": found}
	if err := forward.Set(ctx, secondRecord); err != nil {
		t.Fatal(err)
	}
	if child.Parent == nil || *child.Parent != second.ID || len(child.ModelState().Related) != 0 {
		t.Fatal("forward relation did not update instance/cache", child)
	}
	if rows, err := reverse.All(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if err := reverse.Add(ctx, childRecord); err != nil {
		t.Fatal(err)
	}
	if child.Parent == nil || *child.Parent != parent.ID {
		t.Fatal("reverse add did not synchronize instance", child)
	}
	if err := reverse.Remove(ctx, childRecord); err != nil {
		t.Fatal(err)
	}
	if child.Parent != nil {
		t.Fatal("reverse remove did not clear FK", child)
	}
	if value, err := forward.One(ctx); err != nil || value != nil {
		t.Fatal(value, err)
	}
	if err := reverse.Set(ctx, childRecord); err != nil {
		t.Fatal(err)
	}
	if err := reverse.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Count(ctx); err != nil || count != 1 {
		t.Fatal("clear deleted child", count, err)
	}
}

func TestRelationMutationsScopeAndHookRollback(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	source, _ := models.Bind(child)
	hidden := &relationRow{Definition: parent.Schema(), Tenant: 2}
	if err := store.Save(ctx, hidden, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	target, _ := models.Bind(hidden)
	manager := orm.RelationManager{Store: store, Source: source, Name: "parent", Scope: func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", int64(1)), nil }}
	if err := manager.Set(ctx, target); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal("cross-scope relation assigned", err)
	}
	failure := errors.New("relation hook failure")
	manager.AfterChange = []orm.RelationReceiver{func(context.Context, orm.RelationChange) error { return failure }}
	if err := manager.Clear(ctx); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	loaded, err := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).Get(ctx)
	if err != nil || loaded.Parent == nil || *loaded.Parent != parent.ID {
		t.Fatal("failed relation operation did not rollback", loaded, err)
	}
}
