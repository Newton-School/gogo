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
