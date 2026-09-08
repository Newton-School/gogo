package admindocs_test

import (
	"net/http"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/admin/admindocs"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

// This compiled registration sketch accepts an application's already configured
// Site and services. The application supplies its real session/auth middleware.
func ExampleNew() {
	register := func(site *admin.Site, router *urls.Router, engine *templates.Engine, mux *http.ServeMux) error {
		reference, err := admindocs.New(site, admindocs.Options{
			Models: []admin.DocumentationModel{{Key: "shop.Product", Fields: []admin.DocumentationField{{Name: "Name", Description: "Public product name"}}}},
			Router: router, Templates: engine,
			Views: []admin.DocumentationView{{Route: "shop:product", Title: "Product detail", Description: "Reads a scoped product."}},
		})
		if err != nil {
			return err
		}
		// This example assumes the configured admin.Config.Prefix is the default "/admin/".
		// Omitting this mount disables documentation; no package init installs it.
		mux.Handle("/admin/doc/", reference)
		mux.Handle("/admin/", site.Handler())
		return nil
	}
	_ = register
}
