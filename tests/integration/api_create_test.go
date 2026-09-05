package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type apiCreatedProduct struct {
	models.Base
	ID                   int64
	Tenant, Name, Secret string
	CreatedAt            time.Time
}

func (*apiCreatedProduct) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "CreatedProduct", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly),
		models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(100)),
		models.CharField("secret", models.WithStructField("Secret"), models.WithMaxLength(100), models.ReadOnly),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt"), func(f *models.Field) { f.AutoNowAdd = true }),
	}}
}
func (p *apiCreatedProduct) Clean(context.Context) error {
	if p.Name == "model-denied" {
		return models.Invalid("model_denied", "This name is not available.")
	}
	if p.Name == "normalize-me" {
		p.Name = "NORMALIZED"
	}
	return nil
}

func TestPostgresAPIJSONCreateTypedHooksScopeAtomicAuditAndCSRF(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	schema := (&apiCreatedProduct{}).Schema()
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, `CREATE TABLE api_create_audit (object_id bigint NOT NULL, action text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	input, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name", "created_at"}})
	if err != nil {
		t.Fatal(err)
	}
	mode := ""
	resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: output, Policy: auth.ModelPolicy{}, AllowField: func(_ context.Context, _ auth.Principal, record models.Record, _ string) (bool, error) {
		if mode == "representation_mutates" {
			if err := record.Set("name", "unauthorized"); err != nil {
				return false, err
			}
		}
		return true, nil
	}, Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", p.ID), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	hookCalls := 0
	store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		hookCalls++
		product, ok := event.Model.(*apiCreatedProduct)
		if !ok || product.ID == 0 || product.CreatedAt.IsZero() || !db.InTransaction(ctx, backend.Alias()) {
			return errors.New("typed hook did not run inside create transaction")
		}
		switch mode {
		case "retarget":
			product.ID = 1
		case "after_error":
			return errors.New("synthetic after-save failure")
		case "foreign_error":
			return &db.CommittedCallbackError{Errors: []error{errors.New("foreign transaction")}}
		case "foreign_unknown":
			return &db.Error{Code: db.UnknownCommit, Cause: errors.New("unrelated operation")}
		case "on_commit":
			return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("synthetic postcommit failure") }, false)
		}
		return nil
	}}
	options := api.CreateOptions{Input: input, Factory: func() models.Model { return &apiCreatedProduct{} }, Prepare: func(ctx context.Context, record models.Record) error {
		tenant := auth.FromContext(ctx).ID
		if mode == "wrong_scope" {
			tenant = "another-tenant"
		}
		if err := record.Set("tenant", tenant); err != nil {
			return err
		}
		return record.Set("secret", "server-only")
	}, ValidateWrite: func(ctx context.Context, p auth.Principal, record models.Record) error {
		tenant, err := record.Get("tenant")
		if err != nil || tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		if mode == "late_policy_write" && record.State().Persisted {
			id, _ := record.Get("id")
			_, err := db.ExecutorFor(ctx, backend).Exec(ctx, `UPDATE shop_createdproduct SET name='unauthorized' WHERE id=$1`, id)
			return err
		}
		return nil
	}, Audit: func(ctx context.Context, event api.MutationEvent) error {
		if !db.InTransaction(ctx, backend.Alias()) || event.Action != "add" || len(event.Fields) != 1 || event.Fields[0] != "name" {
			return errors.New("invalid audit event")
		}
		if _, err := db.ExecutorFor(ctx, backend).Exec(ctx, `INSERT INTO api_create_audit VALUES ($1,$2)`, event.ObjectKey["id"].(json.Number).String(), event.Action); err != nil {
			return err
		}
		if mode == "audit_error" {
			return errors.New("synthetic audit failure")
		}
		if mode == "audit_write" {
			_, err := db.ExecutorFor(ctx, backend).Exec(ctx, `UPDATE shop_createdproduct SET name='unauthorized' WHERE id=$1`, event.ObjectKey["id"].(json.Number).String())
			return err
		}
		return nil
	}, Location: func(_ context.Context, key api.Values) (string, error) {
		if mode == "bad_location" {
			return "https://evil.test/", nil
		}
		return "/products/" + key["id"].(json.Number).String() + "/", nil
	}}
	handler, err := resource.CreateHandler(options)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: "tenant-one", Authenticated: true, Active: true, Permissions: []string{"shop.add_createdproduct"}}
	bearer, err := auth.ConstrainPrincipal(principal, []string{"shop.add_createdproduct"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(p auth.Principal, body string, headers http.Header, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.test/products/", strings.NewReader(body)).WithContext(auth.WithPrincipal(ctx, p))
		r.Header.Set("Content-Type", "application/json")
		for name, values := range headers {
			r.Header[name] = values
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	counts := func() (int64, int64) {
		t.Helper()
		n, err := orm.For(store, func() *apiCreatedProduct { return &apiCreatedProduct{} }).Count(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var audit int64
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM api_create_audit`, nil, &audit); err != nil {
			t.Fatal(err)
		}
		return n, audit
	}
	w := call(bearer, `{"name":"normalize-me"}`, nil, nil)
	if w.Code != 201 || w.Header().Get("Location") != "/products/1/" || w.Header().Get("X-Gogo-Mutation") != "committed" || strings.Contains(w.Body.String(), "server-only") {
		t.Fatal("valid create failed", w.Code, w.Body.String(), w.Header())
	}
	if !strings.Contains(w.Body.String(), `"name":"NORMALIZED"`) {
		t.Fatal("typed Clean normalization did not reach INSERT", w.Body.String())
	}
	var savedName string
	if err := db.QueryRow(ctx, backend, `SELECT name FROM shop_createdproduct WHERE id=1`, nil, &savedName); err != nil || savedName != "NORMALIZED" {
		t.Fatal("normalization not persisted", savedName, err)
	}
	if n, audit := counts(); n != 1 || audit != 1 || hookCalls != 1 {
		t.Fatal(n, audit, hookCalls)
	}
	// Cookie/custom identities cannot bypass CSRF, even with valid app grants.
	if w := call(principal, `{"name":"CSRF denied"}`, nil, nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	protect, err := security.CSRF(security.CSRFConfig{Secure: true})
	if err != nil {
		t.Fatal(err)
	}
	seed := httptest.NewRecorder()
	protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, security.CSRFToken(r)) })).ServeHTTP(seed, httptest.NewRequest("GET", "https://example.test/form/", nil))
	if w := call(principal, `{"name":"Cookie success"}`, http.Header{"Origin": {"https://example.test"}, "X-Csrftoken": {seed.Body.String()}}, seed.Result().Cookies()); w.Code != 201 {
		t.Fatal("valid CSRF denied", w.Code, w.Body.String())
	}
	for _, test := range []struct {
		body   string
		status int
	}{{`{}`, 422}, {`{"name":"model-denied"}`, 422}, {`{"name":"forged","tenant":"other"}`, 422}, {`{"name":null}`, 422}, {`{"name":"x","name":"y"}`, 400}} {
		if w := call(bearer, test.body, nil, nil); w.Code != test.status || w.Header().Get("X-Gogo-Mutation") != "unchanged" {
			t.Fatal(test.body, w.Code, w.Body.String())
		}
	}
	for _, failure := range []string{"wrong_scope", "retarget", "after_error", "foreign_error", "foreign_unknown", "audit_error", "audit_write", "late_policy_write", "bad_location", "representation_mutates"} {
		mode = failure
		w := call(bearer, `{"name":"Must roll back"}`, nil, nil)
		if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || w.Header().Get("Location") != "" {
			t.Fatal("failed create disclosed success", failure, w.Code, w.Body.String(), w.Header())
		}
		if failure == "foreign_unknown" && (w.Code != 503 || strings.Contains(w.Body.String(), "MUTATION_UNKNOWN")) {
			t.Fatal("foreign uncertainty substituted for outer rollback", w.Code, w.Body.String())
		}
		if n, audit := counts(); n != 2 || audit != 2 {
			t.Fatal("failed create/audit persisted", failure, n, audit)
		}
	}
	mode = ""
	for _, p := range []auth.Principal{{}, {ID: "tenant-one", Authenticated: true}, {ID: "tenant-one", Authenticated: true, Active: true}} {
		w := call(p, `{"name":"Denied"}`, nil, nil)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("invalid principal allowed", w.Code)
		}
	}
	if w := call(bearer, `{"name":"Unsupported key"}`, http.Header{"Idempotency-Key": {"not-enabled"}}, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	mode = "on_commit"
	w = call(bearer, `{"name":"Committed"}`, nil, nil)
	if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "committed" || w.Header().Get("Location") != "" {
		t.Fatal("postcommit outcome lost", w.Code, w.Header())
	}
	if n, audit := counts(); n != 3 || audit != 3 {
		t.Fatal("postcommit effect not durable", n, audit)
	}
	mode = ""
	store.Backend = lostAPICommitBackend{backend}
	w = call(bearer, `{"name":"Uncertain"}`, nil, nil)
	store.Backend = backend
	if w.Code != 503 || w.Header().Get("X-Gogo-Mutation") != "unknown" || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "MUTATION_UNKNOWN") {
		t.Fatal("unknown commit reported success", w.Code, w.Body.String(), w.Header())
	}
	if n, audit := counts(); n != 4 || audit != 4 {
		t.Fatal("lost ack fixture did not commit", n, audit)
	}
}
