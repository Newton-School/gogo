package catalog

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/orm"
)

// Seed is explicitly invoked by the local operator. It never runs at startup,
// overwrites an existing product, resets credentials or deletes user records.
func Seed(ctx context.Context, store *orm.Store) error {
	products := []Product{
		{Name: "Workspace notebook", Slug: "workspace-notebook", Description: "Lay-flat pages for ideas and plans.", Price: "18.00", Stock: 120, Published: true},
		{Name: "Everyday tote", Slug: "everyday-tote", Description: "A sturdy carryall for daily essentials.", Price: "24.00", Stock: 42, Published: true},
		{Name: "Ceramic mug", Slug: "ceramic-mug", Description: "A calm start to the morning.", Price: "32.00", Stock: 86, Published: true},
		{Name: "Unreleased desk lamp", Slug: "unreleased-desk-lamp", Description: "Visible in Admin, excluded from the public API.", Price: "74.00", Stock: 12, Published: false},
	}
	return db.Atomic(ctx, store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		for i := range products {
			exists, err := orm.For(store, func() *Product { return &Product{} }).Filter(orm.Q("slug", products[i].Slug)).Exists(ctx)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			if err := store.Save(ctx, &products[i], orm.SaveOptions{ForceInsert: true}); err != nil {
				return err
			}
			if err := store.Save(ctx, &ProductNote{ProductID: products[i].ID, Body: fmt.Sprintf("Seeded example for %s.", products[i].Name)}, orm.SaveOptions{ForceInsert: true}); err != nil {
				return err
			}
		}
		return nil
	})
}
