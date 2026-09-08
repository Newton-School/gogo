package admindocs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

type unavailableStore struct{}

func (unavailableStore) Scope(context.Context, auth.Principal, string, models.Schema) (admin.ScopedStore, error) {
	panic("documentation touched a model store")
}

func testSite(t *testing.T) *admin.Site {
	t.Helper()
	key, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "test", Value: []byte(key)}, nil, "admindocs-test")
	if err != nil {
		t.Fatal(err)
	}
	site, err := admin.NewSite(admin.Config{Store: unavailableStore{}, Signer: signer, Policy: auth.ModelPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(admin.ModelAdmin{Schema: models.Schema{AppLabel: "demo", Name: "Entry", Fields: []models.Field{models.BigAutoField("ID"), models.CharField("Title"), models.CharField("Secret")}}, Fields: []string{"Title"}}); err != nil {
		t.Fatal(err)
	}
	return site
}

func testOptions(t *testing.T, invoked *int) Options {
	t.Helper()
	router, err := urls.New(urls.Include("/entries/", "demo", urls.Path("<int:id>/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *invoked++ }), "entry", "GET")))
	if err != nil {
		t.Fatal(err)
	}
	engine := templates.New(templates.Config{
		Tags:       map[string]templates.Tag{"custom": func(context.Context, templates.Context, []any) (any, error) { *invoked++; return nil, nil }},
		Filters:    map[string]templates.Filter{"lower": func(context.Context, any, any) (any, error) { *invoked++; return nil, nil }},
		Loaders:    []templates.Loader{neverLoader{invoked}},
		Processors: []templates.Processor{func(context.Context) (templates.Context, error) { *invoked++; return nil, nil }},
	})
	return Options{Models: []admin.DocumentationModel{{Key: "demo.Entry", Fields: []admin.DocumentationField{{Name: "Title", Description: "Explicit title help"}}}}, Router: router, Templates: engine,
		Views: []admin.DocumentationView{{Route: "demo:entry", Title: "Entry detail", Description: "Read-only view"}},
		Tags:  []admin.DocumentationExtension{{Name: "custom", Description: "Declared custom tag"}}, Filters: []admin.DocumentationExtension{{Name: "lower", Description: "Overridden filter"}},
	}
}

type neverLoader struct{ invoked *int }

func (l neverLoader) Load(context.Context, string) (string, error) {
	*l.invoked++
	panic("documentation loaded the described engine")
}

func TestNewBuildsFrozenDescriptorsWithoutInvokingDocumentedCode(t *testing.T) {
	invoked := 0
	o := testOptions(t, &invoked)
	h, err := New(testSite(t), o)
	if err != nil {
		t.Fatal(err)
	}
	o.Models[0].Fields[0].Name = "Secret"
	o.Models[0].Fields[0].Description = "mutated private description"
	o.Views[0].Title = "mutated private view"
	o.Tags[0].Description = "mutated private tag"
	*o.Router = urls.Router{}
	*o.Templates = templates.Engine{}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "staff", Authenticated: true, Active: true, Staff: true, Permissions: []string{admin.DocumentationPermission, "demo.view_entry"}})
	r := httptest.NewRequest("GET", "/admin/doc/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || invoked != 0 {
		t.Fatal(w.Code, invoked, w.Body.String())
	}
	for _, expected := range []string{"Explicit title help", "Entry detail", "demo:entry", "Declared custom tag", "Overridden filter", "verbatim", "extends"} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatal("missing effective public description", expected)
		}
	}
	if strings.Contains(w.Body.String(), "Secret") || strings.Contains(w.Body.String(), "mutated private") {
		t.Fatal("caller changes retargeted docs", w.Body.String())
	}
}

func TestNewRefusesUnknownDescriptionsAndPartialInventories(t *testing.T) {
	for _, mode := range []string{"nil site", "nil router", "nil engine", "zero engine", "unknown view", "unknown tag", "unknown filter", "duplicate tag", "large description", "large inventory"} {
		t.Run(mode, func(t *testing.T) {
			invoked := 0
			o := testOptions(t, &invoked)
			site := testSite(t)
			switch mode {
			case "nil site":
				site = nil
			case "nil router":
				o.Router = nil
			case "nil engine":
				o.Templates = nil
			case "zero engine":
				o.Templates = &templates.Engine{}
			case "unknown view":
				o.Views[0].Route = "unknown"
			case "unknown tag":
				o.Tags[0].Name = "not_installed"
			case "unknown filter":
				o.Filters[0].Name = "not_installed"
			case "duplicate tag":
				o.Tags = append(o.Tags, o.Tags[0])
			case "large description":
				o.Tags[0].Description = strings.Repeat("x", 4097)
			case "large inventory":
				o.Models = make([]admin.DocumentationModel, 257)
			}
			if h, err := New(site, o); err != admin.ErrDocumentation || h != nil || invoked != 0 {
				t.Fatal("invalid inventory published or callbacks executed", h, err, invoked)
			}
		})
	}
}
