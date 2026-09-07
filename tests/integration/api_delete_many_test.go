package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestPostgresAPIHTTPDeleteAutomaticJoinEndpointAuthority(t *testing.T) {
	for _, mode := range []string{"success", "deny_graph", "resurrect_join"} {
		t.Run(mode, func(t *testing.T) {
			b := testservice.Postgres(t)
			ctx := context.Background()
			target := models.Schema{AppLabel: "deletion", Name: "Tag", Fields: []models.Field{models.BigAutoField("id"), models.CharField("tenant")}}
			source := models.Schema{AppLabel: "deletion", Name: "Post", Fields: []models.Field{models.BigAutoField("id"), models.CharField("tenant"), models.ManyToManyField("tags", models.Relation{Target: target.Key()})}}
			registry := &models.Registry{}
			for _, s := range []models.Schema{target, source} {
				if err := registry.Register(s); err != nil {
					t.Fatal(err)
				}
			}
			if err := registry.Freeze(); err != nil {
				t.Fatal(err)
			}
			engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "deletion", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}}}
			if err := engine.Apply(ctx, ""); err != nil {
				t.Fatal(err)
			}
			store := orm.New(b, registry)
			field, _ := source.Field("tags")
			through, err := models.ImplicitThrough(source, field, target)
			if err != nil {
				t.Fatal(err)
			}
			exec := func(ctx context.Context, query string, args ...any) {
				t.Helper()
				if _, err := db.ExecutorFor(ctx, b).Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(ctx, `INSERT INTO deletion_post (tenant) VALUES ('one'),('two')`)
			exec(ctx, `INSERT INTO deletion_tag (tenant) VALUES ('two')`)
			exec(ctx, "INSERT INTO "+through.DBTable()+" (source_id,target_id) VALUES (1,1)")
			exec(ctx, `CREATE TABLE deletion_many_audit (id bigint)`)
			output, err := api.FromModel(source, api.ModelOptions{Fields: []string{"id"}})
			if err != nil {
				t.Fatal(err)
			}
			resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: source.Key(), Serializer: output, Policy: auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, r auth.Resource) error {
				if r.Model != "Post" {
					return errors.New("synthetic or target model was independently authorized")
				}
				return (auth.ModelPolicy{}).Authorize(ctx, p, action, r)
			}), Scope: func(_ context.Context, p auth.Principal, s models.Schema) (db.Predicate, error) {
				if s.Key() != source.Key() {
					return db.Predicate{}, errors.New("join or hidden target reached application scope")
				}
				return orm.Q("tenant", p.ID), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			checks := 0
			h, err := resource.DeleteHandler(func(*http.Request) (api.Values, error) { return api.Values{"id": 1}, nil }, api.DeleteOptions{ValidateDelete: func(_ context.Context, _ auth.Principal, plan orm.DeletionPlan) error {
				checks++
				if len(plan.Objects) != 1 || len(plan.JoinRemovals) != 1 || len(plan.Updates) != 0 || plan.JoinRemovals[0].Endpoint.Schema().Key() != source.Key() {
					return errors.New("invalid automatic join graph")
				}
				if mode == "deny_graph" {
					return auth.ErrPermissionDenied
				}
				return nil
			}, Audit: func(ctx context.Context, _ api.MutationEvent) error {
				exec(ctx, `INSERT INTO deletion_many_audit VALUES (1)`)
				if mode == "resurrect_join" {
					exec(ctx, "INSERT INTO "+through.DBTable()+" (id,source_id,target_id) VALUES (1,2,1)")
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			grants := []string{"deletion.delete_post"}
			actor, err := auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: grants}, grants)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("DELETE", "https://example.test/posts/1/", nil).WithContext(auth.WithPrincipal(ctx, actor))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			count := func(table string) int64 {
				t.Helper()
				var n int64
				if err := db.QueryRow(ctx, b, "SELECT count(*) FROM "+table, nil, &n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if mode == "success" {
				if w.Code != 204 || count(source.DBTable()) != 1 || count(through.DBTable()) != 0 || count("deletion_many_audit") != 1 || checks != 2 {
					t.Fatal("join deletion failed", w.Code, w.Body.String(), checks)
				}
			} else {
				want := 404
				if mode == "resurrect_join" {
					want = 409
				}
				if w.Code != want || w.Header().Get("X-Gogo-Mutation") != "unchanged" || count(source.DBTable()) != 2 || count(through.DBTable()) != 1 || count("deletion_many_audit") != 0 {
					t.Fatal("join denial escaped transaction", w.Code, w.Body.String())
				}
				if mode == "resurrect_join" && !strings.Contains(w.Body.String(), "DELETE_CONFLICT") {
					t.Fatal("final join fence did not detect resurrection")
				}
			}
			if count(target.DBTable()) != 1 {
				t.Fatal("hidden opposite endpoint deleted")
			}
		})
	}
}
