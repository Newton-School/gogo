package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type apiDeleteFixture struct {
	t              *testing.T
	backend        db.Backend
	store          *orm.Store
	schema         models.Schema
	resource       *api.Resource
	actor          auth.Principal
	mode           string
	checks, audits int
	retained       orm.DeletionPlan
	key            func(*http.Request) (api.Values, error)
	options        api.DeleteOptions
}

func newAPIDeleteFixture(t *testing.T, relation models.DeletePolicy, defaultValue any) *apiDeleteFixture {
	t.Helper()
	b := testservice.Postgres(t)
	parent := models.Schema{AppLabel: "deletion", Name: "Parent", Fields: []models.Field{
		models.BigAutoField("id"), models.CharField("tenant"), models.CharField("name"),
		models.CharField("code", func(f *models.Field) { f.Unique = true }),
	}}
	r := models.Relation{Target: parent.Key(), OnDelete: relation}
	opts := []models.FieldOption{models.Nullable}
	if defaultValue != nil {
		opts = append(opts, models.WithDefault(defaultValue))
	}
	if defaultValue == "fallback" {
		r.TargetFields = []string{"code"}
	}
	child := models.Schema{AppLabel: "deletion", Name: "Child", Fields: []models.Field{
		models.BigAutoField("id"), models.CharField("tenant"), models.ForeignKeyField("parent", r, opts...),
	}}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{parent, child} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	editor, err := b.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []models.Schema{parent, child} {
		if err := editor.CreateModel(context.Background(), b, schema); err != nil {
			t.Fatal(err)
		}
	}
	f := &apiDeleteFixture{t: t, backend: b, store: orm.New(b, registry), schema: parent}
	f.sql(context.Background(), `INSERT INTO deletion_parent (tenant,name,code) VALUES ('one','original','root'),('one','fallback','fallback'),('two','hidden','hidden')`)
	if defaultValue == "fallback" {
		f.sql(context.Background(), `INSERT INTO deletion_child (tenant,parent) VALUES ('one','root')`)
	} else {
		f.sql(context.Background(), `INSERT INTO deletion_child (tenant,parent) VALUES ('one',1)`)
	}
	f.sql(context.Background(), `CREATE TABLE deletion_audit (object_id bigint NOT NULL, action text NOT NULL)`)
	output, err := api.FromModel(parent, api.ModelOptions{Fields: []string{"id", "name"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, resource auth.Resource) error {
		if err := (auth.ModelPolicy{}).Authorize(ctx, p, action, resource); err != nil {
			return err
		}
		if resource.Object == nil {
			return nil
		}
		switch f.mode {
		case "deny_child":
			if resource.Model == "Child" {
				return auth.ErrPermissionDenied
			}
		case "deny_target":
			if action == "view" {
				return auth.ErrPermissionDenied
			}
		case "retarget_identity":
			if values, ok := resource.ID.(api.Values); ok {
				values["id"] = int64(3)
			} else {
				return errors.New("policy identity fixture was not exercised")
			}
		case "policy_mutation":
			return resource.Object.(models.Record).Set("tenant", "two")
		}
		return nil
	})
	f.resource, err = api.NewResource(api.ResourceConfig{Store: f.store, Model: parent.Key(), Serializer: output, EntityTags: true, Policy: policy, Scope: func(ctx context.Context, p auth.Principal, s models.Schema) (db.Predicate, error) {
		if f.mode == "scope_resurrect" && f.audits > 0 {
			f.sql(ctx, `INSERT INTO deletion_parent (id,tenant,name,code) VALUES (1,'two','resurrected','root') ON CONFLICT (id) DO NOTHING`)
		}
		return orm.Q("tenant", p.ID), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	grants := []string{"deletion.delete_parent", "deletion.view_parent", "deletion.delete_child", "deletion.change_child"}
	f.actor, err = auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: grants}, grants)
	if err != nil {
		t.Fatal(err)
	}
	f.key = func(r *http.Request) (api.Values, error) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		return api.Values{"id": parts[len(parts)-1]}, nil
	}
	f.store.BeforeDelete = []orm.DeleteReceiver{func(ctx context.Context, event orm.DeleteEvent) error {
		switch f.mode {
		case "before_mutation":
			return event.Record.Set("tenant", "two")
		case "before_error":
			return errors.New("synthetic private before detail")
		}
		return nil
	}}
	f.store.AfterDelete = []orm.DeleteReceiver{func(ctx context.Context, event orm.DeleteEvent) error {
		if event.Record.Schema().Name != "Parent" {
			return nil
		}
		switch f.mode {
		case "after_resurrect":
			f.sql(ctx, `INSERT INTO deletion_parent (id,tenant,name,code) VALUES (1,'two','resurrected','root')`)
		case "after_panic":
			panic("synthetic private after detail")
		case "foreign_commit":
			return &db.CommittedCallbackError{Errors: []error{errors.New("synthetic foreign commit")}}
		case "foreign_unknown":
			return &db.Error{Code: db.UnknownCommit, Cause: errors.New("synthetic foreign uncertainty")}
		case "postcommit":
			return db.OnCommit(ctx, b.Alias(), func(context.Context) error { return errors.New("synthetic private after commit") }, false)
		case "retained_mutation":
			if len(f.retained.Objects) > 0 {
				_ = f.retained.Objects[0].Set("id", int64(3))
			}
		}
		return nil
	}}
	f.options = api.DeleteOptions{RequireMatch: true, ValidateDelete: func(ctx context.Context, p auth.Principal, plan orm.DeletionPlan) error {
		f.checks++
		for _, record := range plan.Objects {
			tenant, _ := record.Get("tenant")
			if tenant != p.ID {
				return auth.ErrPermissionDenied
			}
		}
		if f.checks == 1 {
			f.retained = plan
		}
		switch f.mode {
		case "graph_mutation":
			plan.Objects[0] = plan.Objects[len(plan.Objects)-1]
		case "deny_graph":
			return auth.ErrPermissionDenied
		case "late_deny_graph":
			if f.checks > 1 {
				return auth.ErrPermissionDenied
			}
		case "late_resurrect":
			if f.checks > 1 {
				f.sql(ctx, `INSERT INTO deletion_parent (id,tenant,name,code) VALUES (1,'two','resurrected','root')`)
			}
		case "late_target":
			if f.checks > 1 {
				f.sql(ctx, `UPDATE deletion_parent SET name='changed' WHERE id=2`)
			}
		}
		return nil
	}, Audit: func(ctx context.Context, event api.MutationEvent) error {
		f.audits++
		if !db.InTransaction(ctx, b.Alias()) || event.Action != "delete" || len(event.ObjectKey) != 1 || event.ObjectKey["id"] != json.Number("1") {
			return errors.New("invalid deletion audit contract")
		}
		f.sql(ctx, `INSERT INTO deletion_audit VALUES ($1,$2)`, event.ObjectKey["id"].(json.Number).String(), event.Action)
		switch f.mode {
		case "audit_error":
			return errors.New("synthetic private audit detail")
		case "audit_resurrect":
			f.sql(ctx, `INSERT INTO deletion_parent (id,tenant,name,code) VALUES (1,'two','resurrected','root')`)
		case "undo_update":
			f.sql(ctx, `UPDATE deletion_child SET parent=3 WHERE id=1`)
		case "move_child":
			f.sql(ctx, `UPDATE deletion_child SET tenant='two' WHERE id=1`)
		case "move_target":
			f.sql(ctx, `UPDATE deletion_parent SET tenant='two' WHERE id=2`)
		case "retarget_audit":
			event.ObjectKey["id"] = json.Number("3")
		}
		return nil
	}}
	return f
}

func (f *apiDeleteFixture) sql(ctx context.Context, query string, args ...any) {
	f.t.Helper()
	if _, err := db.ExecutorFor(ctx, f.backend).Exec(ctx, query, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *apiDeleteFixture) count(table string) int64 {
	f.t.Helper()
	var n int64
	if err := db.QueryRow(context.Background(), f.backend, "SELECT count(*) FROM "+table, nil, &n); err != nil {
		f.t.Fatal(err)
	}
	return n
}
func (f *apiDeleteFixture) handler() http.Handler {
	f.t.Helper()
	h, err := f.resource.DeleteHandler(f.key, f.options)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}
func (f *apiDeleteFixture) request(h http.Handler, method string, id int, body string, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "https://example.test/items/"+strconv.Itoa(id)+"/", reader).WithContext(auth.WithPrincipal(context.Background(), f.actor))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func (f *apiDeleteFixture) delete(h http.Handler) *httptest.ResponseRecorder {
	return f.request(h, "DELETE", 1, "", map[string]string{"If-Match": "*"})
}

func TestPostgresAPIHTTPDeleteCascadeAndReadOnlyGraph(t *testing.T) {
	for _, mode := range []string{"", "retarget_identity", "retarget_audit", "retained_mutation"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPIDeleteFixture(t, models.Cascade, nil)
			f.mode = mode
			h := f.handler()
			w := f.delete(h)
			if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("X-Gogo-Mutation") != "committed" || f.count("deletion_parent") != 2 || f.count("deletion_child") != 0 || f.count("deletion_audit") != 1 || f.checks != 2 {
				t.Fatal("cascade failed", w.Code, w.Body.String(), w.Header(), f.checks)
			}
			if w = f.delete(h); w.Code != 404 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || f.count("deletion_audit") != 1 {
				t.Fatal("deleted identity replayed", w.Code, w.Body.String())
			}
		})
	}
}

func TestPostgresAPIHTTPDeleteRollbackEveryEffectBoundary(t *testing.T) {
	for _, mode := range []string{"deny_child", "deny_graph", "late_deny_graph", "policy_mutation", "graph_mutation", "before_mutation", "before_error", "after_resurrect", "after_panic", "foreign_commit", "foreign_unknown", "audit_error", "audit_resurrect", "late_resurrect"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPIDeleteFixture(t, models.Cascade, nil)
			f.mode = mode
			w := f.delete(f.handler())
			if w.Code < 400 || w.Code == 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || strings.Contains(w.Body.String(), "private") || f.count("deletion_parent") != 3 || f.count("deletion_child") != 1 || f.count("deletion_audit") != 0 {
				t.Fatal("partial delete survived", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}

func TestPostgresAPIHTTPDeleteRelationshipPolicies(t *testing.T) {
	for _, item := range []struct {
		name   string
		policy models.DeletePolicy
		value  any
		want   int
	}{{"null", models.SetNull, nil, 204}, {"default", models.SetDefault, int64(2), 204}, {"string_integer_default", models.SetDefault, "2", 204}, {"non_pk_default", models.SetDefault, "fallback", 204}, {"protect", models.Protect, nil, 409}, {"restrict", models.Restrict, nil, 409}, {"do_nothing", models.DoNothing, nil, 409}} {
		t.Run(item.name, func(t *testing.T) {
			f := newAPIDeleteFixture(t, item.policy, item.value)
			w := f.delete(f.handler())
			if w.Code != item.want {
				t.Fatal("relation policy failed", w.Code, w.Body.String())
			}
			if f.count("deletion_child") != 1 {
				t.Fatal("updated/protected child removed")
			}
			if item.want == 204 {
				var actual any
				if err := db.QueryRow(context.Background(), f.backend, `SELECT parent FROM deletion_child`, nil, &actual); err != nil {
					t.Fatal(err)
				}
				if item.value == nil && actual != nil {
					t.Fatal("SET_NULL did not persist", actual)
				}
				if item.value != nil && actual != int64(2) && actual != "fallback" {
					t.Fatal("SET_DEFAULT did not persist", actual)
				}
			} else if f.count("deletion_parent") != 3 || f.count("deletion_audit") != 0 {
				t.Fatal("protected effect persisted")
			}
		})
	}
	for _, mode := range []string{"undo_update", "move_child", "move_target", "deny_target", "late_target", "scope_resurrect"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPIDeleteFixture(t, models.SetDefault, int64(2))
			f.mode = mode
			w := f.delete(f.handler())
			if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || f.count("deletion_parent") != 3 || f.count("deletion_audit") != 0 {
				t.Fatal("relation fence bypassed", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}
