package catalog

import (
	"context"
	"github.com/Newton-School/gogo/core/orm"
)

func Count(ctx context.Context, store *orm.Store) (int64, error) {
	return orm.For(store, func() *Product { return &Product{} }).Count(ctx)
}

// Seed is an explicit one-off maintenance command; no HTTP endpoint writes data.
func Seed(ctx context.Context, store *orm.Store) error {
	for _, name := range []string{"Notebook", "Pencil"} {
		query := orm.For(store, func() *Product { return &Product{} }).Filter(orm.Q("name", name))
		count, err := query.Count(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			if err := store.Save(ctx, &Product{Name: name}, orm.SaveOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}
