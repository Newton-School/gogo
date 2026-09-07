package admin

import (
	"context"
	"html"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
)

func TestEmptyDisplayDefaultCoversEmptyStringsAndCollections(t *testing.T) {
	base, _ := newTestSite(t)
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := ModelAdmin{Schema: (&testRecord{}).Schema(), Fields: []string{"Name"}, ListDisplay: []string{"emptyText", "emptyList"}, Columns: []DisplayColumn{
		{Name: "emptyText", Value: func(context.Context, Object) (any, error) { return "", nil }},
		{Name: "emptyList", Value: func(context.Context, Object) (any, error) { return []any{}, nil }},
	}}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	page := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
	if page.Code != 200 || strings.Count(page.Body.String(), "—") != 2 {
		t.Fatal("empty values lack the default placeholder", page.Code, page.Body.String())
	}
}

type emptyDisplayMethods struct{}

func (emptyDisplayMethods) String() string { panic("display emptiness invoked String") }
func (emptyDisplayMethods) Len() int       { panic("display emptiness invoked Len") }

func TestEmptyDisplayShapeDoesNotUseTruthinessOrApplicationMethods(t *testing.T) {
	var missing *string
	blank := ""
	var cycle any
	cycle = &cycle
	for _, value := range []any{nil, "", []any{}, [0]int{}, map[string]int{}, missing, &blank, []byte{}, (chan int)(nil), (func())(nil)} {
		if !emptyDisplayValue(value) {
			t.Fatalf("empty value rejected: %T", value)
		}
	}
	for _, value := range []any{0, 0.0, false, " ", "0", []any{nil}, [1]int{}, map[string]int{"zero": 0}, emptyDisplayMethods{}, cycle} {
		if emptyDisplayValue(value) {
			t.Fatalf("nonempty value replaced: %T", value)
		}
	}
	options := ModelAdmin{}
	site := &Site{}
	for _, value := range []any{0, false, " ", []int{0}} {
		if !reflect.DeepEqual(site.displayValue(options, "value", value), value) {
			t.Fatal("presentation changed a nonempty value")
		}
	}
}

func TestEmptyDisplayPrecedenceSnapshotsAndEscaping(t *testing.T) {
	for _, mode := range []string{"site", "model", "column", "empty-site", "empty-model", "empty-column"} {
		t.Run(mode, func(t *testing.T) {
			base, _ := newTestSite(t)
			siteText, modelText, columnText := `<b data-kind="site">empty</b>`, `<b data-kind="model">empty</b>`, `<b data-kind="column">empty</b>`
			if mode == "empty-site" {
				siteText = ""
			}
			if mode == "empty-model" {
				modelText = ""
			}
			if mode == "empty-column" {
				columnText = ""
			}
			config := base.config
			config.EmptyValueDisplay = &siteText
			site, err := NewSite(config)
			if err != nil {
				t.Fatal(err)
			}
			expected := siteText
			options := ModelAdmin{Schema: (&testRecord{}).Schema(), Fields: []string{"Name"}, ListDisplay: []string{"blank", "zero", "boolean"}, Columns: []DisplayColumn{
				{Name: "blank", Value: func(context.Context, Object) (any, error) { return (*string)(nil), nil }},
				{Name: "zero", Value: func(context.Context, Object) (any, error) { return 0, nil }},
				{Name: "boolean", Value: func(context.Context, Object) (any, error) { return false, nil }},
			}}
			if mode == "model" || mode == "column" || mode == "empty-model" || mode == "empty-column" {
				options.EmptyValueDisplay, expected = &modelText, modelText
			}
			if mode == "column" || mode == "empty-column" {
				options.Columns[0].EmptyValueDisplay, expected = &columnText, columnText
			}
			if err := site.Register(options); err != nil {
				t.Fatal(err)
			}
			siteText, modelText, columnText = "mutated site", "mutated model", "mutated column"
			page := perform(site, "GET", "/admin/shop/product/", principal(), nil, nil)
			body := page.Body.String()
			if page.Code != 200 || !strings.Contains(body, `href="/admin/shop/product/1/change/">`+html.EscapeString(expected)+`</a>`) || !strings.Contains(body, `<td>0</td><td>false</td>`) {
				t.Fatal(mode, page.Code, body)
			}
			if strings.Contains(body, `<b data-kind=`) || strings.Contains(body, "mutated ") || strings.Contains(body, `aria-label="empty`) {
				t.Fatal("placeholder escaped its text boundary or retained mutable configuration", body)
			}
		})
	}
}

func TestEmptyDisplayReadonlyFlatAndFieldsetsNeverCallDisplayValue(t *testing.T) {
	for _, fieldsets := range []bool{false, true} {
		base, database := newTestSite(t)
		value := database.records["1"]
		value.Secret = ""
		database.records["1"] = value
		text := `<img src=x onerror=bad()>`
		site, err := NewSite(base.config)
		if err != nil {
			t.Fatal(err)
		}
		options := ModelAdmin{Schema: value.Schema(), Fields: []string{"Name", "Secret"}, ReadonlyFields: []string{"Secret"}, Columns: []DisplayColumn{{Name: "Secret", EmptyValueDisplay: &text, Value: func(context.Context, Object) (any, error) {
			panic("readonly placeholder evaluated a display callback")
		}}}}
		if fieldsets {
			options.Fields, options.Fieldsets = nil, []Fieldset{{Name: "Details", Fields: []string{"Name", "Secret"}}}
		}
		if err := site.Register(options); err != nil {
			t.Fatal(err)
		}
		page := perform(site, "GET", "/admin/shop/product/1/change/", principal(), nil, nil)
		body := page.Body.String()
		if page.Code != 200 || !strings.Contains(body, `<div>`+html.EscapeString(text)+`</div>`) || strings.Contains(body, `<img`) || strings.Contains(body, `name="Secret"`) {
			t.Fatal(fieldsets, page.Code, body)
		}
	}
}

func TestEmptyDisplayHiddenReadonlyRelationsDoNotLoadOrEvaluateLabels(t *testing.T) {
	site, _ := newTestSite(t)
	text := "No visible values"
	site.config.EmptyValueDisplay = &text
	schema := models.Schema{AppLabel: "shop", Name: "Collection", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("products", models.Relation{Target: "shop.Product"}, models.Optional)}}
	record, err := models.NewRecord(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Set("id", int64(1)); err != nil {
		t.Fatal(err)
	}
	record.State().Persisted = true
	object := Object{ID: "collection", Record: record}
	options := ModelAdmin{Schema: schema, Fields: []string{"products"}, ReadonlyFields: []string{"products"}, Fieldsets: []Fieldset{{Name: "Relations", Fields: []string{"products"}}}, ResolveRelation: func(context.Context, models.Field, []string) ([]any, error) {
		panic("placeholder invoked hidden resolver")
	}}
	site.config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return auth.ErrPermissionDenied })
	reader := &readonlyScope{onRead: func() { panic("placeholder loaded denied relations") }}
	values, err := site.readonlyValues(context.Background(), principal(), options, object, reader, options.ReadonlyFields)
	if err != nil {
		t.Fatal(err)
	}
	form, err := forms.NewModelForm(context.Background(), record, forms.ModelFormOptions{Fields: options.Fields, Readonly: options.ReadonlyFields})
	if err != nil {
		t.Fatal(err)
	}
	output, err := site.renderModelForm(context.Background(), options, object, form, options.ReadonlyFields, values)
	if err != nil || reader.calls != 0 || !strings.Contains(string(output), text) || strings.Contains(string(output), `name="products"`) {
		t.Fatal(output, err)
	}
	if _, err := site.renderModelForm(context.Background(), options, object, form, options.ReadonlyFields, nil); err == nil {
		t.Fatal("placeholder masked missing scoped relation evidence")
	}
}
