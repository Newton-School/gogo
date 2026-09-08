package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func genericTodayOptions(t *testing.T, kind models.Kind) (TodayArchiveViewOptions, *genericModelTestBackend) {
	t.Helper()
	day, backend := genericArchiveOptions(t, kind)
	return TodayArchiveViewOptions{
		ListViewOptions: day.ListViewOptions, DateField: day.DateField,
		AllowEmpty: true, Clock: day.Clock,
	}, backend
}

func genericTodayServe(t *testing.T, o TodayArchiveViewOptions, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h, err := NewTodayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGenericTodayArchiveRequiredConfiguration(t *testing.T) {
	for _, mutate := range []func(*TodayArchiveViewOptions){
		func(o *TodayArchiveViewOptions) { o.DateField = "" },
		func(o *TodayArchiveViewOptions) { o.DateField = "title" },
		func(o *TodayArchiveViewOptions) { o.DateField = "published__date" },
		func(o *TodayArchiveViewOptions) { o.Authorize = nil },
		func(o *TodayArchiveViewOptions) { o.Scope = nil },
		func(o *TodayArchiveViewOptions) { o.TemplateName = "../private" },
	} {
		o, b := genericTodayOptions(t, models.Date)
		mutate(&o)
		if h, err := NewTodayArchiveView(o); h != nil || err != ErrGenericConfiguration || b.queries != 0 {
			t.Fatal("invalid today configuration accepted", h, err, b.queries)
		}
	}
	o, b := genericTodayOptions(t, models.Date)
	o.Clock = nil
	if _, err := NewTodayArchiveView(o); err != nil || b.queries != 0 {
		t.Fatal("default clock constructor", err, b.queries)
	}
}

func TestGenericTodayArchiveUsesOneLocalInstant(t *testing.T) {
	for _, test := range []struct {
		zone, instant, date string
		hours               int
	}{
		{"UTC", "2026-01-02T00:00:00Z", "2026-01-02", 24},
		{"America/New_York", "2026-01-02T00:00:00Z", "2026-01-01", 24},
		{"Asia/Kolkata", "2026-01-01T22:00:00Z", "2026-01-02", 24},
		{"America/New_York", "2026-03-08T16:00:00Z", "2026-03-08", 23},
		{"America/New_York", "2026-11-01T17:00:00Z", "2026-11-01", 25},
	} {
		for _, kind := range []models.Kind{models.Date, models.DateTime} {
			for _, future := range []bool{false, true} {
				t.Run(test.zone+test.date+string(kind)+map[bool]string{false: " cutoff", true: " future"}[future], func(t *testing.T) {
					o, b := genericTodayOptions(t, kind)
					o.AllowFuture = future
					o.Templates.LocaleResolver = genericDateResolver(t, test.zone)
					now, err := time.Parse(time.RFC3339, test.instant)
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					o.Clock = func() time.Time {
						calls++
						return now.Add(time.Duration(calls-1) * 48 * time.Hour)
					}
					b.query = func(sql string, args []any) {
						if b.queries != 1 || args[0] != int64(7) {
							t.Fatal(sql, args, b.queries)
						}
						index := 1
						if !future {
							if !strings.Contains(sql, `"published_at" <=`) {
								t.Fatal(sql)
							}
							if kind == models.Date {
								if args[index] != test.date {
									t.Fatal(args)
								}
							} else if !args[index].(time.Time).Equal(now) {
								t.Fatal(args)
							}
							index++
						} else if strings.Contains(sql, `"published_at" <=`) {
							t.Fatal("future cutoff remained", sql)
						}
						if kind == models.Date {
							if args[index] != test.date {
								t.Fatal(args)
							}
						} else {
							start, end := args[index].(time.Time), args[index+1].(time.Time)
							location, err := time.LoadLocation(test.zone)
							if err != nil {
								t.Fatal(err)
							}
							if start.In(location).Format("2006-01-02 15:04:05") != test.date+" 00:00:00" || end.Sub(start) != time.Duration(test.hours)*time.Hour {
								t.Fatal(start, end)
							}
						}
					}
					r := httptest.NewRequest("GET", "/today/1999-01-01/", nil)
					r.SetPathValue("date", "1999-01-01")
					w := genericTodayServe(t, o, r)
					if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "|"+test.date+"|") || strings.Contains(w.Body.String(), "1999-01-01") {
						t.Fatal(w.Code, w.Body.String(), calls)
					}
				})
			}
		}
	}
}

func TestGenericTodayArchiveCapturesScopeBeforeClockAndSkipsUnsafeMethods(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST"} {
		t.Run(method, func(t *testing.T) {
			o, b := genericTodayOptions(t, models.Date)
			o.AllowOptions = true
			ids, events := []int64{7}, []string{}
			o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				events = append(events, "scope")
				return orm.Q("tenant__in", ids), nil
			}
			o.Clock = func() time.Time {
				events = append(events, "clock")
				ids[0] = 99
				return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
			}
			b.query = func(_ string, args []any) {
				events = append(events, "query")
				if args[0] != int64(7) {
					t.Fatal(args)
				}
			}
			w := genericTodayServe(t, o, httptest.NewRequest(method, "/today", nil))
			want := 200
			if method == "POST" {
				want = 405
			}
			if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
			if method == "GET" || method == "HEAD" {
				if !reflect.DeepEqual(events, []string{"scope", "clock", "query"}) {
					t.Fatal(events)
				}
				if method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("HEAD body")
				}
			} else if len(events) != 0 {
				t.Fatal(events)
			}
		})
	}
}

func TestGenericTodayArchiveFailuresNeverEmitOrQuery(t *testing.T) {
	for _, mode := range []string{"auth", "scope", "clock panic", "clock cancel", "year zero", "local year zero", "local year overflow", "ambiguous midnight"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericTodayOptions(t, models.DateTime)
			o.AllowFuture = true // Still needs a usable clock to select today's day.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := 503
			switch mode {
			case "auth":
				o.Authorize = func(*http.Request) error { return auth.ErrPermissionDenied }
				o.Clock = func() time.Time { t.Fatal("denied clock ran"); return time.Time{} }
				want = 403
			case "scope":
				o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
					return db.Predicate{}, errors.New("private")
				}
				o.Clock = func() time.Time { t.Fatal("failed scope clock ran"); return time.Time{} }
			case "clock panic":
				o.Clock = func() time.Time { panic("private") }
			case "clock cancel":
				o.Clock = func() time.Time { cancel(); return time.Now() }
			case "year zero":
				o.Clock = func() time.Time { return time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "local year zero":
				o.Templates.LocaleResolver = genericDateResolver(t, "America/New_York")
				o.Clock = func() time.Time { return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "local year overflow":
				o.Templates.LocaleResolver = genericDateResolver(t, "Asia/Kolkata")
				o.Clock = func() time.Time { return time.Date(9999, 12, 31, 23, 0, 0, 0, time.UTC) }
			case "ambiguous midnight":
				o.Templates.LocaleResolver = genericDateResolver(t, "America/Havana")
				o.Clock = func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }
				want = 404
			}
			w := genericTodayServe(t, o, httptest.NewRequest("GET", "/today", nil).WithContext(ctx))
			if w.Code != want || b.queries != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String(), b.queries)
			}
		})
	}
}

func TestGenericTodayArchiveInheritsCalendarRules(t *testing.T) {
	for _, allow := range []bool{false, true} {
		o, b := genericTodayOptions(t, models.DateTime)
		o.AllowEmpty = allow
		o.Templates.LocaleResolver = genericDateResolver(t, "Pacific/Apia")
		o.Clock = func() time.Time { return time.Date(2011, 12, 31, 0, 0, 0, 0, time.UTC) }
		b.rows = func() db.Rows { return &genericViewTestRows{} }
		o.TemplateViewOptions.Context = nil
		o.TemplateViewOptions.Templates.Loaders = genericTemplateOptions(`{{ day }}|{{ previous_day }}|{{ next_day }}`).Templates.Loaders
		w := genericTodayServe(t, o, httptest.NewRequest("GET", "/today", nil))
		if allow {
			if w.Code != 200 || w.Body.String() != "2011-12-31||" {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
		if b.queries != 1 {
			t.Fatal(b.queries)
		}
	}
}

func TestGenericTodayArchiveSnapshotsAndConcurrentRequests(t *testing.T) {
	o, b := genericTodayOptions(t, models.Date)
	o.TemplateViewOptions = genericTemplateOptions(`{{ day }}|{{ previous_day }}|{{ next_day }}`)
	o.Templates.LocaleResolver = genericDateResolver(t, "America/New_York")
	var calls atomic.Int32
	o.Clock = func() time.Time { calls.Add(1); return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	o.Store.Backend = genericArchiveParallelBackend{Backend: b}
	h, err := NewTodayArchiveView(o)
	if err != nil {
		t.Fatal(err)
	}
	*o.Templates.LocaleResolver = *genericDateResolver(t, "Asia/Kolkata")
	o.AllowFuture, o.DateField = true, "title"
	o.Clock = func() time.Time { panic("replacement") }
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/today", nil))
			if w.Code != 200 || w.Body.String() != "2026-01-01|2025-12-31|" {
				t.Error(w.Code, w.Body.String())
			}
		}()
	}
	group.Wait()
	if calls.Load() != 12 {
		t.Fatal(calls.Load())
	}
}
