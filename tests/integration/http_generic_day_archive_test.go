package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

const genericArchiveNativeTemplate = `{% for item in object_list %}{{ item.title }}:{{ item|length }};{% endfor %}|{{ day }}|{{ previous_day }}|{{ next_day }}|{{ previous_month }}|{{ next_month }}|{{ page.next }}`

func genericArchiveNativeOptions(t *testing.T) (ghttp.DayArchiveViewOptions, *observedAPIBackend) {
	t.Helper()
	detail, backend := genericDateNativeOptions(t)
	return ghttp.DayArchiveViewOptions{
		ListViewOptions: ghttp.ListViewOptions{
			TemplateViewOptions: genericHTMLTemplate("archive.html", genericArchiveNativeTemplate),
			ModelReadOptions:    detail.ModelReadOptions,
			Pagination:          pagination.Config{DefaultSize: 1, MaxSize: 2, MaxOffset: 4},
		},
		DateField: "at", Date: detail.Date, AllowFuture: true,
	}, backend
}

func genericArchiveNativeRouter(t *testing.T, o ghttp.DayArchiveViewOptions) *urls.Router {
	t.Helper()
	h, err := ghttp.NewDayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("archive/<str:date>/", h, "article-day"))
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestPostgresGenericDayArchiveDSTScopePaginationAndMethods(t *testing.T) {
	for _, test := range []struct{ day, start, end string }{
		{"2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(test.day, func(t *testing.T) {
			o, b := genericArchiveNativeOptions(t)
			resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "America/New_York"})
			if err != nil {
				t.Fatal(err)
			}
			o.Templates.LocaleResolver = resolver
			start, _ := time.Parse(time.RFC3339, test.start)
			end, _ := time.Parse(time.RFC3339, test.end)
			for index, at := range []any{start.Add(-time.Microsecond), start, end.Add(-time.Microsecond), end, nil} {
				if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('one',$1,$2,$3,$4)`, index+1, fmt.Sprintf("<edge %d>", index+1), test.day, at); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('two',3,'hidden neighbor','2025-12-01','2025-12-01 12:00:00+00')`); err != nil {
				t.Fatal(err)
			}
			policy := o.Policy
			o.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, r auth.Resource) error {
				if r.Object != nil && (!r.Object.(models.Record).State().Deferred["at"] || !r.Object.(models.Record).State().Deferred["date"]) {
					t.Fatal("navigation widened policy fields")
				}
				return policy.Authorize(ctx, p, action, r)
			})
			router := genericArchiveNativeRouter(t, o)
			path, err := router.Reverse("article-day", map[string]any{"date": test.day}, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := start.Add(-time.Microsecond).In(mustArchiveLocation(t, "America/New_York")).Format("2006-01-02")
			after := end.In(mustArchiveLocation(t, "America/New_York")).Format("2006-01-02")
			b.queries = 0
			out := genericDateNativeCall(router, "GET", path)
			previousMonth := ""
			if before[:7] != test.day[:7] {
				previousMonth = before[:7] + "-01"
			}
			want := "&lt;edge 3&gt;:1;|" + test.day + "|" + before + "|" + after + "|" + previousMonth + "||?page=2&amp;page_size=1"
			if out.Code != 200 || out.Body.String() != want || b.queries != 5 {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
			if out := genericDateNativeCall(router, "GET", path+"?page=2&page_size=1"); out.Code != 200 || !strings.HasPrefix(out.Body.String(), "&lt;edge 2&gt;:1;") || strings.Contains(out.Body.String(), "?page=3") {
				t.Fatal(out.Code, out.Body.String())
			}
			if out := genericDateNativeCall(router, "GET", path+"?page=3&page_size=1"); out.Code != 404 {
				t.Fatal("out of range", out.Code)
			}
			for _, method := range []string{"HEAD", "OPTIONS", "POST"} {
				b.queries = 0
				out := genericDateNativeCall(router, method, path)
				status, queries := 200, 0
				if method == "HEAD" {
					queries = 5
				}
				if method == "POST" {
					status = 405
				}
				if out.Code != status || b.queries != queries || method == "HEAD" && out.Body.Len() != 0 {
					t.Fatal(method, out.Code, b.queries)
				}
			}
			var count int64
			if err := db.QueryRow(context.Background(), b, `SELECT count(*) FROM calendar_article`, nil, &count); err != nil || count != 6 {
				t.Fatal("safe request mutated rows", count, err)
			}
		})
	}
}

func mustArchiveLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	value, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPostgresGenericDayArchivePublicationCutoffAndEmptyFuture(t *testing.T) {
	o, b := genericArchiveNativeOptions(t)
	o.Pagination.DefaultSize, o.Pagination.MaxSize = 2, 2
	o.AllowEmpty, o.AllowFuture = true, false
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clockCalls := 0
	o.Clock = func() time.Time { clockCalls++; return now }
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "America/New_York"})
	if err != nil {
		t.Fatal(err)
	}
	o.Templates.LocaleResolver = resolver
	for index, at := range []time.Time{now, now.Add(time.Microsecond), now.Add(5 * time.Hour)} {
		if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('one',$1,$2,'2026-01-01',$3)`, index+1, fmt.Sprintf("row%d", index+1), at); err != nil {
			t.Fatal(err)
		}
	}
	router := genericArchiveNativeRouter(t, o)
	if out := genericDateNativeCall(router, "GET", "/archive/2026-01-01/"); out.Code != 200 || !strings.HasPrefix(out.Body.String(), "row1:1;|") || strings.Contains(out.Body.String(), "row2") || clockCalls != 1 {
		t.Fatal("not exact instant cutoff", out.Code, out.Body.String(), clockCalls)
	}
	b.queries = 0
	if out := genericDateNativeCall(router, "GET", "/archive/2026-01-02/"); out.Code != 200 || !strings.HasPrefix(out.Body.String(), "|2026-01-02|2026-01-01||2025-12-01||") || b.queries != 1 {
		t.Fatal("future empty calendar", out.Code, out.Body.String(), b.queries)
	}
	o.AllowEmpty = false
	if out := genericDateNativeCall(genericArchiveNativeRouter(t, o), "GET", "/archive/2026-01-02/"); out.Code != 404 {
		t.Fatal("default future absence", out.Code)
	}
	o.AllowFuture, o.AllowEmpty = true, true
	o.Clock = func() time.Time { t.Fatal("allow future used clock"); return time.Time{} }
	if out := genericDateNativeCall(genericArchiveNativeRouter(t, o), "GET", "/archive/2026-01-01/"); out.Code != 200 || !strings.HasPrefix(out.Body.String(), "row2:1;row1:1;|") {
		t.Fatal("future today included", out.Code, out.Body.String())
	}
	// Calendar Date values are compared to local today, not the row's instant.
	o.DateField, o.AllowFuture, o.Clock = "date", false, func() time.Time { return now }
	o.Pagination.DefaultSize, o.Pagination.MaxSize = 3, 3
	if out := genericDateNativeCall(genericArchiveNativeRouter(t, o), "GET", "/archive/2026-01-01/"); out.Code != 200 || !strings.HasPrefix(out.Body.String(), "row1:1;row2:1;row3:1;|") {
		t.Fatal("Date used exact-now cutoff", out.Code, out.Body.String())
	}
}

func TestPostgresGenericDayArchiveNeighborGrantsAndCompletion(t *testing.T) {
	o, b := genericArchiveNativeOptions(t)
	for _, row := range []struct {
		id         int
		title, day string
	}{
		{1, "current", "2026-01-15"}, {2, "nearest", "2026-01-14"}, {3, "farther", "2026-01-13"}, {4, "next", "2026-02-04"},
	} {
		if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('one',$1,$2,$3,$4)`, row.id, row.title, row.day, row.day+"T12:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"object", "field", "object final", "field final", "provider", "neighbor provider", "last provider"} {
		t.Run(mode, func(t *testing.T) {
			current := o
			final := false
			policy := o.Policy
			current.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, r auth.Resource) error {
				if r.Object != nil && r.ID.(map[string]any)["id"] == int64(2) && (mode == "object" || mode == "object final" && final) {
					return auth.ErrPermissionDenied
				}
				return policy.Authorize(ctx, p, action, r)
			})
			current.AllowField = func(_ context.Context, _ auth.Principal, r models.Record, field string) (bool, error) {
				id, _ := r.Get("id")
				return !(field == "at" && id == int64(2) && (mode == "field" || mode == "field final" && final)), nil
			}
			current.Context = func(*http.Request) (templates.Context, error) { final = true; return nil, nil }
			b.queries = 0
			if strings.HasSuffix(mode, "provider") {
				b.failAt = 1
				if mode == "neighbor provider" {
					b.failAt = 2
				}
				if mode == "last provider" {
					b.failAt = 5
				}
			}
			out := genericDateNativeCall(genericArchiveNativeRouter(t, current), "GET", "/archive/2026-01-15/")
			b.failAt = 0
			want := 200
			if strings.HasSuffix(mode, "final") {
				want = 403
			}
			if strings.HasSuffix(mode, "provider") {
				want = 503
			}
			if out.Code != want || want != 200 && strings.Contains(out.Body.String(), "current") {
				t.Fatal(out.Code, out.Body.String())
			}
			if want == 200 && (strings.Contains(out.Body.String(), "2026-01-14") || strings.Contains(out.Body.String(), "2026-01-13") || b.queries != 5) {
				t.Fatal("denied nearest bypassed", out.Body.String(), b.queries)
			}
		})
	}
}
