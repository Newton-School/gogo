package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
)

const genericArchiveTemplate = `{% for item in object_list %}{{ item.title }}:{{ item|length }};{% endfor %}|{{ day }}|{{ previous_day }}|{{ next_day }}|{{ previous_month }}|{{ next_month }}|{{ page.next }}|{{ date_list|length }}`

func genericArchiveOptions(t *testing.T, kind models.Kind) (DayArchiveViewOptions, *genericModelTestBackend) {
	t.Helper()
	detail, backend := genericDateOptions(t, kind)
	options := DayArchiveViewOptions{
		ListViewOptions: ListViewOptions{TemplateViewOptions: genericTemplateOptions(genericArchiveTemplate), ModelReadOptions: detail.ModelReadOptions, Pagination: pagination.Config{DefaultSize: 2, MaxSize: 2, MaxOffset: 4}},
		DateField:       "published", Date: func(*http.Request) (string, error) { return "2026-01-15", nil },
		Clock: func() time.Time { return time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC) },
	}
	backend.rows = func() db.Rows {
		if backend.queries == 1 {
			return &genericViewTestRows{values: [][]any{{"<one>", int64(1)}, {"two", int64(2)}, {"sentinel", int64(3)}}}
		}
		day := []string{"2026-01-14", "2026-01-20", "2025-12-04", "2026-02-02"}[(backend.queries-2)%4]
		return &genericViewTestRows{values: [][]any{{"neighbor", int64(backend.queries * 10), day}}}
	}
	return options, backend
}

func genericArchiveServe(t *testing.T, o DayArchiveViewOptions, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h, err := NewDayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, request)
	return out
}

func TestGenericDayArchiveConfiguration(t *testing.T) {
	for _, mutate := range []func(*DayArchiveViewOptions){
		func(o *DayArchiveViewOptions) { o.Date = nil },
		func(o *DayArchiveViewOptions) { o.DateField = "" },
		func(o *DayArchiveViewOptions) { o.DateField = "title" },
		func(o *DayArchiveViewOptions) { o.DateField = "published__date" },
		func(o *DayArchiveViewOptions) { o.Authorize = nil },
		func(o *DayArchiveViewOptions) { o.Scope = nil },
		func(o *DayArchiveViewOptions) { o.TemplateName = "../private" },
		func(o *DayArchiveViewOptions) { o.Ordering = []string{"--published"} },
		func(o *DayArchiveViewOptions) { o.Pagination.Mode = pagination.CursorMode },
		func(o *DayArchiveViewOptions) { o.Pagination.MaxSize = 201 },
	} {
		o, b := genericArchiveOptions(t, models.Date)
		mutate(&o)
		if h, err := NewDayArchiveView(o); h != nil || err != ErrGenericConfiguration || b.queries != 0 {
			t.Fatal("invalid constructor accepted", h, err, b.queries)
		}
	}
}

func TestGenericDayArchiveProjectionScopeNavigationAndMethods(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST"} {
		t.Run(method, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.Date)
			o.AllowOptions = true
			ids, events := []int64{7}, []string{}
			o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				events = append(events, "scope")
				return orm.Q("tenant__in", ids), nil
			}
			o.Date = func(r *http.Request) (string, error) {
				events = append(events, "date")
				ids[0] = 99
				r.URL.RawQuery = "page=999"
				return "2026-01-15", nil
			}
			o.Clock = func() time.Time {
				events = append(events, "clock")
				return time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
			}
			objectCalls, dateCalls := 0, 0
			o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
				if r.Object != nil {
					objectCalls++
					if !r.Object.(models.Record).State().Deferred["published"] || r.ID.(map[string]any)["id"] == int64(3) {
						t.Fatal("navigation date or sentinel widened policy record")
					}
					_ = r.Object.(models.Record).Set("title", "mutated")
					r.ID.(map[string]any)["id"] = int64(999)
				}
				return nil
			})
			o.AllowField = func(_ context.Context, _ auth.Principal, record models.Record, name string) (bool, error) {
				value, _ := record.Get("title")
				if value == "mutated" || !record.State().Deferred["published"] {
					t.Fatal("callback record was shared or widened")
				}
				if name == "published" {
					dateCalls++
				}
				return true, nil
			}
			b.query = func(sql string, args []any) {
				events = append(events, "query")
				if args[0] != int64(7) || args[1] != "2026-02-15" || !strings.Contains(sql, `"published_at" <=`) {
					t.Fatal("query lost frozen scope or cutoff", sql, args)
				}
				if b.queries == 1 {
					if !strings.HasPrefix(sql, `SELECT "title", "id"`) || !strings.Contains(sql, `ORDER BY "published_at" DESC, "id" ASC`) || !reflect.DeepEqual(args, []any{int64(7), "2026-02-15", "2026-01-15", 3, 0}) {
						t.Fatal("page selection/order/bounds", sql, args)
					}
				} else if !strings.HasPrefix(sql, `SELECT "title", "id", "published_at"`) || args[len(args)-1] != 1 || len(args) != 4 {
					t.Fatal("navigation unbounded or inherited page filter", sql, args)
				}
			}
			o.ExtraContext = templates.Context{"day": "private", "next_month": "private"}
			out := genericArchiveServe(t, o, httptest.NewRequest(method, "/archive", nil))
			want := 200
			if method == "POST" {
				want = 405
			}
			if out.Code != want || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(out.Code, out.Body.String())
			}
			if method == "GET" || method == "HEAD" {
				if b.queries != 5 || objectCalls != 12 || dateCalls != 8 || !reflect.DeepEqual(events[:3], []string{"scope", "date", "clock"}) {
					t.Fatal(b.queries, objectCalls, dateCalls, events)
				}
				if method == "HEAD" && out.Body.Len() != 0 {
					t.Fatal("HEAD body")
				}
				if method == "GET" && out.Body.String() != "&lt;one&gt;:1;two:1;|2026-01-15|2026-01-14|2026-01-20|2025-12-01|2026-02-01|?page=2&amp;page_size=2|0" {
					t.Fatal(out.Body.String())
				}
			} else if b.queries != 0 || len(events) != 0 {
				t.Fatal("nonread invoked callbacks", events)
			}
		})
	}
}

func TestGenericDayArchiveEmptyPaginationAndCalendarNavigation(t *testing.T) {
	for _, test := range []struct {
		name, date, query string
		empty, future     bool
		status            int
		nav               string
	}{
		{"empty denied", "2026-01-15", "", false, false, 404, ""},
		{"empty allowed", "2026-01-15", "", true, false, 200, "2026-01-14|2026-01-16|2025-12-01|2026-02-01"},
		{"future empty denied", "2026-02-16", "", false, false, 404, ""},
		{"future empty allowed", "2026-02-16", "", true, false, 200, "2026-02-15||2026-01-01|"},
		{"future allowed", "2026-02-16", "", true, true, 200, "2026-02-15|2026-02-17|2026-01-01|2026-03-01"},
		{"out of range", "2026-01-15", "?page=2", true, false, 404, ""},
		{"unknown", "2026-01-15", "?private=x", true, false, 400, ""},
		{"repeated", "2026-01-15", "?page=1&page=1", true, false, 400, ""},
		{"mixed", "2026-01-15", "?offset=0", true, false, 400, ""},
		{"offset bound", "2026-01-15", "?page=4", true, false, 400, ""},
		{"invalid date", "2025-02-29", "", true, false, 404, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.Date)
			o.TemplateViewOptions = genericTemplateOptions(`{{ previous_day }}|{{ next_day }}|{{ previous_month }}|{{ next_month }}`)
			o.AllowEmpty, o.AllowFuture = test.empty, test.future
			o.Date = func(*http.Request) (string, error) { return test.date, nil }
			b.rows = func() db.Rows { return &genericViewTestRows{} }
			if test.future {
				o.Clock = func() time.Time { t.Fatal("unneeded clock"); return time.Time{} }
			}
			out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive"+test.query, nil))
			if out.Code != test.status || b.queries > 1 || out.Code == 200 && out.Body.String() != test.nav {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
		})
	}
}

func TestGenericDayArchiveCalendarAndExactInstantCutoff(t *testing.T) {
	for _, test := range []struct {
		kind                  models.Kind
		zone, day, start, end string
	}{
		{models.DateTime, "America/New_York", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{models.DateTime, "America/New_York", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
		{models.Date, "Asia/Kolkata", "2024-02-29", "", ""},
	} {
		t.Run(test.day, func(t *testing.T) {
			o, b := genericArchiveOptions(t, test.kind)
			o.AllowEmpty = true
			o.Templates.LocaleResolver = genericDateResolver(t, test.zone)
			o.Date = func(*http.Request) (string, error) { return test.day, nil }
			clock, _ := time.Parse(time.RFC3339, test.day+"T12:00:00Z")
			o.Clock = func() time.Time { return clock }
			b.rows = func() db.Rows { return &genericViewTestRows{} }
			b.query = func(sql string, args []any) {
				if test.kind == models.Date {
					if args[1] != test.day || args[2] != test.day {
						t.Fatal(args)
					}
				} else {
					start, _ := time.Parse(time.RFC3339, test.start)
					end, _ := time.Parse(time.RFC3339, test.end)
					if !args[1].(time.Time).Equal(clock) || !args[2].(time.Time).Equal(start) || !args[3].(time.Time).Equal(end) {
						t.Fatal("not exact-now/local-day", sql, args)
					}
				}
			}
			if out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil)); out.Code != 200 || b.queries != 1 {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
		})
	}
}

func TestGenericDayArchiveNeighborDenialAndFinalFence(t *testing.T) {
	for _, mode := range []string{"object", "field", "object final", "field final", "operational", "render", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.Date)
			final := false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
				if r.Object == nil || r.ID.(map[string]any)["id"] != int64(20) {
					return nil
				}
				if mode == "object" || mode == "object final" && final {
					return auth.ErrPermissionDenied
				}
				if mode == "operational" {
					return errors.Join(auth.ErrPermissionDenied, errors.New("private provider"))
				}
				return nil
			})
			o.AllowField = func(_ context.Context, _ auth.Principal, record models.Record, name string) (bool, error) {
				id, _ := record.Get("id")
				return !(id == int64(20) && name == "published" && (mode == "field" || mode == "field final" && final)), nil
			}
			o.Context = func(*http.Request) (templates.Context, error) {
				final = true
				if mode == "render" {
					return nil, errors.New("private renderer")
				}
				if mode == "cancel" {
					cancel()
				}
				return nil, nil
			}
			out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil).WithContext(ctx))
			want := 503
			if mode == "object" || mode == "field" {
				want = 200
			}
			if strings.HasSuffix(mode, "final") {
				want = 403
			}
			if out.Code != want || strings.Contains(out.Body.String(), "private") || want != 200 && strings.Contains(out.Body.String(), "&lt;one&gt;") {
				t.Fatal(mode, out.Code, out.Body.String())
			}
			if want == 200 && (b.queries != 5 || strings.Contains(out.Body.String(), "2026-01-14")) {
				t.Fatal("denied neighbor shown or scanned past", out.Body.String(), b.queries)
			}
		})
	}
}

func TestGenericDayArchiveCompletionAndAggregateBudget(t *testing.T) {
	for _, mode := range []string{"page close", "neighbor close", "neighbor extra", "neighbor nil", "neighbor wrong date", "budget"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.Date)
			o.TemplateViewOptions = genericTemplateOptions(`{{ object_list|length }}`)
			base := b.rows
			readers := []*genericViewTestRows{}
			b.rows = func() db.Rows {
				r := base().(*genericViewTestRows)
				if mode == "page close" && b.queries == 1 || mode == "neighbor close" && b.queries == 2 {
					r.closeErr = errors.New("private completion")
				}
				if b.queries == 2 {
					if mode == "neighbor extra" {
						r.values = append(r.values, r.values[0])
					}
					if mode == "neighbor nil" {
						r.values[0][2] = nil
					}
					if mode == "neighbor wrong date" {
						r.values[0][2] = "2026-01-16"
					}
				}
				if mode == "budget" {
					text := strings.Repeat("x", 4<<20)
					if b.queries == 1 {
						r.values = [][]any{{text, int64(1)}}
					} else {
						r.values[0][0] = text
					}
				}
				readers = append(readers, r)
				return r
			}
			if mode == "budget" {
				// The first page independently fits. Adding its neighbor must fail
				// the shared operation budget, not an individual field decoder.
				o.AllowEmpty = true
				if out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil)); out.Code != 200 {
					t.Fatal("positive budget fixture", out.Code)
				}
				b.queries, readers, o.AllowEmpty = 0, nil, false
			}
			out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil))
			if out.Code != 503 || b.queries > 2 || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
			for _, r := range readers {
				if r.closes != 1 {
					t.Fatal("reader not completed exactly once", r.closes)
				}
			}
		})
	}
}

func TestGenericDayArchiveUnsupportedNeighborIntervalsAreOmitted(t *testing.T) {
	for _, test := range []struct {
		name, zone, day string
		empty           bool
		queries         int
		want            string
	}{
		{"skipped previous day", "Pacific/Apia", "2011-12-31", true, 1, "|2012-01-01|2011-11-01|2012-01-01"},
		{"folded previous day", "America/Havana", "2020-11-02", true, 1, "|2020-11-03|2020-10-01|2020-12-01"},
		{"month boundary only", "America/Havana", "2020-11-15", false, 4, "|||"},
		{"earliest calendar", "UTC", "0001-01-01", true, 1, "|0001-01-02||0001-02-01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.DateTime)
			o.TemplateViewOptions = genericTemplateOptions(`{{ previous_day }}|{{ next_day }}|{{ previous_month }}|{{ next_month }}`)
			o.Templates.LocaleResolver = genericDateResolver(t, test.zone)
			o.Date = func(*http.Request) (string, error) { return test.day, nil }
			o.AllowEmpty, o.AllowFuture = test.empty, true
			b.rows = func() db.Rows {
				if b.queries == 1 {
					return &genericViewTestRows{values: [][]any{{"one", int64(1)}}}
				}
				return &genericViewTestRows{}
			}
			out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil))
			if out.Code != 200 || out.Body.String() != test.want || b.queries != test.queries {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
		})
	}
	// A discovered row on a folded day is not an excuse to scan to another day.
	o, b := genericArchiveOptions(t, models.DateTime)
	o.AllowFuture = true
	o.Templates.LocaleResolver = genericDateResolver(t, "America/Havana")
	o.Date = func(*http.Request) (string, error) { return "2020-11-02", nil }
	b.rows = func() db.Rows {
		if b.queries == 1 {
			return &genericViewTestRows{values: [][]any{{"one", int64(1)}}}
		}
		if b.queries == 2 {
			return &genericViewTestRows{values: [][]any{{"hidden calendar", int64(2), time.Date(2020, 11, 1, 12, 0, 0, 0, time.UTC)}}}
		}
		return &genericViewTestRows{}
	}
	if out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil)); out.Code != 200 || strings.Contains(out.Body.String(), "2020-11-01") || b.queries != 4 {
		t.Fatal(out.Code, out.Body.String(), b.queries)
	}
}

func TestGenericDayArchiveFailureAndCancellationBoundaries(t *testing.T) {
	for _, mode := range []string{"date exact", "date mixed", "date panic", "date cancel", "clock panic", "clock invalid", "clock cancel", "scope", "provider", "neighbor cancel", "requested gap"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericArchiveOptions(t, models.DateTime)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want, reads := 503, 0
			switch mode {
			case "date exact":
				o.Date = func(*http.Request) (string, error) { return "", ErrInvalidLookup }
				want = 404
			case "date mixed":
				o.Date = func(*http.Request) (string, error) { return "", errors.Join(ErrInvalidLookup, errors.New("private")) }
			case "date panic":
				o.Date = func(*http.Request) (string, error) { panic("private") }
			case "date cancel":
				o.Date = func(*http.Request) (string, error) { cancel(); return "2026-01-15", nil }
			case "clock panic":
				o.Clock = func() time.Time { panic("private") }
			case "clock invalid":
				o.Clock = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "clock cancel":
				o.Clock = func() time.Time { cancel(); return time.Now() }
			case "scope":
				o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
					return db.Predicate{}, errors.New("private")
				}
			case "provider":
				b.failure = errors.New("private")
				reads = 1
			case "neighbor cancel":
				b.query = func(string, []any) {
					if b.queries == 2 {
						cancel()
					}
				}
				reads = 2
			case "requested gap":
				o.Date = func(*http.Request) (string, error) { return "2011-12-30", nil }
				o.Templates.LocaleResolver = genericDateResolver(t, "Pacific/Apia")
				want = 404
			}
			out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive", nil).WithContext(ctx))
			if out.Code != want || b.queries != reads || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String(), b.queries)
			}
		})
	}
}

type genericArchiveParallelBackend struct{ db.Backend }

func (genericArchiveParallelBackend) Query(context.Context, string, ...any) (db.Rows, error) {
	return &genericViewTestRows{values: [][]any{{"one", int64(1)}}}, nil
}

func TestGenericDayArchiveConstructorSnapshotAndConcurrentRequests(t *testing.T) {
	o, b := genericArchiveOptions(t, models.Date)
	o.AllowEmpty = true
	o.TemplateViewOptions = genericTemplateOptions(`{{ day }}|{{ previous_day }}|{{ next_day }}`)
	o.Templates.LocaleResolver = genericDateResolver(t, "America/New_York")
	o.Date = func(r *http.Request) (string, error) { return r.PathValue("day"), nil }
	o.Clock = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	o.Store.Backend = genericArchiveParallelBackend{Backend: b}
	h, err := NewDayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	*o.Templates.LocaleResolver = *genericDateResolver(t, "Asia/Kolkata")
	o.AllowFuture, o.DateField = true, "title"
	o.Date = func(*http.Request) (string, error) { return "private", nil }
	o.Clock = func() time.Time { panic("replacement clock") }
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r := httptest.NewRequest("GET", "/archive", nil)
			r.SetPathValue("day", "2026-01-01")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != 200 || out.Body.String() != "2026-01-01|2025-12-31|" {
				t.Error(out.Code, out.Body.String())
			}
		}()
	}
	group.Wait()
}

func TestGenericDayArchiveOffsetAndExplicitEmptyOrdering(t *testing.T) {
	o, b := genericArchiveOptions(t, models.Date)
	o.AllowEmpty = true
	o.Ordering = []string{}
	o.Pagination.Mode = pagination.LimitOffset
	b.rows = func() db.Rows {
		return &genericViewTestRows{values: [][]any{{"one", int64(1)}, {"sentinel", int64(2)}}}
	}
	b.query = func(sql string, args []any) {
		if !strings.Contains(sql, `ORDER BY "id" ASC`) || strings.Contains(sql, `ORDER BY "published_at"`) || args[len(args)-1] != 1 {
			t.Fatal(sql, args)
		}
	}
	if out := genericArchiveServe(t, o, httptest.NewRequest("GET", "/archive?offset=1&limit=1", nil)); out.Code != 200 || !strings.Contains(out.Body.String(), "?limit=1&amp;offset=2") {
		t.Fatal(out.Code, out.Body.String())
	}
}
