package integration_test

import (
	"context"
	"encoding/json"
	"errors"
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

const createdReferenceID = "28bd14e3-9287-446d-85e6-a8b5a2f9d981"

type apiCreatedReference struct {
	models.Base
	ID, Tenant, Name string
	TargetID         int64
}

func (*apiCreatedReference) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "CreatedReference", Fields: []models.Field{
		models.UUIDField("id", models.Primary, models.WithStructField("ID"), models.WithDefaultFunc("test-reference-id", func() any { return createdReferenceID })),
		models.CharField("tenant", models.ReadOnly, models.WithStructField("Tenant")),
		models.CharField("name", models.WithStructField("Name")),
		models.ForeignKeyField("target", models.Relation{Target: "shop.CreateTarget", OnDelete: models.Protect}, models.WithStructField("TargetID")),
	}}
}

func TestPostgresAPIJSONCreateDefaultIdentityAndRelationScopeRechecks(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "shop", Name: "CreateTarget", Fields: []models.Field{
		models.BigAutoField("id"), models.CharField("tenant"),
	}}
	schema := (&apiCreatedReference{}).Schema()
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
	if _, err := backend.Exec(ctx, `INSERT INTO shop_createtarget (tenant) VALUES ('one'),('two')`); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(ctx, `CREATE TABLE api_reference_audit (id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	// Even an application's custom scalar override cannot bypass the
	// resource's mandatory target scope after hooks and at transaction exit.
	input, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name", "target"}, Overrides: map[string]api.Field{"target": api.IntegerField("target")}})
	if err != nil {
		t.Fatal(err)
	}
	mode := ""
	output, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name"}})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: output, Policy: auth.ModelPolicy{}, Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", p.ID), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	store.BeforeSave = []orm.SaveReceiver{func(_ context.Context, event orm.SaveEvent) error {
		if mode == "retarget" {
			return event.Record.Set("target", int64(2))
		}
		return nil
	}}
	handler, err := resource.CreateHandler(api.CreateOptions{
		Input: input, Factory: func() models.Model { return &apiCreatedReference{} },
		Prepare: func(ctx context.Context, r models.Record) error { return r.Set("tenant", auth.FromContext(ctx).ID) },
		ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
			tenant, err := r.Get("tenant")
			if err != nil || tenant != p.ID {
				return auth.ErrPermissionDenied
			}
			return nil
		},
		Audit: func(ctx context.Context, event api.MutationEvent) error {
			if event.ObjectKey["id"] != createdReferenceID {
				return errors.New("generated primary key lost")
			}
			if _, err := db.ExecutorFor(ctx, backend).Exec(ctx, `INSERT INTO api_reference_audit VALUES ($1)`, event.ObjectKey["id"]); err != nil {
				return err
			}
			if mode == "move_target" {
				_, err := db.ExecutorFor(ctx, backend).Exec(ctx, `UPDATE shop_createtarget SET tenant='two' WHERE id=1`)
				return err
			}
			return nil
		},
		Location: func(_ context.Context, key api.Values) (string, error) {
			return "/references/" + key["id"].(string) + "/", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: []string{"shop.add_createdreference"}}, []string{"shop.add_createdreference"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.test/references/", strings.NewReader(body)).WithContext(auth.WithPrincipal(ctx, principal))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	var unavailableBody string
	for _, scenario := range []string{"hidden", "missing", "retarget", "move_target"} {
		mode = scenario
		body := `{"name":"reference","target":1}`
		if scenario == "hidden" {
			body = `{"name":"reference","target":2}`
		} else if scenario == "missing" {
			body = `{"name":"reference","target":99}`
		}
		w := call(body)
		if w.Code != 422 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || w.Header().Get("Location") != "" {
			t.Fatal(scenario, w.Code, w.Body.String(), w.Header())
		}
		if scenario == "hidden" {
			unavailableBody = w.Body.String()
		} else if w.Body.String() != unavailableBody {
			t.Fatal("relation denial discloses target existence", scenario, w.Body.String(), unavailableBody)
		}
		var records, audits int64
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM shop_createdreference`, nil, &records); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM api_reference_audit`, nil, &audits); err != nil || records != 0 || audits != 0 {
			t.Fatal("denied reference/audit persisted", scenario, records, audits, err)
		}
		var tenant string
		if err := db.QueryRow(ctx, backend, `SELECT tenant FROM shop_createtarget WHERE id=1`, nil, &tenant); err != nil || tenant != "one" {
			t.Fatal("failed target move did not roll back", tenant, err)
		}
	}
	mode = ""
	w := call(`{"name":"reference","target":1}`)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || body["id"] != createdReferenceID || w.Header().Get("Location") != "/references/"+createdReferenceID+"/" || w.Header().Get("X-Gogo-Mutation") != "committed" {
		t.Fatal("scoped UUID create failed", w.Code, w.Body.String(), w.Header())
	}
}
