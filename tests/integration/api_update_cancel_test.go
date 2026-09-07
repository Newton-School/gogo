package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type updateKeyObservedBackend struct {
	db.Backend
	begins, queries int
}

func (b *updateKeyObservedBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	b.begins++
	return b.Backend.BeginTx(ctx, options)
}
func (b *updateKeyObservedBackend) Query(ctx context.Context, sql string, args ...any) (db.Rows, error) {
	b.queries++
	return b.Backend.Query(ctx, sql, args...)
}

func TestPostgresAPIHTTPUpdateCanceledDecoderNeverBecomesNotFound(t *testing.T) {
	backend := &updateKeyObservedBackend{Backend: testservice.Postgres(t)}
	schema := (&apiUpdatedItem{}).Schema()
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	serializer, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := api.NewResource(api.ResourceConfig{Store: orm.New(backend, registry), Model: schema.Key(), Serializer: serializer, Policy: auth.ModelPolicy{}, Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	grants := []string{"shop.change_updateditem"}
	actor, err := auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: grants}, grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PUT", "PATCH"} {
		for _, test := range []struct {
			name string
			key  api.Values
			err  error
		}{
			{"nil", nil, nil}, {"empty", api.Values{}, nil}, {"wrong_field", api.Values{"wrong": 1}, nil},
			{"malformed", api.Values{"id": "invalid"}, nil}, {"valid", api.Values{"id": 1}, nil}, {"invalid_key_error", nil, api.ErrInvalidKey},
		} {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(auth.WithPrincipal(context.Background(), actor))
				defer cancel()
				key := func(*http.Request) (api.Values, error) { cancel(); return test.key, test.err }
				handler, err := resource.UpdateHandler(key, api.UpdateOptions{Input: serializer, Factory: func() models.Model { return &apiUpdatedItem{} }, ValidateWrite: func(context.Context, auth.Principal, models.Record) error {
					t.Error("canceled request reached write policy")
					return nil
				}, Audit: func(context.Context, api.MutationEvent) error { t.Error("canceled request reached audit"); return nil }})
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(method, "https://example.test/items/key/", strings.NewReader(`{}`)).WithContext(ctx)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 503 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || !strings.Contains(w.Body.String(), `"code":"UNAVAILABLE"`) || backend.begins != 0 || backend.queries != 0 {
					t.Fatal("canceled route decoder misclassified or reached database", w.Code, w.Body.String(), backend.begins, backend.queries)
				}
			})
		}
	}
}
