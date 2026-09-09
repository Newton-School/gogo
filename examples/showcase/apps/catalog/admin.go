package catalog

import (
	"context"
	"strconv"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func RegisterAdmin(site *admin.Site, store *orm.Store) error {
	if err := site.Register(admin.ModelAdmin{
		Schema: (&Product{}).Schema(), ListDisplay: []string{"name", "price", "stock", "published"},
		ListDisplayLinks: []string{"name"}, ListEditable: []string{"price", "stock", "published"},
		SearchFields: []string{"name", "description"}, SearchHelpText: "Search product names and descriptions.",
		ListFilter: []string{"published"}, Ordering: []string{"name"}, ListPerPage: 20,
		ReadonlyFields: []string{"id", "created_at"}, SaveOnTop: true,
		PrepopulatedFields: map[string][]string{"slug": {"name"}}, ConstraintChecker: store,
		Fieldsets: []admin.Fieldset{
			{Name: "Product", Fields: []string{"name", "slug", "description"}},
			{Name: "Inventory", Fields: []string{"price", "stock", "published"}},
			{Name: "History", Fields: []string{"id", "created_at"}, Classes: []string{"collapse"}},
		},
		Inlines: []admin.Inline{{Name: "notes", Label: "Internal notes", FKName: "product", Schema: (&ProductNote{}).Schema(), Fields: []string{"body"}, Extra: 1, Maximum: 20, CanDelete: true, ConstraintChecker: store}},
		Actions: []admin.Action{{Name: "publish", Description: "Publish selected products", Permission: "change", Confirm: true,
			Run: func(ctx context.Context, scoped admin.ScopedStore, objects []admin.Object) error {
				for _, object := range objects {
					if err := object.Record.Set("published", true); err != nil {
						return err
					}
					if _, err := scoped.Save(ctx, object); err != nil {
						return err
					}
				}
				return nil
			},
		}},
	}); err != nil {
		return err
	}
	return site.Register(admin.ModelAdmin{
		Schema: (&ProductNote{}).Schema(), Fields: []string{"product", "body"},
		ListDisplay: []string{"body", "product"}, SearchFields: []string{"body"}, AutocompleteFields: []string{"product"}, ConstraintChecker: store,
		ResolveRelation: func(ctx context.Context, _ models.Field, ids []string) ([]any, error) {
			if len(ids) != 1 {
				return nil, auth.ErrPermissionDenied
			}
			id, err := strconv.ParseInt(ids[0], 10, 64)
			if err != nil {
				return nil, auth.ErrPermissionDenied
			}
			product, err := orm.For(store, func() *Product { return &Product{} }).Filter(orm.Q("id", id)).Get(ctx)
			if err != nil {
				return nil, auth.ErrPermissionDenied
			}
			return []any{product.ID}, nil
		},
	})
}
