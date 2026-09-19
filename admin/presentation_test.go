package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

func TestAppIndexGroupsOnlyPermittedModels(t *testing.T) {
	site, _ := newTestSite(t)
	for _, name := range []string{"ViewOnly", "AddOnly", "Hidden"} {
		schema := models.Schema{AppLabel: "reports", Name: name, LabelPlural: name,
			Fields: []models.Field{models.BigAutoField("id")}}
		if err := site.Register(ModelAdmin{Schema: schema}); err != nil {
			t.Fatal(err)
		}
	}
	site.config.Policy = auth.ModelPolicy{}
	p := principal()
	p.Permissions = []string{"reports.view_viewonly", "reports.add_addonly", "shop.view_product"}
	response := perform(site, "GET", "/admin/", p, nil, nil)
	body := response.Body.String()
	if response.Code != 200 || strings.Count(body, `<table class="app-module">`) != 2 {
		t.Fatal("index did not group its authorized models", response.Code, body)
	}
	for _, want := range []string{`<caption>reports</caption>`, `<caption>shop</caption>`, `href="/admin/reports/addonly/add/"`, `aria-label="View ViewOnly"`} {
		if !strings.Contains(body, want) {
			t.Fatal("missing model action", want)
		}
	}
	for _, absent := range []string{"Hidden", "/admin/reports/viewonly/add/", `aria-label="View AddOnly"`, "model-card", "brand-mark", "WORKSPACE"} {
		if strings.Contains(body, absent) {
			t.Fatal("index contains denied metadata or retired decoration", absent)
		}
	}
	denied := perform(site, "GET", "/admin/reports/hidden/", p, nil, nil)
	if denied.Code != 403 {
		t.Fatal("presentation weakened route authorization", denied.Code)
	}
}

func TestAppGroupingPreservesOrderActionsAndEscaping(t *testing.T) {
	rows := []any{
		templates.Context{"app": "one", "label": "First", "can_add": false},
		templates.Context{"app": "two", "label": "Second"},
		templates.Context{"app": "one", "label": "Third", "can_add": true},
	}
	groups := groupNavigation(rows)
	if len(groups) != 2 || groups[0]["label"] != "one" || groups[1]["label"] != "two" {
		t.Fatal("unstable app order", groups)
	}
	first := groups[0]["models"].([]any)
	if len(first) != 2 || first[0].(templates.Context)["label"] != "First" || first[1].(templates.Context)["can_add"] != true {
		t.Fatal("grouping altered model rows", first)
	}
	site, _ := newTestSite(t)
	options := site.models["shop.Product"]
	options.Schema.LabelPlural = `<img src=x onerror=bad()>`
	site.models["shop.Product"] = options
	body := perform(site, "GET", "/admin/", principal(), nil, nil).Body.String()
	if strings.Contains(body, "<img") || !strings.Contains(body, "&lt;img") {
		t.Fatal("index label escaping was lost")
	}
}

func TestFlatTemplateNavigationRemainsAvailable(t *testing.T) {
	site, _ := newTestSite(t)
	site.engine = templates.New(templates.Config{Loaders: []templates.Loader{templates.MapLoader{
		"index.html": `{% for item in models %}MODEL={{ item.label }};{% endfor %}{% for item in navigation %}NAV={{ item.label }};{% endfor %}`,
	}}})
	response := perform(site, "GET", "/admin/", principal(), nil, nil)
	if response.Code != 200 || response.Body.String() != "MODEL=Product;NAV=Product;" {
		t.Fatal("custom template context changed", response.Code, response.Body.String())
	}
}

func TestSimpleTemplatesPreserveFormAndNavigationControls(t *testing.T) {
	site, _ := newTestSite(t)
	page := perform(site, "GET", "/admin/shop/product/1/change/", principal(), nil, nil)
	for _, control := range []string{`enctype="multipart/form-data"`, `name="csrfmiddlewaretoken"`, `name="_edit_token"`, `name="_save"`, `name="_continue"`, `name="_addanother"`, `href="#main"`, `<summary>Model navigation</summary>`} {
		if !strings.Contains(page.Body.String(), control) {
			t.Fatal("form lost control", control)
		}
	}
	for _, template := range []string{"action.html", "delete.html", "history.html", "documentation.html"} {
		t.Run(template, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "http://example.test/admin/shop/product/", nil)
			site.render(w, r, principal(), template, templates.Context{"title": "Review"}, 200)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `<main id="main"`) {
				t.Fatal("page failed to render", w.Code)
			}
		})
	}
	if _, err := site.engine.Render(context.Background(), "fieldsets.html", templates.Context{}); err != nil {
		t.Fatal(err)
	}
}
