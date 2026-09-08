package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/urls"
)

func TestPostgresGenericTodayArchiveSingleClockAndPublicationScope(t *testing.T) {
	for _, bounds := range []struct{ day, start, end string }{
		{"2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(bounds.day, func(t *testing.T) {
			day, b := genericArchiveNativeOptions(t)
			start, _ := time.Parse(time.RFC3339, bounds.start)
			end, _ := time.Parse(time.RFC3339, bounds.end)
			now := start.Add(12 * time.Hour)
			for index, row := range []struct {
				title   string
				instant any
			}{
				{"before", start.Add(-time.Microsecond)}, {"<now>", now},
				{"<later>", now.Add(time.Microsecond)}, {"tomorrow", end}, {"null", nil},
			} {
				if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('one',$1,$2,$3,$4)`, index+1, row.title, bounds.day, row.instant); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('two',2,'hidden',$1,$2)`, bounds.day, now); err != nil {
				t.Fatal(err)
			}
			resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "America/New_York"})
			if err != nil {
				t.Fatal(err)
			}
			for _, future := range []bool{false, true} {
				o := ghttp.TodayArchiveViewOptions{ListViewOptions: day.ListViewOptions, DateField: "at", AllowEmpty: true, AllowFuture: future}
				o.Templates.LocaleResolver = resolver
				o.Pagination = pagination.Config{DefaultSize: 10, MaxSize: 10}
				calls := 0
				o.Clock = func() time.Time { calls++; return now.Add(time.Duration(calls-1) * 48 * time.Hour) }
				h, err := ghttp.NewTodayArchiveView(o)
				if err != nil {
					t.Fatal(err)
				}
				router, err := urls.New(urls.Path("articles/today/", h, "article-today"))
				if err != nil {
					t.Fatal(err)
				}
				path, err := router.Reverse("article-today", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				b.queries = 0
				out := genericDateNativeCall(router, http.MethodGet, path)
				want := "&lt;now&gt;:1;|" + bounds.day + "|"
				if future {
					want = "&lt;later&gt;:1;" + want
				}
				if out.Code != 200 || !strings.HasPrefix(out.Body.String(), want) || calls != 1 || b.queries != 1 {
					t.Fatal(out.Code, out.Body.String(), calls, b.queries)
				}
				for _, hidden := range []string{"before", "tomorrow", "null", "hidden"} {
					if strings.Contains(out.Body.String(), hidden) {
						t.Fatal("out-of-scope output", out.Body.String())
					}
				}
				// HEAD performs the same one-clock read but never emits its body.
				calls, b.queries = 0, 0
				out = genericDateNativeCall(router, http.MethodHead, path)
				if out.Code != 200 || out.Body.Len() != 0 || calls != 1 || b.queries != 1 {
					t.Fatal(out.Code, calls, b.queries)
				}
				calls, b.queries = 0, 0
				out = genericDateNativeCall(router, http.MethodOptions, path)
				if out.Code != 200 || calls != 0 || b.queries != 0 {
					t.Fatal(out.Code, calls, b.queries)
				}
				calls, b.queries, b.failAt = 0, 0, 1
				out = genericDateNativeCall(router, http.MethodGet, path)
				b.failAt = 0
				if out.Code != 503 || strings.Contains(out.Body.String(), "&lt;now&gt;") {
					t.Fatal(out.Code, out.Body.String())
				}
			}
			var rows int64
			if err := db.QueryRow(context.Background(), b, `SELECT count(*) FROM calendar_article`, nil, &rows); err != nil || rows != 6 {
				t.Fatal(rows, err)
			}
		})
	}
}

func TestPostgresGenericTodayArchiveLateGrantCannotEmit(t *testing.T) {
	day, b := genericArchiveNativeOptions(t)
	if _, err := b.Exec(context.Background(), `INSERT INTO calendar_article VALUES ('one',1,'visible','2026-01-01','2026-01-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	o := ghttp.TodayArchiveViewOptions{ListViewOptions: day.ListViewOptions, DateField: "at", AllowEmpty: true, Clock: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }}
	policy := o.Policy
	calls := 0
	o.Policy = auth.PolicyFunc(func(ctx context.Context, p auth.Principal, action string, r auth.Resource) error {
		if r.Object != nil {
			calls++
			if calls > 1 {
				return auth.ErrPermissionDenied
			}
		}
		return policy.Authorize(ctx, p, action, r)
	})
	h, err := ghttp.NewTodayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	out := genericDateNativeCall(h, http.MethodGet, "/today/")
	if out.Code != 403 || strings.Contains(out.Body.String(), "visible") || calls != 2 {
		t.Fatal(out.Code, out.Body.String(), calls)
	}
}
