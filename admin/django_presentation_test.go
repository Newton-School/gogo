package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/templates"
)

func presentationFields() []forms.Field {
	kinds := []forms.Kind{forms.Char, forms.Boolean, forms.NullBoolean, forms.ChoiceKind, forms.TypedChoice, forms.MultipleChoice, forms.TypedMultipleChoice, forms.Integer, forms.Float, forms.Decimal, forms.Date, forms.DateTime, forms.Time, forms.Duration, forms.Email, forms.URL, forms.UUID, forms.Slug, forms.IP, forms.Regex, forms.JSON, forms.File, forms.Image, forms.FilePath, forms.ModelChoice, forms.ModelMultipleChoice, forms.MultiValue, forms.Combo, forms.SplitDateTime}
	fields := []forms.Field{}
	for _, kind := range kinds {
		field := forms.NewField(string(kind), kind)
		field.Required = false
		field.HelpText = "Example " + string(kind) + " field."
		if kind == forms.ChoiceKind || kind == forms.TypedChoice || kind == forms.MultipleChoice || kind == forms.TypedMultipleChoice || kind == forms.ModelChoice || kind == forms.ModelMultipleChoice {
			field.Choices = []forms.Choice{{Value: "one", Label: "One"}, {Value: "two", Label: "Two"}}
		}
		if kind == forms.MultiValue || kind == forms.Combo {
			field.Fields = []forms.Field{forms.NewField("first", forms.Char), forms.NewField("second", forms.Char)}
		}
		if kind == forms.Date || kind == forms.Time || kind == forms.DateTime || kind == forms.SplitDateTime {
			field.Initial = time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
		}
		fields = append(fields, field)
	}
	for _, typ := range []string{"password", "hidden", "multiple-hidden", "textarea", "radio", "checkbox-multiple", "select-multiple"} {
		field := forms.NewField("widget_"+strings.ReplaceAll(typ, "-", "_"), forms.Char)
		field.Required = false
		field.Choices = []forms.Choice{{Value: "one", Label: "One"}, {Value: "two", Label: "Two"}}
		field.Widget = forms.InputWidget{Type: typ}
		fields = append(fields, field)
	}
	fields = append(fields, forms.Field{Name: "permissions", Label: "Permissions", Kind: forms.MultipleChoice, Choices: []forms.Choice{{Value: "one", Label: "Can view products"}, {Value: "two", Label: "Can change products"}}, Widget: forms.InputWidget{Type: "select-multiple", Attrs: map[string]string{"class": "selectfilter", "size": "8", "data-field-name": "Permissions", "data-is-stacked": "0"}}})
	return fields
}

func TestDjangoWidgetsPreserveEveryFormKind(t *testing.T) {
	site, _ := newTestSite(t)
	for _, field := range presentationFields() {
		t.Run(field.Name, func(t *testing.T) {
			form, err := forms.New([]forms.Field{field})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := form.Render("div")
			body, err := site.renderAdminForm(context.Background(), form)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := form.Render("div")
			if before != after {
				t.Fatal("Admin rendering mutated the public form")
			}
			if field.Name != "widget_multiple_hidden" && !strings.Contains(string(body), `name="`+field.Name) {
				t.Fatal("lost field identity", body)
			}
		})
	}
}

func TestDjangoAssetsArePublicImmutableAndTraversalSafe(t *testing.T) {
	site, _ := newTestSite(t)
	for _, name := range []string{"css/base.css", "css/forms.css", "js/theme.js", "js/admin/DateTimeShortcuts.js", "img/icon-yes.svg", "LICENSE"} {
		path := site.djangoAssetURL() + name
		get := perform(site, "GET", path, auth.Principal{}, nil, nil)
		if get.Code != 200 || !strings.Contains(get.Header().Get("Cache-Control"), "immutable") || len(get.Result().Cookies()) != 0 {
			t.Fatal(name, get.Code, get.Header())
		}
		head := perform(site, "HEAD", path, auth.Principal{}, nil, nil)
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal(name, "HEAD changed asset contract")
		}
		if post := perform(site, "POST", path, auth.Principal{}, nil, nil); post.Code != 405 {
			t.Fatal(name, "unsafe asset method", post.Code)
		}
	}
	for _, name := range []string{"../admin.css", "css/../../../../site.go", "css/./base.css", "css//base.css", "css\\base.css"} {
		if _, _, _, _, ok := site.djangoAsset(site.djangoAssetURL() + name); ok {
			t.Fatal("asset traversal", name)
		}
	}
	css, _ := embedded.ReadFile("internal/assets/admin.css")
	if !strings.Contains(string(css), djangoAssetVersion) {
		t.Fatal("stylesheet and source version drift")
	}
}

func TestDjangoAppIndexAndDeniedApp(t *testing.T) {
	site, _ := newTestSite(t)
	page := perform(site, "GET", "/admin/shop/", principal(), nil, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `class="app-shop module`) {
		t.Fatal(page.Code)
	}
	site.config.Policy = auth.ModelPolicy{}
	if page := perform(site, "GET", "/admin/shop/", principal(), nil, nil); page.Code != 404 {
		t.Fatal("denied app disclosed models", page.Code)
	}
}

func renderPresentationGallery(t *testing.T, site *Site, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Read-only gallery", 405)
		return
	}
	form, err := forms.New(presentationFields())
	if err != nil {
		t.Fatal(err)
	}
	body, err := site.renderAdminForm(r.Context(), form)
	if err != nil {
		t.Fatal(err)
	}
	site.render(w, r, principal(), "form.html", templates.Context{"title": "Field widgets", "form": body}, 200)
}

func TestDjangoFieldsHaveUniqueIDsAndEscapeData(t *testing.T) {
	site, _ := newTestSite(t)
	field := forms.NewField("title", forms.Char)
	field.Label, field.HelpText, field.Initial = `<img src=x onerror=bad()>`, `<script>bad()</script>`, `"><script>bad()</script>`
	form, _ := forms.New([]forms.Field{field})
	body, err := site.renderAdminForm(context.Background(), form)
	if err != nil || strings.Contains(string(body), "<img") || strings.Contains(string(body), "<script>") {
		t.Fatal("unescaped form", err)
	}
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(string(body), -1) {
		if seen[match[1]] {
			t.Fatal(fmt.Sprint("duplicate ID ", match[1]))
		}
		seen[match[1]] = true
	}
}
