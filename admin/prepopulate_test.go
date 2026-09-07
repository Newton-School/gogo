package admin

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

type prepopulatedArticle struct {
	models.Base
	ID                    int64
	Title, Subtitle, Slug string
}

func (*prepopulatedArticle) Schema() models.Schema {
	return models.Schema{AppLabel: "news", Name: "Article", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("title", models.WithStructField("Title"), models.WithMaxLength(100)), models.TextField("subtitle", models.WithStructField("Subtitle"), models.Optional), models.SlugField("slug", models.WithStructField("Slug"), models.WithMaxLength(40))}}
}
func prepopulatedOptions() ModelAdmin {
	return ModelAdmin{Schema: (&prepopulatedArticle{}).Schema(), Fields: []string{"title", "subtitle", "slug"}, PrepopulatedFields: map[string][]string{"slug": {"title", "subtitle"}}}
}

type prepopulatedStore struct {
	Store
	article *prepopulatedArticle
}

func (s prepopulatedStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	base, err := s.Store.Scope(ctx, p, site, schema)
	return prepopulatedScope{ScopedStore: base, article: s.article}, err
}

type prepopulatedScope struct {
	ScopedStore
	article *prepopulatedArticle
}

func (s prepopulatedScope) New(context.Context) (Object, error) {
	record, err := models.Bind(&prepopulatedArticle{})
	return Object{Record: record}, err
}
func (s prepopulatedScope) Get(context.Context, string, bool) (Object, error) {
	value := *s.article
	value.ModelState().Persisted = true
	record, err := models.Bind(&value)
	return Object{Record: record, ID: "1", Version: "v1", Label: value.Title}, err
}

func TestPrepopulatedRegistrationAndDeclarationSnapshots(t *testing.T) {
	for _, mutate := range []func(*ModelAdmin){
		func(o *ModelAdmin) { o.PrepopulatedFields["missing"] = []string{"title"} },
		func(o *ModelAdmin) { o.PrepopulatedFields["title"] = []string{"subtitle"} },
		func(o *ModelAdmin) { o.PrepopulatedFields["slug"] = nil },
		func(o *ModelAdmin) { o.PrepopulatedFields["slug"] = []string{"title", "title"} },
		func(o *ModelAdmin) { o.PrepopulatedFields["slug"] = []string{"slug"} },
		func(o *ModelAdmin) { o.PrepopulatedFields["slug"] = []string{"missing"} },
		func(o *ModelAdmin) { o.ReadonlyFields = []string{"title"} },
		func(o *ModelAdmin) { o.Exclude = []string{"slug"} },
		func(o *ModelAdmin) { o.SensitiveFields = []string{"subtitle"} },
		func(o *ModelAdmin) {
			f := forms.NewField("slug", forms.Slug)
			f.Widget = forms.InputWidget{Type: "password"}
			o.FormOverrides = map[string]forms.Field{"slug": f}
		},
	} {
		site, _ := newTestSite(t)
		options := prepopulatedOptions()
		mutate(&options)
		if err := site.Register(options); err == nil {
			t.Fatal("invalid prepopulation accepted", options.PrepopulatedFields)
		}
	}
	site, _ := newTestSite(t)
	options := prepopulatedOptions()
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	options.PrepopulatedFields["slug"][0] = "injected"
	options.PrepopulatedFields["other"] = []string{"title"}
	stored := site.models["news.Article"].PrepopulatedFields
	if len(stored) != 1 || stored["slug"][0] != "title" {
		t.Fatal("registration retained caller-owned map/slice")
	}
}

func TestPrepopulatedAddChangeAndReadonlyRendering(t *testing.T) {
	site, database := newTestSite(t)
	site.config.Store = prepopulatedStore{Store: database, article: &prepopulatedArticle{ID: 1, Title: "Existing", Slug: ""}}
	options := prepopulatedOptions()
	options.Fieldsets = []Fieldset{{Name: "Article", Fields: options.Fields}}
	options.Fields = nil
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	add := perform(site, "GET", "/admin/news/article/add/", principal(), nil, nil)
	if add.Code != 200 || !strings.Contains(add.Body.String(), `data-prepopulate-from="[&#34;id_title&#34;,&#34;id_subtitle&#34;]"`) || !strings.Contains(add.Body.String(), `data-prepopulate-maxlength="40"`) {
		t.Fatal(add.Code, add.Body.String())
	}
	change := perform(site, "GET", "/admin/news/article/1/change/", principal(), nil, nil)
	if change.Code != 200 || strings.Contains(change.Body.String(), "data-prepopulate-from") {
		t.Fatal("saved object was prepopulated", change.Code, change.Body.String())
	}
	for _, name := range []string{"title", "slug"} {
		options := site.models["news.Article"]
		options.GetReadonlyFields = func(context.Context, Object) []string { return []string{name} }
		site.models["news.Article"] = options
		page := perform(site, "GET", "/admin/news/article/add/", principal(), nil, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), "data-prepopulate-from") {
			t.Fatal("dynamic readonly field entered suggestion mapping", page.Code)
		}
	}
}

func TestPrepopulationIsNotAServerDefaultAndEscapesAttributes(t *testing.T) {
	options := prepopulatedOptions()
	record, _ := models.Bind(&prepopulatedArticle{})
	overrides, err := prepopulatedOverrides(options, Object{Record: record}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"", "invalid slug", "manual-slug"} {
		form, err := forms.NewModelForm(context.Background(), record, forms.ModelFormOptions{Fields: options.Fields, Overrides: overrides}, forms.WithData(url.Values{"title": {"Valid title"}, "slug": {slug}}))
		if err != nil {
			t.Fatal(err)
		}
		valid := form.IsValid()
		if valid != (slug == "manual-slug") {
			t.Fatal("suggestion changed server validation", slug, form.Errors())
		}
	}
	field := overrides["slug"]
	widget := field.Widget.(prepopulatedWidget)
	copy := widget.CloneWidget().(prepopulatedWidget)
	copy.Sources[0] = "changed"
	if widget.Sources[0] != "title" {
		t.Fatal("widget sources not cloned")
	}
	html, err := widget.Render(forms.BoundField{Field: field, Name: "slug", ID: "id_row_slug", Value: `"><script>attack</script>`})
	if err != nil || strings.Contains(string(html), "<script>") || !strings.Contains(string(html), "id_row_title") {
		t.Fatal("unsafe or unprefixed widget", html, err)
	}
}

func TestPrepopulationUnicodeFollowsBothModelAndFormContracts(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    bool
		override *forms.Field
		want     bool
	}{
		{name: "ASCII default"},
		{name: "Unicode descriptor", model: true, want: true},
		{name: "strict override", model: true, override: &forms.Field{Kind: forms.Slug}},
		{name: "wider override cannot widen model", override: &forms.Field{Kind: forms.Slug, AllowUnicode: true}},
		{name: "text override permits Unicode", model: true, override: &forms.Field{Kind: forms.Char}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := prepopulatedOptions()
			options.Schema.Fields[3].AllowUnicode = test.model
			if test.override != nil {
				options.FormOverrides = map[string]forms.Field{"slug": *test.override}
			}
			record, _ := models.NewRecord(options.Schema)
			overrides, err := prepopulatedOverrides(options, Object{Record: record}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			widget := overrides["slug"].Widget.(prepopulatedWidget)
			if widget.AllowUnicode != test.want {
				t.Fatal("suggestion widened validation contract", widget.AllowUnicode)
			}
			field := overrides["slug"]
			field.Name = "slug"
			html, err := widget.Render(forms.BoundField{Field: field, ID: "id_slug", Name: "slug"})
			want := `data-prepopulate-unicode="false"`
			if test.want {
				want = `data-prepopulate-unicode="true"`
			}
			if err != nil || !strings.Contains(string(html), want) {
				t.Fatal("missing declared Unicode mode", html, err)
			}
			if field.MaxLength != 40 {
				t.Fatal("model length not preserved", field.MaxLength)
			}
		})
	}
}
