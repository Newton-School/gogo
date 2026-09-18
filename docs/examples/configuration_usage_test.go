package examples_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
	"github.com/Newton-School/gogo/docs/snippets/catalog"
)

func Example_settingDefinition() {
	// docs:begin setting-definition
	schema := conf.CoreSchema()
	schema = append(schema, conf.Definition{
		Name: "GOGO_PAGE_SIZE", Group: "Catalog", Kind: conf.Integer,
		Default: "25", Min: 1, Choices: []string{"25", "50", "100"},
	})
	values, err := schema.Load(map[string]string{"GOGO_PAGE_SIZE": "50"})
	if err != nil {
		panic(err)
	}
	fmt.Println(values.Int("GOGO_PAGE_SIZE")) // 50
	// docs:end setting-definition
	// Output: 50
}

func Example_requiredSecretDefinition() {
	// docs:begin setting-secret
	schema := conf.Schema{{
		Name: "GOGO_DELIVERY_KEY", Kind: conf.String,
		Sensitive: true, RequiredFor: []string{"delivery"},
	}}
	_, err := schema.Load(map[string]string{}, "delivery")
	fmt.Println(err) // CONFIG_REQUIRED: GOGO_DELIVERY_KEY
	// docs:end setting-secret
	// Output: CONFIG_REQUIRED: GOGO_DELIVERY_KEY
}

func Example_relationOptions() {
	// docs:begin relation-options
	category := models.ForeignKeyField("category", models.Relation{
		Target: "catalog.Category", TargetFields: []string{"id"},
		OnDelete:    models.Protect,
		RelatedName: "products", RelatedQueryName: "product",
	})
	// Add category to Product.Schema().Fields.
	// Register both Product and Category schemas before resolving relations.
	// docs:end relation-options
	fmt.Println(category.Relation.Target, category.Relation.RelatedName)
	// Output: catalog.Category products
}

func Example_modelFormOptions() {
	// docs:begin model-form-record
	record, err := models.Bind(&catalog.Product{Price: "0.00"})
	if err != nil {
		panic(err)
	}
	// docs:end model-form-record
	// docs:begin model-form-options
	form, err := forms.NewModelForm(context.Background(), record,
		forms.ModelFormOptions{Fields: []string{"name", "price"}},
		forms.WithData(url.Values{"name": {"Notebook"}, "price": {"19.95"}}),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(form.IsValid()) // true; no save has happened.
	// docs:end model-form-options
	// Output: true
}

func Example_formSetOptions() {
	// docs:begin formset-options
	options := forms.FormSetOptions{
		Prefix: "items", Minimum: 1, Maximum: 10, AbsoluteMaximum: 20,
		ExistingIDs: []string{}, // No editable existing rows in this create form.
		CanDelete:   false, CanOrder: true,
	}
	values := url.Values{
		"items-TOTAL_FORMS": {"1"}, "items-INITIAL_FORMS": {"0"},
		"items-0-name": {"Notebook"}, "items-0-ORDER": {"1"},
	}
	set, err := forms.BindFormSet(context.Background(),
		[]forms.Field{forms.NewField("name", forms.Char)}, values, options)
	if err != nil {
		panic(err)
	}
	fmt.Println(set.IsValid()) // true; rows are not persisted.
	// docs:end formset-options
	// Output: true
}

func Example_inputWidget() {
	// docs:begin input-widget
	field := forms.NewField("price", forms.Decimal)
	field.MaxDigits, field.DecimalPlaces = 12, 2
	field.Widget = forms.InputWidget{
		Type: "number", Attrs: map[string]string{"step": "0.01"},
	}
	bound := forms.BoundField{Field: field, Name: "price", ID: "id_price", Value: "19.95"}
	html, err := bound.HTML()
	if err != nil {
		panic(err)
	}
	// docs:end input-widget
	fmt.Println(strings.Contains(string(html), `step="0.01"`))
	// Output: true
}

func Example_multiWidget() {
	// docs:begin multi-widget
	field := forms.NewField("starts_at", forms.SplitDateTime)
	field.Widget = forms.MultiWidget{
		Widgets: []forms.Widget{
			forms.InputWidget{Type: "date"}, forms.InputWidget{Type: "time"},
		},
	}
	bound := forms.BoundField{Field: field, Name: "starts_at", ID: "id_starts_at",
		Value: time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)}
	html, err := bound.HTML()
	if err != nil {
		panic(err)
	}
	// docs:end multi-widget
	fmt.Println(strings.Contains(string(html), "starts_at_0"), strings.Contains(string(html), "starts_at_1"))
	// Output: true true
}

func Example_modelSerializerOptions() {
	schema := (&catalog.Product{}).Schema()
	// docs:begin model-serializer-options
	serializer, err := api.FromModel(schema, api.ModelOptions{
		Fields: []string{"id", "name", "price"}, Readonly: []string{"id"},
	})
	if err != nil {
		panic(err)
	}
	values, err := serializer.Validate(context.Background(),
		api.Values{"name": "Notebook", "price": "19.95"}, api.BindOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(values["name"]) // Notebook
	// docs:end model-serializer-options
	// Output: Notebook
}

func Example_headersConfig() {
	// docs:begin headers-config
	middleware, err := security.Headers(security.HeadersConfig{
		AllowedHosts: []string{"localhost", "127.0.0.1"},
		FramePolicy:  "DENY", ReferrerPolicy: "same-origin",
		CORSOrigins: []string{"http://localhost:3000"},
		CORSMethods: []string{"GET", "POST"}, CORSHeaders: []string{"Content-Type"},
	})
	if err != nil {
		panic(err)
	}
	app := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// docs:end headers-config
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/", nil))
	fmt.Println(response.Code, response.Header().Get("X-Frame-Options"))
	// Output: 204 DENY
}

func Example_csrfConfig() {
	// docs:begin csrf-config
	middleware, err := security.CSRF(security.CSRFConfig{
		CookieName: "catalog_csrf", Secure: false, // Local HTTP only; true for HTTPS.
		TrustedOrigins: []string{"http://localhost:3000"}, MaxBodyBytes: 1 << 20,
	})
	if err != nil {
		panic(err)
	}
	app := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, security.CSRFToken(r)) // Supply to your form's CSRF field.
	}))
	// docs:end csrf-config
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/", nil))
	fmt.Println(response.Code, response.Body.Len() > 0)
	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("POST", "http://localhost/", nil))
	fmt.Println(response.Code)
	// Output:
	// 200 true
	// 403
}

// docs:begin session-middleware
func sessionHandler(store sessions.Store, signer *security.Signer, next http.Handler) (http.Handler, error) {
	middleware, err := sessions.Middleware(sessions.MiddlewareConfig{
		Store: store, Signer: signer, CookieName: "catalog_session",
		TTL: 24 * time.Hour, Secure: true,
	})
	if err != nil {
		return nil, err
	}
	return middleware(next), nil
}

// docs:end session-middleware

func Example_sessionMiddleware() {
	_, err := sessionHandler(nil, nil, http.NotFoundHandler())
	fmt.Println(err != nil) // Providers must be supplied; no implicit fallback.
	// Output: true
}
