package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
)

type genericViewTestRows struct {
	db.Rows
	values               [][]any
	index, closes        int
	closeErr, afterClose error
}

func (r *genericViewTestRows) Next() bool { r.index++; return r.index <= len(r.values) }
func (r *genericViewTestRows) Scan(dest ...any) error {
	for i, value := range r.values[r.index-1] {
		*dest[i].(*any) = value
	}
	return nil
}
func (r *genericViewTestRows) Err() error {
	if r.closes > 0 {
		return r.afterClose
	}
	return nil
}
func (r *genericViewTestRows) Close() error { r.closes++; return r.closeErr }

func genericViewTestOptions(t *testing.T, source string, values [][]any) (ListViewOptions, *genericModelTestBackend, *[]*genericViewTestRows) {
	t.Helper()
	model, backend := genericModelTestOptions(t)
	model.AllowAnonymous = true
	readers := []*genericViewTestRows{}
	backend.failure = nil
	backend.rows = func() db.Rows {
		rows := &genericViewTestRows{values: values}
		readers = append(readers, rows)
		return rows
	}
	return ListViewOptions{TemplateViewOptions: genericTemplateOptions(source), ModelReadOptions: model,
		Pagination: pagination.Config{DefaultSize: 2, MaxSize: 2, MaxOffset: 4}}, backend, &readers
}

func TestGenericModelViewConstruction(t *testing.T) {
	for _, mutate := range []func(*ListViewOptions){
		func(o *ListViewOptions) { o.Authorize = nil },
		func(o *ListViewOptions) { o.TemplateName = "" },
		func(o *ListViewOptions) { o.Fields = nil },
		func(o *ListViewOptions) { o.Ordering = []string{"missing"} },
		func(o *ListViewOptions) { o.Ordering = []string{"title", "-title"} },
		func(o *ListViewOptions) { o.Ordering = []string{"owner__name"} },
		func(o *ListViewOptions) { o.Ordering = []string{"--id"} },
		func(o *ListViewOptions) { o.Ordering = make([]string, 65) },
		func(o *ListViewOptions) { o.Pagination.Mode = pagination.CursorMode },
		func(o *ListViewOptions) { o.Pagination.MaxSize = 201 },
		func(o *ListViewOptions) { o.Pagination.DefaultSize = 0; o.Pagination.MaxSize = 1 },
	} {
		options, backend, _ := genericViewTestOptions(t, "page", nil)
		mutate(&options)
		if handler, err := NewListView(options); handler != nil || err != ErrGenericConfiguration || backend.queries != 0 {
			t.Fatal("invalid list configuration accepted or queried", err, backend.queries)
		}
	}
	options, backend, _ := genericViewTestOptions(t, "page", nil)
	if handler, err := NewDetailView(DetailViewOptions{TemplateViewOptions: options.TemplateViewOptions, ModelReadOptions: options.ModelReadOptions}); handler != nil || err != ErrGenericConfiguration || backend.queries != 0 {
		t.Fatal("missing detail key accepted", handler, err)
	}
	schema := genericModelTestSchema()
	schema.Ordering = []string{"-title"}
	for _, test := range []struct{ given, want []string }{
		{nil, []string{"-title", "id"}}, {[]string{}, []string{"id"}}, {[]string{"-id", "title"}, []string{"-id", "title"}},
	} {
		actual, err := genericModelOrdering(schema, test.given)
		if err != nil || !reflect.DeepEqual(actual, test.want) {
			t.Fatal("ordering/tie-breaker changed", actual, err)
		}
	}
}

func TestGenericListProjectionPaginationHeadAndReservedContext(t *testing.T) {
	source := `{% for item in object_list %}{{ item.title }};{% endfor %}|{{ page.size }}|{{ page.offset }}|{% if page.has_next %}N{% endif %}{% if page.has_previous %}P{% endif %}|{{ page.next }}`
	options, backend, readers := genericViewTestOptions(t, source, [][]any{{"<one>", int64(1)}, {"two", int64(2)}, {"sentinel", int64(3)}})
	options.Ordering = []string{"-title"}
	options.ExtraContext = templates.Context{"object_list": []string{"wrong"}, "page": map[string]any{"size": 999}}
	options.Context = func(*http.Request) (templates.Context, error) {
		return templates.Context{"object_list": []string{"wrong"}, "page": map[string]any{"size": 999}}, nil
	}
	objects := 0
	options.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
		if resource.Object != nil {
			objects++
			id := resource.ID.(map[string]any)["id"]
			if id == int64(3) {
				t.Fatal("pagination sentinel was exposed to object projection")
			}
		}
		return nil
	})
	backend.query = func(statement string, args []any) {
		if !strings.Contains(statement, `ORDER BY "title" DESC, "id" ASC`) || !reflect.DeepEqual(args, []any{int64(7), 3, 0}) {
			t.Fatal("unbounded/non-deterministic list query", statement, args)
		}
	}
	handler, err := NewListView(options)
	if err != nil || backend.queries != 0 {
		t.Fatal(err, backend.queries)
	}
	options.Ordering[0], options.Pagination.MaxSize = "id", 1
	want := `&lt;one&gt;;two;|2|0|N|?page=2&amp;page_size=2`
	for _, method := range []string{"GET", "HEAD"} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(method, "/books/", nil))
		if out.Code != 200 || out.Header().Get("Content-Length") != strconv.Itoa(len(want)) || method == "GET" && out.Body.String() != want || method == "HEAD" && out.Body.Len() != 0 || out.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("list projection/navigation/HEAD changed", method, out.Code, out.Body.String(), out.Header())
		}
	}
	if objects != 8 || backend.queries != 2 {
		t.Fatal("missing object fences or unexpected count query", objects, backend.queries)
	}
	for _, rows := range *readers {
		if rows.closes != 1 {
			t.Fatal("list reader ownership", rows.closes)
		}
	}
}

func TestGenericListRejectsMalformedPaginationAfterScopeBeforeSQL(t *testing.T) {
	for _, mode := range []pagination.Mode{pagination.PageNumber, pagination.LimitOffset} {
		queries := []string{"unknown=1", "page=1&page=2", "page_size=1&page_size=1", "limit=1&limit=2", "offset=0&offset=1", "page=1&limit=1", "cursor=abc", "page=0", "page_size=3", "offset=5", "page=9", "limit=0", "page=1;page=2"}
		for _, raw := range queries {
			options, backend, _ := genericViewTestOptions(t, "page", nil)
			options.Pagination.Mode = mode
			grants, scopes, loads := 0, 0, 0
			options.Authorize = func(*http.Request) error { grants++; return nil }
			options.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				scopes++
				return db.Predicate{}, nil
			}
			options.Context = func(*http.Request) (templates.Context, error) { loads++; return nil, nil }
			handler, err := NewListView(options)
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest("GET", "/books/?"+raw, nil))
			if out.Code != 400 || grants != 2 || scopes != 1 || loads != 0 || backend.queries != 0 {
				t.Fatal("invalid pagination bypassed scope or reached reads", mode, raw, out.Code, grants, scopes, loads, backend.queries)
			}
		}
	}
}

func TestGenericListEmptyPagesScanCapAndOffsetMode(t *testing.T) {
	for _, test := range []struct {
		query, want string
		values      [][]any
		mode        pagination.Mode
	}{
		{"", "0|0||", nil, pagination.PageNumber},
		{"page=3", "2|4|P|", [][]any{{"a", int64(1)}, {"b", int64(2)}, {"c", int64(3)}}, pagination.PageNumber},
		{"limit=2&offset=2", "2|2|NP|?limit=2&amp;offset=4", [][]any{{"a", int64(1)}, {"b", int64(2)}, {"c", int64(3)}}, pagination.LimitOffset},
	} {
		options, _, _ := genericViewTestOptions(t, `{{ object_list|length }}|{{ page.offset }}|{% if page.has_next %}N{% endif %}{% if page.has_previous %}P{% endif %}|{{ page.next }}`, test.values)
		options.Pagination.Mode = test.mode
		handler, err := NewListView(options)
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", "/books/?"+test.query, nil))
		if out.Code != 200 || out.Body.String() != test.want {
			t.Fatal("empty page or usable navigation bound changed", test.query, out.Code, out.Body.String())
		}
	}
}

func TestGenericDetailScopeKeyFailureAndCardinality(t *testing.T) {
	for _, test := range []struct {
		name   string
		key    map[string]any
		keyErr error
		rows   [][]any
		status int
		reads  int
	}{
		{"found", map[string]any{"id": []byte("9")}, nil, [][]any{{"<nine>", int64(9)}}, 200, 1},
		{"missing", map[string]any{"id": int64(9)}, nil, nil, 404, 1},
		{"duplicate", map[string]any{"id": int64(9)}, nil, [][]any{{"a", int64(9)}, {"b", int64(9)}}, 503, 1},
		{"extra provider rows", map[string]any{"id": int64(9)}, nil, [][]any{{"a", int64(9)}, {"b", int64(9)}, {"c", int64(9)}}, 503, 1},
		{"invalid key sentinel", nil, ErrInvalidLookup, nil, 404, 0},
		{"mixed key failure", nil, errors.Join(ErrInvalidLookup, errors.New("provider failure")), nil, 503, 0},
		{"provider key failure", nil, errors.New("private provider failure"), nil, 503, 0},
		{"missing key", nil, nil, nil, 404, 0},
		{"extra key", map[string]any{"id": int64(9), "tenant": 7}, nil, nil, 404, 0},
		{"wrong key", map[string]any{"tenant": int64(9)}, nil, nil, 404, 0},
		{"invalid scalar", map[string]any{"id": "not-number"}, nil, nil, 404, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, backend, _ := genericViewTestOptions(t, `{{ object.title }}`, test.rows)
			events := []string{}
			base.Scope = func(context.Context, auth.Principal, models.Schema) (db.Predicate, error) {
				events = append(events, "scope")
				return orm.Q("tenant", int64(7)), nil
			}
			backend.query = func(_ string, args []any) {
				events = append(events, "query")
				if !reflect.DeepEqual(args, []any{int64(7), int64(9), 2}) {
					t.Fatal("detail identity was unbounded or unscoped", args)
				}
			}
			base.ExtraContext = templates.Context{"object": map[string]any{"title": "wrong"}}
			handler, err := NewDetailView(DetailViewOptions{TemplateViewOptions: base.TemplateViewOptions, ModelReadOptions: base.ModelReadOptions,
				Key: func(*http.Request) (map[string]any, error) {
					events = append(events, "key")
					return test.key, test.keyErr
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest("GET", "/books/9/", nil))
			if out.Code != test.status || backend.queries != test.reads || len(events) < 2 || events[0] != "scope" || events[1] != "key" {
				t.Fatal("detail error/lookup sequence changed", out.Code, out.Body.String(), backend.queries, events)
			}
			if test.status == 200 && out.Body.String() != "&lt;nine&gt;" || test.status != 200 && strings.Contains(out.Body.String(), "private") {
				t.Fatal("detail exposed wrong representation", out.Body.String())
			}
		})
	}
}

func TestGenericDetailRequiresEveryCompositeIdentityField(t *testing.T) {
	for _, key := range []map[string]any{
		{"tenant": "7", "id": "9"}, {"id": "9"}, {"tenant": "7"},
		{"tenant": "7", "id": "9", "extra": "wrong"}, {"tenant": nil, "id": "9"},
	} {
		schema := genericModelTestSchema()
		schema.PrimaryKey = []string{"tenant", "id"}
		model, backend := genericModelTestOptions(t, schema)
		model.AllowAnonymous = true
		backend.failure = nil
		backend.rows = func() db.Rows { return &genericViewTestRows{values: [][]any{{"nine", int64(7), int64(9)}}} }
		backend.query = func(_ string, args []any) {
			if !reflect.DeepEqual(args, []any{int64(7), int64(7), int64(9), 2}) {
				t.Fatal("composite key order or scope changed", args)
			}
		}
		model.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, resource auth.Resource) error {
			if resource.Object != nil && !reflect.DeepEqual(resource.ID, map[string]any{"tenant": int64(7), "id": int64(9)}) {
				t.Fatal("object grant lost composite identity", resource.ID)
			}
			return nil
		})
		handler, err := NewDetailView(DetailViewOptions{TemplateViewOptions: genericTemplateOptions(`{{ object.title }}`), ModelReadOptions: model,
			Key: func(*http.Request) (map[string]any, error) { return key, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", "/books/7/9/", nil))
		valid := len(key) == 2 && key["tenant"] == "7" && key["id"] == "9"
		if valid && (out.Code != 200 || out.Body.String() != "nine" || backend.queries != 1) || !valid && (out.Code != 404 || backend.queries != 0) {
			t.Fatal("composite detail accepted incomplete/extra identity", key, out.Code, out.Body.String(), backend.queries)
		}
	}
}

func TestGenericDetailDoesNotInheritListOrdering(t *testing.T) {
	schema := genericModelTestSchema()
	schema.Ordering = []string{"unsupported__ordering"}
	model, backend := genericModelTestOptions(t, schema)
	model.AllowAnonymous = true
	backend.failure = nil
	backend.rows = func() db.Rows { return &genericViewTestRows{values: [][]any{{"one", int64(1)}}} }
	backend.query = func(statement string, _ []any) {
		if strings.Contains(statement, "ORDER BY") || strings.Contains(statement, "unsupported") {
			t.Fatal("detail inherited list ordering", statement)
		}
	}
	template := genericTemplateOptions(`{{ object.title }}`)
	handler, err := NewDetailView(DetailViewOptions{TemplateViewOptions: template, ModelReadOptions: model,
		Key: func(*http.Request) (map[string]any, error) { return map[string]any{"id": 1}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest("GET", "/books/1/", nil))
	if out.Code != 200 || out.Body.String() != "one" || backend.queries != 1 {
		t.Fatal("exact detail was broken by list configuration", out.Code, out.Body.String(), backend.queries)
	}
	if handler, err := NewListView(ListViewOptions{TemplateViewOptions: template, ModelReadOptions: model}); handler != nil || err != ErrGenericConfiguration {
		t.Fatal("list did not reject unsupported ordering", handler, err)
	}
}

func TestGenericModelViewsRejectCompletionErrorsAndSkipOptionsReads(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, mode := range []string{"options", "post", "query error", "close error", "post close error", "overflow"} {
			base, backend, readers := genericViewTestOptions(t, "private page", [][]any{{"one", int64(1)}})
			base.AllowOptions = true
			base.Pagination.DefaultSize = 1
			if mode == "query error" {
				backend.failure = errors.New("private provider failure")
			}
			original := backend.rows
			backend.rows = func() db.Rows {
				row := original().(*genericViewTestRows)
				switch mode {
				case "close error":
					row.closeErr = errors.New("private close failure")
				case "post close error":
					row.afterClose = errors.New("private terminal failure")
				case "overflow":
					row.values = [][]any{{"one", int64(1)}, {"two", int64(2)}, {"three", int64(3)}}
				}
				return row
			}
			var handler http.Handler
			var err error
			if detail {
				handler, err = NewDetailView(DetailViewOptions{TemplateViewOptions: base.TemplateViewOptions, ModelReadOptions: base.ModelReadOptions, Key: func(*http.Request) (map[string]any, error) { return map[string]any{"id": 1}, nil }})
			} else {
				handler, err = NewListView(base)
			}
			if err != nil {
				t.Fatal(err)
			}
			method, want, queries := "GET", 503, 1
			if mode == "options" {
				method, want, queries = "OPTIONS", 200, 0
			} else if mode == "post" {
				method, want, queries = "POST", 405, 0
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest(method, "/books/", nil))
			if out.Code != want || backend.queries != queries || strings.Contains(out.Body.String(), "private") {
				t.Fatal("completion or method boundary failed", detail, mode, out.Code, out.Body.String(), backend.queries)
			}
			for _, rows := range *readers {
				if rows.closes != 1 {
					t.Fatal("reader not closed exactly once", detail, mode, rows.closes)
				}
			}
		}
	}
}
