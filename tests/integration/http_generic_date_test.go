package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

func genericDateNativeOptions(t *testing.T) (ghttp.DateDetailViewOptions, *observedAPIBackend) {
	t.Helper()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "calendar", Name: "Article", PrimaryKey: []string{"tenant", "id"}, Fields: []models.Field{
		models.TextField("tenant"), models.IntegerField("id"), models.TextField("title"),
		models.DateField("date", models.WithColumn("publication_day"), models.Nullable),
		models.DateTimeField("at", models.WithColumn("publication_instant"), models.Nullable),
	}}
	if err := backend.SchemaEditor().CreateModel(context.Background(), backend, schema); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	observed := &observedAPIBackend{Backend: backend}
	return ghttp.DateDetailViewOptions{
		DetailViewOptions: ghttp.DetailViewOptions{
			TemplateViewOptions: genericHTMLTemplate("dated.html", `{{ object.title }}|{{ object.date }}|{{ object.at }}|{{ object.id }}`),
			ModelReadOptions: ghttp.ModelReadOptions{
				Store: orm.New(observed, registry), Model: schema.Key(), Fields: []string{"title"}, PolicyFields: []string{"tenant"},
				Policy: auth.PolicyFunc(func(_ context.Context, p auth.Principal, _ string, r auth.Resource) error {
					if r.Object != nil {
						value, err := r.Object.(models.Record).Get("tenant")
						if err != nil || value != p.ID {
							return auth.ErrPermissionDenied
						}
					}
					return nil
				}),
				Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
					return orm.Q("tenant", p.ID), nil
				},
			},
			Key: func(r *http.Request) (map[string]any, error) {
				return map[string]any{"tenant": auth.FromContext(r.Context()).ID, "id": urls.Param(r, "id")}, nil
			},
		},
		DateField: "at", AllowFuture: true,
		Date: func(r *http.Request) (string, error) {
			date, ok := urls.Param(r, "date").(string)
			if !ok {
				return "", ghttp.ErrInvalidLookup
			}
			return date, nil
		},
	}, observed
}

func genericDateNativeRouter(t *testing.T, options ghttp.DateDetailViewOptions) *urls.Router {
	t.Helper()
	h, err := ghttp.NewDateDetailView(options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("articles/<str:date>/<int:id>/", h, "dated-detail"))
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func genericDateNativeCall(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "one", Authenticated: true, Active: true})
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest(method, path, nil).WithContext(ctx))
	return out
}

func TestPostgresGenericDateDetailCalendarRangesAndCompositeScope(t *testing.T) {
	for _, test := range []struct{ zone, day, start, end string }{
		{"America/New_York", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"America/New_York", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(test.day, func(t *testing.T) {
			o, backend := genericDateNativeOptions(t)
			resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: test.zone})
			if err != nil {
				t.Fatal(err)
			}
			o.Templates.LocaleResolver = resolver
			o.Clock = func() time.Time { t.Fatal("allow-future invoked clock"); return time.Time{} }
			start, _ := time.Parse(time.RFC3339, test.start)
			end, _ := time.Parse(time.RFC3339, test.end)
			for i, at := range []any{start.Add(-time.Microsecond), start, end.Add(-time.Microsecond), end, nil} {
				if _, err := backend.Exec(context.Background(), `INSERT INTO calendar_article (tenant,id,title,publication_day,publication_instant) VALUES ($1,$2,$3,$4,$5)`, "one", i+1, fmt.Sprintf("<article %d>", i+1), test.day, at); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := backend.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('two',2,'hidden article',$1,$2)`, test.day, start); err != nil {
				t.Fatal(err)
			}
			router := genericDateNativeRouter(t, o)
			for id, want := range map[int]int{1: 404, 2: 200, 3: 200, 4: 404, 5: 404, 99: 404} {
				path, err := router.Reverse("dated-detail", map[string]any{"date": test.day, "id": id}, nil)
				if err != nil {
					t.Fatal(err)
				}
				backend.queries = 0
				out := genericDateNativeCall(router, "GET", path)
				if out.Code != want || backend.queries != 1 || strings.Contains(out.Body.String(), "hidden") || want == 200 && out.Body.String() != fmt.Sprintf("&lt;article %d&gt;|||", id) {
					t.Fatal(id, out.Code, out.Body.String(), backend.queries)
				}
			}
			for _, method := range []string{"HEAD", "OPTIONS", "POST"} {
				backend.queries = 0
				out := genericDateNativeCall(router, method, "/articles/"+test.day+"/2/")
				want, reads := 200, 0
				if method == "POST" {
					want = 405
				}
				if method == "HEAD" {
					reads = 1
				}
				if out.Code != want || backend.queries != reads || method == "HEAD" && out.Body.Len() != 0 {
					t.Fatal(method, out.Code, backend.queries)
				}
			}
			// SQL DATE equality uses the supplied calendar value, not the timestamp
			// timezone: even a row whose instant lies outside this day is eligible.
			o.DateField = "date"
			if out := genericDateNativeCall(genericDateNativeRouter(t, o), "GET", "/articles/"+test.day+"/1/"); out.Code != 200 {
				t.Fatal("Date was instant-filtered", out.Code, out.Body.String())
			}
			if out := genericDateNativeCall(router, "GET", "/articles/2026-02-30/2/"); out.Code != 404 {
				t.Fatal("invalid calendar", out.Code)
			}
			if out := genericDateNativeCall(router, "GET", "/articles/2025-01-01/2/"); out.Code != 404 {
				t.Fatal("mismatched calendar", out.Code)
			}
			var count int64
			if err := db.QueryRow(context.Background(), backend, `SELECT count(*) FROM calendar_article`, nil, &count); err != nil || count != 6 {
				t.Fatal("safe views changed rows", count, err)
			}
		})
	}
}

func TestPostgresGenericDateDetailTodayFutureAndFinalAuthority(t *testing.T) {
	o, backend := genericDateNativeOptions(t)
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "America/New_York"})
	if err != nil {
		t.Fatal(err)
	}
	o.Templates.LocaleResolver = resolver
	o.AllowFuture = false
	clockCalls := 0
	o.Clock = func() time.Time { clockCalls++; return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	for _, statement := range []string{
		`INSERT INTO calendar_article VALUES ('one',1,'later today','2026-01-01','2026-01-02 04:59:00+00')`,
		`INSERT INTO calendar_article VALUES ('one',2,'tomorrow','2026-01-02','2026-01-02 05:00:00+00')`,
	} {
		if _, err := backend.Exec(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
	router := genericDateNativeRouter(t, o)
	if out := genericDateNativeCall(router, "GET", "/articles/2026-01-01/1/"); out.Code != 200 || out.Body.String() != "later today|||" || clockCalls != 1 {
		t.Fatal("today was exact-now filtered", out.Code, out.Body.String(), clockCalls)
	}
	backend.queries = 0
	if out := genericDateNativeCall(router, "GET", "/articles/2026-01-02/2/"); out.Code != 404 || backend.queries != 0 || clockCalls != 2 {
		t.Fatal("future day queried", out.Code, backend.queries, clockCalls)
	}
	o.AllowFuture = true
	if out := genericDateNativeCall(genericDateNativeRouter(t, o), "GET", "/articles/2026-01-02/2/"); out.Code != 200 || clockCalls != 2 {
		t.Fatal("allow future failed", out.Code, clockCalls)
	}
	for _, mode := range []string{"object", "field", "provider"} {
		current := o
		allow := true
		basePolicy := o.Policy
		current.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, a string, r auth.Resource) error {
			if mode == "object" && r.Object != nil && !allow {
				return auth.ErrPermissionDenied
			}
			return basePolicy.Authorize(ctx, p, a, r)
		})
		current.AllowField = func(context.Context, auth.Principal, models.Record, string) (bool, error) {
			return mode != "field" || allow, nil
		}
		current.Context = func(*http.Request) (templates.Context, error) { allow = false; return nil, nil }
		backend.queries = 0
		want := 404
		if mode == "provider" {
			backend.failAt = 1
			want = 503
		}
		out := genericDateNativeCall(genericDateNativeRouter(t, current), "GET", "/articles/2026-01-01/1/")
		backend.failAt = 0
		if out.Code != want || strings.Contains(out.Body.String(), "later today") {
			t.Fatal("late failure leaked dated object", mode, out.Code, out.Body.String())
		}
	}
}
