package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

type apiUpdatedEagerReference struct {
	models.Base
	ID       int64
	TargetID *int64
	Name     string
}

func (*apiUpdatedEagerReference) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "UpdatedEagerReference", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("name", models.WithStructField("Name")),
		models.ForeignKeyField("target", models.Relation{Target: "shop.UpdateEagerTarget", OnDelete: models.Protect}, models.Nullable, models.WithStructField("TargetID")),
	}}
}

func TestPostgresAPIHTTPUpdateUsesDetailEagerRepresentation(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		for _, related := range []bool{false, true} {
			name := "unkeyed"
			if receipts {
				name = "receipt"
			}
			if related {
				name += "/visible"
			} else {
				name += "/null"
			}
			t.Run(name, func(t *testing.T) { testAPIUpdateEagerRepresentation(t, receipts, related) })
		}
	}
}

func testAPIUpdateEagerRepresentation(t *testing.T, receipts, related bool) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "shop", Name: "UpdateEagerTarget", Fields: []models.Field{models.BigAutoField("id"), models.CharField("name")}}
	schema := (&apiUpdatedEagerReference{}).Schema()
	registry := &models.Registry{}
	for _, definition := range []models.Schema{target, schema} {
		if err := registry.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range []models.Schema{target, schema} {
		if err := editor.CreateModel(ctx, backend, definition); err != nil {
			t.Fatal(err)
		}
	}
	if receipts {
		for _, definition := range api.IdempotencySchemas() {
			if err := backend.SchemaEditor().CreateModel(ctx, backend, definition); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := backend.Exec(ctx, `INSERT INTO shop_updateeagertarget(name) VALUES ('visible target')`); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	row := &apiUpdatedEagerReference{Name: "before"}
	if related {
		id := int64(1)
		row.TargetID = &id
	}
	if err := store.Save(ctx, row, orm.SaveOptions{ForceInsert: true}); err != nil {
		t.Fatal(err)
	}
	input, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := api.New(api.Definition{Fields: []api.Field{
		api.StringField("name"),
		api.ComputedField("target_name", func(_ context.Context, reader api.ValueReader) (any, error) {
			target, loaded := orm.RelatedOne(reader.(models.Record), "target")
			if !loaded {
				return nil, errors.New("expected explicitly selected relation")
			}
			if target == nil {
				return nil, nil
			}
			return target.Get("name")
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: output, Policy: auth.ModelPolicy{}, Scope: func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) { return db.Predicate{}, nil }, SelectRelated: []string{"target"}, EntityTags: true})
	if err != nil {
		t.Fatal(err)
	}
	key := func(*http.Request) (api.Values, error) { return api.Values{"id": row.ID}, nil }
	audits := 0
	options := api.UpdateOptions{Input: input, Factory: func() models.Model { return &apiUpdatedEagerReference{} }, ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil }, Audit: func(context.Context, api.MutationEvent) error { audits++; return nil }, RequireMatch: true}
	if receipts {
		options.Idempotency = &api.MutationIdempotencyOptions{Version: "v1", Scope: func(_ context.Context, p auth.Principal) (string, error) { return p.ID, nil }, Redact: func(_ context.Context, _ auth.Principal, row models.Record, body api.Values) (api.Values, error) {
			if _, loaded := orm.RelatedOne(row, "target"); !loaded {
				return nil, errors.New("receipt lost eager scope shape")
			}
			return body, nil
		}}
	}
	update, err := resource.UpdateHandler(key, options)
	if err != nil {
		t.Fatal(err)
	}
	grants := []string{"shop.view_updatedeagerreference", "shop.change_updatedeagerreference", "shop.add_updatedeagerreference"}
	actor, err := auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: grants}, grants)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, body, tag string) *http.Request {
		r := httptest.NewRequest(method, "https://example.test/items/1/", strings.NewReader(body)).WithContext(auth.WithPrincipal(ctx, actor))
		r.Header.Set("Content-Type", "application/json")
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		if receipts && method == "PATCH" {
			r.Header.Set("Idempotency-Key", "update-eager")
		}
		return r
	}
	got := httptest.NewRecorder()
	resource.DetailHandler(key).ServeHTTP(got, request("GET", "", ""))
	expectedTarget := `"target_name":null`
	if related {
		expectedTarget = `"target_name":"visible target"`
	}
	if got.Code != 200 || !strings.Contains(got.Body.String(), expectedTarget) {
		t.Fatal("detail eager shape", got.Code, got.Body.String())
	}
	digest := sha256.Sum256(got.Body.Bytes())
	tag := `"` + hex.EncodeToString(digest[:]) + `"`
	if got.Header().Get("ETag") != tag {
		t.Fatal("validator does not identify exact emitted bytes")
	}
	changed := httptest.NewRecorder()
	update.ServeHTTP(changed, request("PATCH", `{"name":"after"}`, tag))
	if changed.Code != 200 || !strings.Contains(changed.Body.String(), expectedTarget) || !strings.Contains(changed.Body.String(), `"name":"after"`) || changed.Header().Get("ETag") != "" || audits != 1 {
		t.Fatal("unchanged detail tag or update eager projection failed", changed.Code, changed.Body.String(), audits)
	}
	if receipts {
		replayed := httptest.NewRecorder()
		update.ServeHTTP(replayed, request("PATCH", `{"name":"after"}`, tag))
		if replayed.Code != 200 || replayed.Header().Get("Idempotency-Replayed") != "true" || replayed.Body.String() != changed.Body.String() || replayed.Header().Get("ETag") != "" || audits != 1 {
			t.Fatal("same-key old-tag eager receipt did not replay", replayed.Code, replayed.Body.String(), audits)
		}
	}
	// Creation and update use the same explicit eager representation shape.
	// This also exercises the root-only receipt lock for a nullable outer join.
	create, err := resource.CreateHandler(api.CreateOptions{
		Input: input, Factory: options.Factory, ValidateWrite: options.ValidateWrite,
		Audit: options.Audit, Idempotency: options.Idempotency,
		Prepare: func(_ context.Context, proposed models.Record) error {
			if related {
				return proposed.Set("target", int64(1))
			}
			return nil
		},
		Location: func(_ context.Context, key api.Values) (string, error) {
			return fmt.Sprintf("/items/%v/", key["id"]), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	createRequest := func() *http.Request {
		r := request("POST", `{"name":"created"}`, "")
		if receipts {
			r.Header.Set("Idempotency-Key", "create-eager")
		}
		return r
	}
	created := httptest.NewRecorder()
	create.ServeHTTP(created, createRequest())
	if created.Code != 201 || !strings.Contains(created.Body.String(), expectedTarget) || !strings.Contains(created.Body.String(), `"name":"created"`) || created.Header().Get("Location") == "" || audits != 2 {
		t.Fatal("created eager projection failed", created.Code, created.Body.String(), audits)
	}
	if receipts {
		replayed := httptest.NewRecorder()
		create.ServeHTTP(replayed, createRequest())
		if replayed.Code != 201 || replayed.Header().Get("Idempotency-Replayed") != "true" || replayed.Body.String() != created.Body.String() || replayed.Header().Get("Location") != created.Header().Get("Location") || audits != 2 {
			t.Fatal("created eager receipt failed", replayed.Code, replayed.Body.String(), audits)
		}
	}
}
