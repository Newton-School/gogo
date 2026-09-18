package documentation_test

import (
	"context"
	"crypto/rand"
	"fmt"

	"example.com/gogo-showcase/apps/catalog"
	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
)

// Registration needs no database. This test store denies every data operation.
// A running application must supply its authorized, transactional store.
type registrationStore struct{}

func (registrationStore) Scope(context.Context, auth.Principal, string, models.Schema) (admin.ScopedStore, error) {
	return nil, auth.ErrPermissionDenied
}

func Example_adminOptions() {
	// Test-only key. Running applications load a stable private key from settings.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "test", Value: key}, nil, "admin-test")
	if err != nil {
		panic(err)
	}
	site, err := admin.NewSite(admin.Config{Store: registrationStore{}, Policy: auth.ModelPolicy{}, Signer: signer})
	if err != nil {
		panic(err)
	}
	// docs:begin admin-list
	config := admin.ModelAdmin{Schema: (&catalog.Product{}).Schema()}
	config.ListDisplay = []string{"name", "price", "published"}
	config.ListDisplayLinks = []string{"name"}
	config.ListEditable = []string{"price", "published"}
	config.ListPerPage = 20
	// docs:end admin-list
	// docs:begin admin-search
	config.SearchFields = []string{"name", "description"}
	config.SearchHelpText = "Search product names and descriptions."
	config.ListFilter = []string{"published"}
	config.Ordering = []string{"name"}
	config.SortableBy = []string{"name", "price"}
	// docs:end admin-search
	// docs:begin admin-form
	config.Fields = []string{"name", "slug", "price", "published", "id"}
	config.ReadonlyFields = []string{"id"}
	config.SaveOnTop = true
	config.PrepopulatedFields = map[string][]string{"slug": {"name"}}
	// docs:end admin-form
	// docs:begin admin-register
	if err := site.Register(config); err != nil {
		panic(err)
	}
	// docs:end admin-register
	fmt.Println("registered")
	// Output: registered
}

// docs:begin admin-site-options
func buildAdmin(store admin.Store, signer *security.Signer) (*admin.Site, error) {
	return admin.NewSite(admin.Config{
		Name: "catalog", Header: "Catalog administration", Title: "Catalog Admin",
		IndexTitle: "Manage catalog", Prefix: "/admin/", SiteURL: "/",
		Store: store, Policy: auth.ModelPolicy{}, Signer: signer,
		CSRF: security.CSRFConfig{Secure: true},
	})
}

// docs:end admin-site-options

func Example_adminFeatureOptions() {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "test", Value: key}, nil, "admin-test")
	if err != nil {
		panic(err)
	}
	site, err := buildAdmin(registrationStore{}, signer)
	if err != nil {
		panic(err)
	}
	config := admin.ModelAdmin{Schema: (&catalog.Product{}).Schema()}
	// docs:begin admin-fieldsets
	config.Fields = nil // Fieldsets replaces Fields; do not set both.
	config.ReadonlyFields = []string{"id"}
	config.Fieldsets = []admin.Fieldset{
		{Name: "Product", Description: "Public catalog values.",
			Fields: []string{"name", "slug", "price", "published"}},
		{Name: "Identity", Fields: []string{"id"}, Classes: []string{"collapse"}},
	}
	// docs:end admin-fieldsets
	// docs:begin admin-inline
	config.Inlines = []admin.Inline{{
		Name: "notes", Label: "Internal notes", FKName: "product",
		Schema: (&catalog.ProductNote{}).Schema(), Fields: []string{"body"},
		Extra: 1, Minimum: 0, Maximum: 20, CanDelete: true,
	}}
	// docs:end admin-inline
	// docs:begin admin-column
	config.Columns = []admin.DisplayColumn{{
		Name: "price_usd", Label: "Price (USD)", Ordering: "price",
		Value: func(ctx context.Context, object admin.Object) (any, error) {
			price, err := object.Record.Get("price")
			if err != nil {
				return nil, err
			}
			return fmt.Sprintf("USD %v", price), nil
		},
	}}
	config.ListDisplay = []string{"name", "price_usd"}
	// docs:end admin-column
	// docs:begin admin-action
	config.Actions = []admin.Action{{
		Name: "publish", Description: "Publish selected products",
		Permission: "change", Confirm: true,
		Run: func(ctx context.Context, store admin.ScopedStore, objects []admin.Object) error {
			for _, object := range objects {
				if err := object.Record.Set("published", true); err != nil {
					return err
				}
				if _, err := store.Save(ctx, object); err != nil {
					return err
				}
			}
			return nil
		},
	}}
	// docs:end admin-action
	if err := site.Register(config); err != nil {
		panic(err)
	}
	fmt.Println("registered fieldsets, inline, column and action")
	// Output: registered fieldsets, inline, column and action
}
