package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/templates"
)

func genericDateOptions(t *testing.T, kind models.Kind) (DateDetailViewOptions, *genericModelTestBackend) {
	t.Helper()
	schema := genericModelTestSchema()
	field := models.DateField("published", models.WithColumn("published_at"), models.Nullable)
	if kind == models.DateTime {
		field = models.DateTimeField("published", models.WithColumn("published_at"), models.Nullable)
	}
	schema.Fields = append(schema.Fields, field)
	model, backend := genericModelTestOptions(t, schema)
	model.AllowAnonymous = true
	backend.failure = nil
	backend.rows = func() db.Rows { return &genericViewTestRows{values: [][]any{{"<one>", int64(9)}}} }
	return DateDetailViewOptions{
		DetailViewOptions: DetailViewOptions{
			TemplateViewOptions: genericTemplateOptions(`{{ object.title }}|{{ object|length }}`),
			ModelReadOptions:    model,
			Key:                 func(*http.Request) (map[string]any, error) { return map[string]any{"id": "9"}, nil },
		},
		DateField: "published",
		Date:      func(*http.Request) (string, error) { return "2026-01-02", nil },
		Clock:     func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}, backend
}

func genericDateResolver(t *testing.T, defaultZone string, zones ...string) *i18n.Resolver {
	t.Helper()
	resolver, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: defaultZone, TimeZones: zones})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func genericDateServe(t *testing.T, options DateDetailViewOptions, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewDateDetailView(options)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestGenericDateDetailConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DateDetailViewOptions)
	}{
		{"date", func(o *DateDetailViewOptions) { o.Date = nil }},
		{"key", func(o *DateDetailViewOptions) { o.Key = nil }},
		{"field missing", func(o *DateDetailViewOptions) { o.DateField = "missing" }},
		{"field empty", func(o *DateDetailViewOptions) { o.DateField = "" }},
		{"field text", func(o *DateDetailViewOptions) { o.DateField = "title" }},
		{"field lookup", func(o *DateDetailViewOptions) { o.DateField = "published__date" }},
		{"grant", func(o *DateDetailViewOptions) { o.Authorize = nil }},
		{"scope", func(o *DateDetailViewOptions) { o.Scope = nil }},
		{"template", func(o *DateDetailViewOptions) { o.TemplateName = "../private" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, backend := genericDateOptions(t, models.Date)
			test.mutate(&o)
			if h, err := NewDateDetailView(o); h != nil || err != ErrGenericConfiguration || backend.queries != 0 {
				t.Fatal("invalid date configuration accepted", h, err, backend.queries)
			}
		})
	}
	for _, field := range []models.Field{
		{Name: "published", Kind: models.Date, Codec: genericModelTestCodec{}},
		{Name: "published", Kind: models.DateTime, Relation: &models.Relation{Target: "library.Book"}},
	} {
		schema := genericModelTestSchema()
		schema.Fields = append(schema.Fields, field)
		o, _ := genericDateOptions(t, models.Date)
		o.ModelReadOptions, _ = genericModelTestOptions(t, schema)
		if h, err := NewDateDetailView(o); h != nil || err != ErrGenericConfiguration {
			t.Fatal("unsupported date descriptor accepted", err)
		}
	}
}

func TestGenericDateDetailScopeOrderProjectionAndMethods(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST"} {
		t.Run(method, func(t *testing.T) {
			o, backend := genericDateOptions(t, models.Date)
			o.AllowOptions = true
			events := []string{}
			ids := []int64{7}
			o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				events = append(events, "scope")
				return orm.Q("tenant__in", ids), nil
			}
			o.Date = func(r *http.Request) (string, error) {
				events = append(events, "date")
				ids[0] = 99
				r.URL.Path = "/other"
				r.Header.Set("X-Value", "other")
				r.SetPathValue("id", "other")
				return "2026-01-02", nil
			}
			o.Clock = func() time.Time { events = append(events, "clock"); return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
			o.Key = func(r *http.Request) (map[string]any, error) {
				events = append(events, "key")
				if r.URL.Path != "/books/9" || r.Header.Get("X-Value") != "original" || r.PathValue("id") != "9" {
					t.Fatal("date decoder retargeted key request")
				}
				return map[string]any{"id": "9"}, nil
			}
			backend.query = func(sql string, args []any) {
				events = append(events, "query")
				if !reflect.DeepEqual(args, []any{int64(7), "2026-01-02", int64(9), 2}) || !strings.Contains(sql, `"published_at" =`) || strings.Contains(sql, "ORDER BY") || !strings.HasPrefix(sql, `SELECT "title", "id"`) {
					t.Fatal("date query lost scope/bounds or widened selection", sql, args)
				}
			}
			o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
				if resource.Object != nil {
					record := resource.Object.(models.Record)
					if !record.State().Deferred["published"] {
						t.Fatal("date filter auto-loaded hidden field into policy record")
					}
				}
				return nil
			})
			req := httptest.NewRequest(method, "/books/9?application=value", nil)
			req.Header.Set("X-Value", "original")
			req.SetPathValue("id", "9")
			out := genericDateServe(t, o, req)
			want := 200
			if method == "POST" {
				want = 405
			}
			if out.Code != want || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("method/status changed", out.Code, out.Header())
			}
			if method == "GET" || method == "HEAD" {
				if !reflect.DeepEqual(events, []string{"scope", "date", "clock", "key", "query"}) || method == "GET" && out.Body.String() != "&lt;one&gt;|1" || method == "HEAD" && out.Body.Len() != 0 {
					t.Fatal("date execution/projection changed", events, out.Body.String())
				}
			} else if len(events) != 0 || backend.queries != 0 {
				t.Fatal("non-read reached date callbacks", events)
			}
		})
	}
}

func TestGenericDateDetailDayBoundaries(t *testing.T) {
	for _, test := range []struct {
		zone, date, start, end string
		kind                   models.Kind
		status                 int
	}{
		{"America/New_York", "2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", models.DateTime, 200},
		{"America/New_York", "2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", models.DateTime, 200},
		{"Asia/Kolkata", "2024-02-29", "2024-02-28T18:30:00Z", "2024-02-29T18:30:00Z", models.DateTime, 200},
		{"Pacific/Apia", "2011-12-30", "", "", models.DateTime, 404},
		{"America/Havana", "2020-11-01", "", "", models.DateTime, 404},
		{"America/Havana", "2020-03-08", "", "", models.DateTime, 404},
		{"UTC", "9999-12-31", "", "", models.DateTime, 404},
		{"Asia/Kolkata", "0001-01-01", "", "", models.DateTime, 404},
		{"UTC", "9999-12-31", "", "", models.Date, 200},
		{"Pacific/Apia", "2011-12-30", "", "", models.Date, 200},
	} {
		t.Run(test.zone+"/"+test.date+"/"+string(test.kind), func(t *testing.T) {
			o, backend := genericDateOptions(t, test.kind)
			o.Templates.LocaleResolver = genericDateResolver(t, test.zone)
			o.AllowFuture = true
			o.Clock = func() time.Time { t.Fatal("allow-future called clock"); return time.Time{} }
			o.Date = func(*http.Request) (string, error) { return test.date, nil }
			backend.query = func(sql string, args []any) {
				if test.kind == models.Date {
					if args[1] != test.date {
						t.Fatal("date was timezone-converted", args)
					}
					return
				}
				start, _ := time.Parse(time.RFC3339, test.start)
				end, _ := time.Parse(time.RFC3339, test.end)
				if len(args) != 5 || !args[1].(time.Time).Equal(start) || !args[2].(time.Time).Equal(end) || !strings.Contains(sql, `"published_at" >=`) || !strings.Contains(sql, `"published_at" <`) {
					t.Fatal("day boundary normalized or used fixed duration", sql, args)
				}
			}
			out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil))
			if out.Code != test.status || test.status == 404 && backend.queries != 0 {
				t.Fatal("date boundary outcome", out.Code, out.Body.String(), backend.queries)
			}
		})
	}
}

func TestGenericDateDetailFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DateDetailViewOptions, *genericModelTestBackend)
		status int
	}{
		{"empty", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { return "", nil }
		}, 404},
		{"invalid leap", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { return "2025-02-29", nil }
		}, 404},
		{"invalid sentinel", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { return "", ErrInvalidLookup }
		}, 404},
		{"mixed sentinel", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { return "", errors.Join(ErrInvalidLookup, errors.New("private")) }
		}, 503},
		{"panic", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { panic("private") }
		}, 503},
		{"scope", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				return db.Predicate{}, ErrNotFound
			}
			o.Date = func(*http.Request) (string, error) { panic("must not decode") }
		}, 503},
		{"future", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Date = func(*http.Request) (string, error) { return "2026-01-03", nil }
		}, 404},
		{"clock panic", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Clock = func() time.Time { panic("private") }
		}, 503},
		{"clock range", func(o *DateDetailViewOptions, _ *genericModelTestBackend) {
			o.Clock = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
		}, 503},
		{"provider", func(_ *DateDetailViewOptions, b *genericModelTestBackend) { b.failure = errors.New("private") }, 503},
		{"missing", func(_ *DateDetailViewOptions, b *genericModelTestBackend) {
			b.rows = func() db.Rows { return &genericViewTestRows{} }
		}, 404},
		{"duplicate", func(_ *DateDetailViewOptions, b *genericModelTestBackend) {
			b.rows = func() db.Rows {
				return &genericViewTestRows{values: [][]any{{"private", int64(9)}, {"private", int64(9)}}}
			}
		}, 503},
		{"terminal", func(_ *DateDetailViewOptions, b *genericModelTestBackend) {
			b.rows = func() db.Rows {
				return &genericViewTestRows{values: [][]any{{"private", int64(9)}}, afterClose: errors.New("private")}
			}
		}, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, backend := genericDateOptions(t, models.DateTime)
			test.mutate(&o, backend)
			out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil))
			if out.Code != test.status || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
	for _, phase := range []string{"date", "clock", "key", "template"} {
		t.Run("cancel "+phase, func(t *testing.T) {
			o, backend := genericDateOptions(t, models.DateTime)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch phase {
			case "date":
				o.Date = func(*http.Request) (string, error) { cancel(); return "invalid", ErrInvalidLookup }
			case "clock":
				o.Clock = func() time.Time { cancel(); return time.Time{} }
			case "key":
				o.Key = func(*http.Request) (map[string]any, error) { cancel(); return map[string]any{"id": 9}, nil }
			case "template":
				o.Context = func(*http.Request) (templates.Context, error) { cancel(); return nil, nil }
			}
			out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil).WithContext(ctx))
			if out.Code != 503 || phase != "template" && backend.queries != 0 {
				t.Fatal(out.Code, backend.queries)
			}
		})
	}
}

func TestGenericDateDetailLocaleSnapshotAndFutureCalendar(t *testing.T) {
	for _, mode := range []string{"default", "inherited", "inherited without resolver"} {
		t.Run(mode, func(t *testing.T) {
			o, backend := genericDateOptions(t, models.DateTime)
			resolver := genericDateResolver(t, "Asia/Kolkata", "Asia/Kolkata", "America/New_York")
			o.Templates.LocaleResolver = resolver
			ctx := context.Background()
			zone, day := "Asia/Kolkata", "2026-01-02"
			if mode != "default" {
				var err error
				ctx, err = resolver.WithLocale(ctx, i18n.Preferences{TimeZone: "America/New_York"})
				if err != nil {
					t.Fatal(err)
				}
				zone, day = "America/New_York", "2026-01-01"
				if mode == "inherited without resolver" {
					o.Templates.LocaleResolver = nil
				}
			}
			o.Date = func(r *http.Request) (string, error) {
				l, _ := i18n.FromContext(r.Context())
				if l.TimeZone() != zone {
					t.Fatal("decoder locale", l.TimeZone())
				}
				return day, nil
			}
			o.Clock = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
			o.Templates.Processors = []templates.Processor{func(ctx context.Context) (templates.Context, error) {
				l, _ := i18n.FromContext(ctx)
				return templates.Context{"zone": l.TimeZone()}, nil
			}}
			o.Templates.Loaders = []templates.Loader{templates.MapLoader{"page.html": `{{ zone }}|{{ object.title }}`}}
			o.TemplateName = "page.html"
			h, err := NewDateDetailView(o)
			if err != nil {
				t.Fatal(err)
			}
			*resolver = *genericDateResolver(t, "UTC")
			o.Date = func(*http.Request) (string, error) { return "invalid", nil }
			o.AllowFuture = true
			o.Clock = nil
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest("GET", "/date", nil).WithContext(ctx))
			if out.Code != 200 || out.Body.String() != zone+"|&lt;one&gt;" || backend.queries != 1 {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
	for _, phase := range []string{"object", "field"} {
		o, _ := genericDateOptions(t, models.Date)
		allow := true
		o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
			if phase == "object" && r.Object != nil && !allow {
				return auth.ErrPermissionDenied
			}
			return nil
		})
		o.AllowField = func(context.Context, auth.Principal, models.Record, string) (bool, error) {
			return phase != "field" || allow, nil
		}
		o.Context = func(*http.Request) (templates.Context, error) { allow = false; return nil, nil }
		if out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil)); out.Code != 404 || strings.Contains(out.Body.String(), "one") {
			t.Fatal("final date grant", phase, out.Code, out.Body.String())
		}
	}
}

func TestGenericDateDetailRejectsForeignLocaleBeforeLookup(t *testing.T) {
	o, backend := genericDateOptions(t, models.Date)
	o.Templates.LocaleResolver = genericDateResolver(t, "UTC")
	foreign := genericDateResolver(t, "Europe/Paris")
	ctx, err := foreign.WithLocale(context.Background(), i18n.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	o.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
		t.Fatal("foreign locale reached scope")
		return db.Predicate{}, nil
	}
	o.Date = func(*http.Request) (string, error) { t.Fatal("foreign locale reached decoder"); return "", nil }
	if out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil).WithContext(ctx)); out.Code != 503 || backend.queries != 0 {
		t.Fatal(out.Code, backend.queries)
	}
}

type genericDateValueContext struct {
	context.Context
	afterValue func()
}

func (c *genericDateValueContext) Value(key any) any {
	if callback := c.afterValue; callback != nil {
		c.afterValue = nil
		callback()
	}
	return c.Context.Value(key)
}

func TestGenericDateDetailLocaleReadCancellationStopsKey(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &genericDateValueContext{Context: base}
	o, backend := genericDateOptions(t, models.Date)
	o.AllowFuture = true
	o.Date = func(*http.Request) (string, error) {
		ctx.afterValue = cancel
		return "2026-01-02", nil
	}
	keys := 0
	o.Key = func(*http.Request) (map[string]any, error) { keys++; return map[string]any{"id": 9}, nil }
	out := genericDateServe(t, o, httptest.NewRequest("GET", "/date", nil).WithContext(ctx))
	if out.Code != 503 || keys != 0 || backend.queries != 0 {
		t.Fatal("canceled locale read reached identity callback", out.Code, keys, backend.queries)
	}
}

func FuzzGenericCalendarDate(f *testing.F) {
	for _, value := range []string{"2024-02-29", "2025-02-29", "0000-01-01", "9999-12-31", "2026-1-02", "", "２０２６-01-02"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		date, ok := genericCalendarDate(value)
		if ok && (len(value) != 10 || date.Year() < 1 || date.Year() > 9999 || date.Format("2006-01-02") != value) {
			t.Fatal("invalid calendar accepted")
		}
	})
}
