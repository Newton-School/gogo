package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/templates"
)

type genericCreateBackend struct {
	*genericModelTestBackend
	begins, inserts, commits, rollbacks, reads  int
	stored                                      map[string]any
	tx                                          *genericCreateTransaction
	beginErr, insertErr, commitErr, rollbackErr error
	commitHook                                  func()
	readHook                                    func(int)
}
type genericCreateTransaction struct {
	db.Transaction
	backend *genericCreateBackend
	row     map[string]any
}

func (b *genericCreateBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	b.tx = &genericCreateTransaction{backend: b}
	return b.tx, nil
}
func (tx *genericCreateTransaction) Query(_ context.Context, statement string, args ...any) (db.Rows, error) {
	b := tx.backend
	if strings.HasPrefix(statement, "INSERT INTO") {
		b.inserts++
		if b.insertErr != nil {
			return nil, b.insertErr
		}
		if len(args) != 2 {
			return nil, fmt.Errorf("unexpected insert shape: %s", statement)
		}
		tx.row = map[string]any{"id": int64(1), "title": args[0], "tenant": args[1]}
		return &genericModelRowsReader{names: []string{"id", "title", "tenant"}, data: []map[string]any{tx.row}}, nil
	}
	b.reads++
	if b.readHook != nil {
		b.readHook(b.reads)
	}
	data := []map[string]any{}
	if tx.row != nil && len(args) >= 2 && reflect.DeepEqual(args[0], tx.row["tenant"]) && reflect.DeepEqual(args[1], tx.row["id"]) {
		data = append(data, tx.row)
	}
	return &genericModelRowsReader{names: []string{"id", "title", "tenant"}, data: data}, nil
}
func (tx *genericCreateTransaction) Commit() error {
	b := tx.backend
	b.commits++
	if b.commitErr != nil {
		return b.commitErr
	}
	b.stored = tx.row
	if b.commitHook != nil {
		b.commitHook()
	}
	return nil
}
func (tx *genericCreateTransaction) Rollback() error {
	tx.backend.rollbacks++
	tx.row = nil
	return tx.backend.rollbackErr
}

func genericCreateOptions(t *testing.T) (CreateViewOptions, *genericCreateBackend) {
	t.Helper()
	schema := models.Schema{AppLabel: "create", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title"), models.TextField("tenant")}}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	backend := &genericCreateBackend{genericModelTestBackend: &genericModelTestBackend{failure: errors.New("unexpected nontransaction query")}}
	return CreateViewOptions{
		TemplateViewOptions: TemplateViewOptions{
			ReadViewOptions: ReadViewOptions{Authorize: func(*http.Request) error { return nil }, AllowOptions: true},
			Templates:       templates.Config{Loaders: []templates.Loader{templates.MapLoader{"create.html": `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">{{ form.html }}<button type="submit">Create</button></form>|{{ object }}|{{ form.is_bound }}`}}}, TemplateName: "create.html",
		},
		Store: orm.New(backend, registry), Model: schema.Key(), Fields: []string{"title", "tenant"}, ReadonlyFields: []string{"tenant"},
		Policy: auth.PolicyFunc(func(_ context.Context, p auth.Principal, action string, r auth.Resource) error {
			if action != "add" {
				return auth.ErrPermissionDenied
			}
			if r.Object != nil {
				tenant, err := r.Object.(models.Record).Get("tenant")
				if err != nil || tenant != p.ID {
					return auth.ErrPermissionDenied
				}
			}
			return nil
		}),
		Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
			return orm.Q("tenant", p.ID), nil
		},
		Factory:       func() models.Model { record, _ := models.NewRecord(schema); return record },
		Prepare:       func(ctx context.Context, r models.Record) error { return r.Set("tenant", auth.FromContext(ctx).ID) },
		ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil },
		SuccessURL: func(_ context.Context, id map[string]any) (string, error) {
			return fmt.Sprintf("/articles/%v/", id["id"]), nil
		},
	}, backend
}

func genericCreateRequest(method, body string) *http.Request {
	r := httptest.NewRequest(method, "https://example.test/create/", strings.NewReader(body))
	if method != "POST" && body == "" {
		r.Body = http.NoBody
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://example.test")
	}
	return r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{ID: "one", Authenticated: true, Active: true}))
}
func genericCreateTokens(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	out := httptest.NewRecorder()
	h.ServeHTTP(out, genericCreateRequest("GET", ""))
	if out.Code != 200 {
		t.Fatal("unbound form", out.Code, out.Body.String())
	}
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if len(match) != 2 || len(out.Result().Cookies()) != 1 {
		t.Fatal("missing builtin CSRF token/cookie", out.Body.String(), out.Header())
	}
	return out.Result().Cookies()[0], match[1]
}
func genericCreatePost(t *testing.T, h http.Handler, values url.Values, alter func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	cookie, token := genericCreateTokens(t, h)
	values.Set("csrfmiddlewaretoken", token)
	r := genericCreateRequest("POST", values.Encode())
	r.AddCookie(cookie)
	if alter != nil {
		alter(r)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

func TestGenericCreateSafeFormAndForceInsert(t *testing.T) {
	o, b := genericCreateOptions(t)
	o.Initial = func(*http.Request) (map[string]any, error) {
		return map[string]any{"title": "<script>initial</script>"}, nil
	}
	h, err := NewCreateView(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericCreateRequest(method, ""))
		if out.Code != 200 || b.begins != 0 || b.queries != 0 || out.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(method, out.Code, out.Body.String(), b)
		}
		if method == "GET" {
			body := out.Body.String()
			if !strings.Contains(body, "&lt;script&gt;initial&lt;/script&gt;") || strings.Contains(body, `name="tenant"`) || !strings.Contains(body, `for="id_title"`) {
				t.Fatal(body)
			}
		}
		if method == "HEAD" && out.Body.Len() != 0 {
			t.Fatal("HEAD emitted form")
		}
	}
	out := genericCreatePost(t, h, url.Values{"title": {"<new>"}}, nil)
	if out.Code != 303 || out.Header().Get("Location") != "/articles/1/" || b.inserts != 1 || b.commits != 1 || b.rollbacks != 0 || b.reads != 2 || b.stored["title"] != "<new>" || b.stored["tenant"] != "one" {
		t.Fatal(out.Code, out.Body.String(), out.Header(), b)
	}
}

func TestGenericCreateRejectsInvalidInputBeforePersistence(t *testing.T) {
	for _, test := range []struct {
		name, body, content string
		status              int
	}{
		{"missing", "", "application/x-www-form-urlencoded", 422},
		{"readonly", "title=ok&tenant=two", "application/x-www-form-urlencoded", 400},
		{"identity", "title=ok&id=1", "application/x-www-form-urlencoded", 400},
		{"duplicate", "title=a&title=b", "application/x-www-form-urlencoded", 400},
		{"malformed", "title=%xx", "application/x-www-form-urlencoded", 400},
		{"json", `{"title":"ok"}`, "application/json", 415},
		{"multipart", "title=ok", "multipart/form-data; boundary=ignored", 415},
		{"parameter", "title=ok", "application/x-www-form-urlencoded; other=value", 415},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			o.Initial = func(*http.Request) (map[string]any, error) { return map[string]any{"title": "not a POST default"}, nil }
			h, err := NewCreateView(o)
			if err != nil {
				t.Fatal(err)
			}
			cookie, token := genericCreateTokens(t, h)
			r := genericCreateRequest("POST", test.body)
			r.AddCookie(cookie)
			r.Header.Set("X-CSRFToken", token)
			r.Header.Set("Content-Type", test.content)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != test.status || b.begins != 0 || b.queries != 0 || out.Header().Get("Location") != "" {
				t.Fatal(out.Code, out.Body.String(), b)
			}
			if test.status == 422 && !strings.Contains(out.Body.String(), `aria-invalid="true"`) {
				t.Fatal("invalid form inaccessible", out.Body.String())
			}
		})
	}
}

func TestGenericCreateCSRFIsMandatoryAndMetadataIsFrozen(t *testing.T) {
	for _, mode := range []string{"missing cookie", "missing token", "foreign origin", "fake middleware cache", "mutated method", "mutated body"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			var original *http.Request
			o.Authorize = func(r *http.Request) error {
				if original != nil && r.Method == "POST" {
					if mode == "mutated method" {
						original.Method = "GET"
						r.Method = "GET"
					}
					if mode == "mutated body" {
						original.Body = io.NopCloser(strings.NewReader("title=retargeted"))
						r.PostForm = url.Values{"title": {"retargeted"}}
					}
				}
				return nil
			}
			h, err := NewCreateView(o)
			if err != nil {
				t.Fatal(err)
			}
			cookie, token := genericCreateTokens(t, h)
			original = genericCreateRequest("POST", "title=original")
			original.AddCookie(cookie)
			original.Header.Set("X-CSRFToken", token)
			switch mode {
			case "missing cookie":
				original.Header.Del("Cookie")
			case "missing token":
				original.Header.Del("X-CSRFToken")
			case "foreign origin":
				original.Header.Set("Origin", "https://other.test")
			case "fake middleware cache":
				original.Header.Del("X-CSRFToken")
				original.PostForm = url.Values{"title": {"original"}, "csrfmiddlewaretoken": {token}}
				original.Form = original.PostForm
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, original)
			if strings.HasPrefix(mode, "mutated") {
				if out.Code != 303 || b.stored["title"] != "original" {
					t.Fatal(out.Code, out.Body.String(), b)
				}
			} else if out.Code != 403 || b.begins != 0 {
				t.Fatal(out.Code, b.begins)
			}
		})
	}
}

func TestGenericCreateCallbacksCannotRetargetPersistedData(t *testing.T) {
	for _, mode := range []string{"after save", "success URL", "final grant", "scope exclusion", "denied proposal", "external URL", "invalid URL", "final provider"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			originalGrant := o.ValidateWrite
			switch mode {
			case "after save":
				o.Store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { b.tx.row["tenant"] = "two"; return nil }}
			case "success URL":
				o.SuccessURL = func(context.Context, map[string]any) (string, error) {
					b.tx.row["title"] = "changed"
					return "/ok/", nil
				}
			case "final grant":
				o.ValidateWrite = func(ctx context.Context, p auth.Principal, r models.Record) error {
					if b.reads == 1 {
						b.tx.row["tenant"] = "two"
					}
					return originalGrant(ctx, p, r)
				}
			case "scope exclusion":
				o.Prepare = func(_ context.Context, r models.Record) error { return r.Set("tenant", "two") }
				o.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil })
			case "denied proposal":
				o.ValidateWrite = func(context.Context, auth.Principal, models.Record) error { return auth.ErrPermissionDenied }
			case "external URL":
				o.SuccessURL = func(context.Context, map[string]any) (string, error) { return "https://example.test/ok/", nil }
			case "invalid URL":
				o.SuccessURL = func(context.Context, map[string]any) (string, error) { return "/%00", nil }
			case "final provider":
				b.readHook = func(n int) {
					if n == 2 {
						panic("private provider failure")
					}
				}
			}
			h, err := NewCreateView(o)
			if err != nil {
				t.Fatal(err)
			}
			out := genericCreatePost(t, h, url.Values{"title": {"new"}}, nil)
			if out.Code != 403 && out.Code != 503 {
				t.Fatal(out.Code, out.Body.String())
			}
			if b.stored != nil || b.commits != 0 || b.rollbacks != 1 || out.Header().Get("Location") != "" || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String(), b)
			}
		})
	}
}

func TestGenericCreateCommitOutcomesAreNotConflated(t *testing.T) {
	for _, mode := range []string{"rejected", "unknown", "mixed rejection", "callback error", "callback panic", "late cancellation", "fake committed error"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "rejected":
				b.commitErr = &db.Error{Code: db.CheckViolation}
			case "unknown":
				b.commitErr = &db.Error{Code: db.UnknownCommit}
			case "mixed rejection":
				b.commitErr = errors.Join(&db.Error{Code: db.CheckViolation}, errors.New("private cleanup failure"))
			case "callback error", "callback panic":
				o.Store.AfterSave = []orm.SaveReceiver{func(c context.Context, _ orm.SaveEvent) error {
					return db.OnCommit(c, b.Alias(), func(context.Context) error {
						if mode == "callback panic" {
							panic("private panic")
						}
						return errors.New("private callback")
					}, false)
				}}
			case "late cancellation":
				b.commitHook = cancel
			case "fake committed error":
				o.Prepare = func(context.Context, models.Record) error {
					return &db.CommittedCallbackError{Errors: []error{errors.New("pretend")}}
				}
			}
			h, err := NewCreateView(o)
			if err != nil {
				t.Fatal(err)
			}
			out := genericCreatePost(t, h, url.Values{"title": {"new"}}, func(r *http.Request) {
				*r = *r.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
			})
			want := 503
			if mode == "rejected" {
				want = 422
			}
			if mode == "late cancellation" {
				want = 303
			}
			if out.Code != want {
				t.Fatal(mode, out.Code, out.Body.String(), b)
			}
			if strings.Contains(mode, "callback") {
				if b.stored == nil || !strings.Contains(out.Body.String(), "committed") || b.rollbacks != 0 {
					t.Fatal(mode, b, out.Body.String())
				}
			}
			if mode == "unknown" || mode == "mixed rejection" {
				if !strings.Contains(out.Body.String(), "unknown") {
					t.Fatal(out.Body.String())
				}
			}
			if mode == "fake committed error" && (b.stored != nil || strings.Contains(out.Body.String(), "committed")) {
				t.Fatal(out.Body.String(), b)
			}
			if want != 303 && out.Header().Get("Location") != "" {
				t.Fatal("error redirected", out.Header())
			}
		})
	}
}

func TestGenericCreateRequiredConfiguration(t *testing.T) {
	for name, change := range map[string]func(*CreateViewOptions){
		"factory": func(o *CreateViewOptions) { o.Factory = nil }, "policy": func(o *CreateViewOptions) { o.Policy = nil }, "scope": func(o *CreateViewOptions) { o.Scope = nil }, "proposed policy": func(o *CreateViewOptions) { o.ValidateWrite = nil }, "success": func(o *CreateViewOptions) { o.SuccessURL = nil }, "authorize": func(o *CreateViewOptions) { o.Authorize = nil }, "fields": func(o *CreateViewOptions) { o.Fields = nil }, "duplicate": func(o *CreateViewOptions) { o.Fields = []string{"title", "title"} }, "readonly": func(o *CreateViewOptions) { o.ReadonlyFields = []string{"unknown"} }, "all readonly": func(o *CreateViewOptions) { o.ReadonlyFields = []string{"title", "tenant"} }, "csrf exemption": func(o *CreateViewOptions) {
			o.CSRF = &security.CSRFConfig{Exempt: func(*http.Request) bool { return true }}
		}, "body limit": func(o *CreateViewOptions) { o.MaxBodyBytes = 11 << 20 },
		"invalid cookie": func(o *CreateViewOptions) { o.CSRF = &security.CSRFConfig{CookieName: "bad cookie"} },
		"origin budget": func(o *CreateViewOptions) {
			o.CSRF = &security.CSRFConfig{TrustedOrigins: []string{"https://" + strings.Repeat("x", 2048) + ".test"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			change(&o)
			h, err := NewCreateView(o)
			if h != nil || err != ErrGenericConfiguration || b.begins != 0 || b.queries != 0 {
				t.Fatal(h, err, b)
			}
		})
	}
}

type genericCreateMetadataTripwire struct{ calls *int }

func (v genericCreateMetadataTripwire) MarshalJSON() ([]byte, error) {
	*v.calls++
	return []byte("null"), nil
}
func (v genericCreateMetadataTripwire) String() string { *v.calls++; return "private metadata" }

func TestGenericCreateMetadataPreservesTypesAndRejectsMethodsBeforeFingerprint(t *testing.T) {
	raw := json.RawMessage(`{"value":9007199254740993}`)
	zone := time.FixedZone("fixture", 3600)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, zone)
	input := map[string]any{"raw": raw, "null": models.JSONNull, "number": json.Number("9007199254740993"), "time": stamp, "empty": []string{}, "nil": []string(nil)}
	copy, err := cloneGenericCreateMetadata(input)
	if err != nil {
		t.Fatal(err)
	}
	values := copy.(map[string]any)
	raw[2] = 'X'
	*zone = *time.FixedZone("changed", 7200)
	if string(values["raw"].(json.RawMessage)) != `{"value":9007199254740993}` || values["null"] != models.JSONNull || values["number"] != json.Number("9007199254740993") || values["time"].(time.Time).Format(time.RFC3339) != "2026-01-01T00:00:00+01:00" || values["empty"].([]string) == nil || values["nil"].([]string) != nil {
		t.Fatal(values)
	}
	for _, incoming := range []bool{false, true} {
		t.Run(fmt.Sprint(incoming), func(t *testing.T) {
			o, b := genericCreateOptions(t)
			calls := 0
			schema, _ := o.Store.Registry.Get(o.Model)
			schema.Fields[1].Default = genericCreateMetadataTripwire{calls: &calls}
			if incoming {
				o.Factory = func() models.Model { record, _ := models.NewRecord(schema); return record }
			} else {
				registry := &models.Registry{}
				if err := registry.Register(schema); err != nil {
					t.Fatal(err)
				}
				o.Store.Registry = registry
			}
			h, err := NewCreateView(o)
			if incoming {
				if err != nil {
					t.Fatal(err)
				}
				out := httptest.NewRecorder()
				h.ServeHTTP(out, genericCreateRequest("GET", ""))
				if out.Code != 503 {
					t.Fatal(out.Code, out.Body.String())
				}
			} else if h != nil || err != ErrGenericConfiguration {
				t.Fatal(h, err)
			}
			if calls != 0 || b.begins != 0 || b.queries != 0 {
				t.Fatal("metadata method invoked", calls, b)
			}
		})
	}
}

func TestGenericCreateCallbackSchemaMetadataIsDetached(t *testing.T) {
	o, _ := genericCreateOptions(t)
	schema, _ := o.Store.Registry.Get(o.Model)
	schema.Fields[1].Default = map[string]any{"nested": []string{"original"}}
	schema.Fields[1].Min = []int{1}
	schema.Fields[1].Max = map[string]int{"max": 9}
	schema.Fields[2].Choices = []models.Choice{{Value: map[string]string{"choice": "original"}, Label: "choice"}}
	schema.Fields[2].Default = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("original", 3600))
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	o.Store.Registry = registry
	check := func(s models.Schema) {
		if s.Fields[1].Default.(map[string]any)["nested"].([]string)[0] != "original" || s.Fields[1].Min.([]int)[0] != 1 || s.Fields[1].Max.(map[string]int)["max"] != 9 || s.Fields[2].Choices[0].Value.(map[string]string)["choice"] != "original" || s.Fields[2].Default.(time.Time).Location().String() != "original" {
			t.Fatal("callback changed later schema metadata", s)
		}
	}
	mutate := func(s models.Schema) {
		s.Fields[1].Default.(map[string]any)["nested"].([]string)[0] = "changed"
		s.Fields[1].Min.([]int)[0] = 2
		s.Fields[1].Max.(map[string]int)["max"] = 2
		s.Fields[2].Choices[0].Value.(map[string]string)["choice"] = "changed"
		*s.Fields[2].Default.(time.Time).Location() = *time.FixedZone("changed", 7200)
	}
	grants, scopes := 0, 0
	o.Scope = func(_ context.Context, _ auth.Principal, s models.Schema) (db.Predicate, error) {
		scopes++
		check(s)
		mutate(s)
		return orm.Q("tenant", "one"), nil
	}
	o.ValidateWrite = func(_ context.Context, _ auth.Principal, r models.Record) error {
		grants++
		s := r.Schema()
		check(s)
		mutate(s)
		return nil
	}
	o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
		if r.Object != nil {
			s := r.Object.(models.Record).Schema()
			check(s)
			mutate(s)
		}
		return nil
	})
	model, _, _, err := newGenericCreateModel(o)
	if err != nil {
		t.Fatal(err)
	}
	view := &genericCreate{options: o, model: model}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "one", Active: true, Authenticated: true})
	row := genericModelRow{values: map[string]any{"id": int64(1), "title": "new", "tenant": "one"}, database: model.store.Backend.Alias()}
	for i := 0; i < 2; i++ {
		if _, err := model.query(ctx); err != nil {
			t.Fatal(err)
		}
		if err := view.writeGrant(ctx, row, true); err != nil {
			t.Fatal(err)
		}
		check(model.schema)
		stored, _ := model.store.Registry.Get(o.Model)
		check(stored)
	}
	if grants != 2 || scopes != 2 {
		t.Fatal(grants, scopes)
	}
}

func TestGenericCreateSchemaAggregateBudgetAndUnsupportedFields(t *testing.T) {
	for _, mode := range []string{"field union", "constraint bytes", "custom codec", "relation", "generated", "file", "too many fields", "cycle", "csrf field collision"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			schema, _ := o.Store.Registry.Get(o.Model)
			switch mode {
			case "field union":
				schema.Fields[1].HelpText = strings.Repeat("a", 4<<20)
				schema.Fields[2].HelpText = strings.Repeat("b", 4<<20)
			case "constraint bytes":
				schema.Constraints = []models.Constraint{{Name: "large_check", Kind: "check", Expression: strings.Repeat("x", 8<<20)}}
			case "custom codec":
				schema.Fields[1].Codec = genericModelTestCodec{}
			case "relation":
				schema.Fields = append(schema.Fields, models.Field{Name: "related", Kind: models.ManyToMany, Relation: &models.Relation{Target: "other.Item"}})
			case "generated":
				schema.Fields[1].Kind = models.Generated
				schema.Fields[1].GeneratedExpression = "1"
			case "file":
				schema.Fields[1].Kind = models.File
			case "csrf field collision":
				schema.Fields = append(schema.Fields, models.TextField("csrfmiddlewaretoken"))
				o.Fields = append(o.Fields, "csrfmiddlewaretoken")
			case "too many fields":
				for len(schema.Fields) < 65 {
					schema.Fields = append(schema.Fields, models.TextField(fmt.Sprintf("field%d", len(schema.Fields))))
				}
			case "cycle":
				cycle := map[string]any{}
				cycle["self"] = cycle
				schema.Fields[1].Default = cycle
			}
			registry := &models.Registry{}
			if err := registry.Register(schema); err != nil { // Some malformed descriptors fail even earlier at registration.
				if mode == "relation" || mode == "generated" {
					return
				}
				t.Fatal(err)
			}
			o.Store.Registry = registry
			h, err := NewCreateView(o)
			if h != nil || err != ErrGenericConfiguration || b.begins != 0 || b.queries != 0 {
				t.Fatal(h, err, b)
			}
		})
	}
}

func TestGenericCreateDetachedPoliciesIdentityAndRegistration(t *testing.T) {
	o, b := genericCreateOptions(t)
	o.ValidateWrite = func(_ context.Context, _ auth.Principal, record models.Record) error {
		_ = record.Set("tenant", "changed detached copy")
		record.State().Persisted = false
		return nil
	}
	o.SuccessURL = func(_ context.Context, identity map[string]any) (string, error) {
		identity["id"] = int64(99)
		return "/done/", nil
	}
	h, err := NewCreateView(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Fields[0] = "tenant"
	o.ReadonlyFields[0] = "title"
	o.Store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { t.Fatal("late hook used"); return nil }}
	*o.Store = *orm.New(&genericModelTestBackend{failure: errors.New("replacement")}, &models.Registry{})
	out := genericCreatePost(t, h, url.Values{"title": {"new"}}, nil)
	if out.Code != 303 || out.Header().Get("Location") != "/done/" || b.stored["tenant"] != "one" || b.stored["id"] != int64(1) {
		t.Fatal(out.Code, out.Body.String(), b)
	}
}

func TestGenericCreateRefusesAmbientTransactionAndBoundsBody(t *testing.T) {
	o, b := genericCreateOptions(t)
	h, err := NewCreateView(o)
	if err != nil {
		t.Fatal(err)
	}
	cookie, token := genericCreateTokens(t, h)
	err = db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		r := genericCreateRequest("POST", "title=new")
		*r = *r.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
		r.AddCookie(cookie)
		r.Header.Set("X-CSRFToken", token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		if out.Code != 503 || b.begins != 1 || b.inserts != 0 {
			t.Fatal(out.Code, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"declared", "streamed"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateOptions(t)
			o.MaxBodyBytes = 128
			h, err := NewCreateView(o)
			if err != nil {
				t.Fatal(err)
			}
			cookie, token := genericCreateTokens(t, h)
			r := genericCreateRequest("POST", "title="+strings.Repeat("x", 129))
			r.AddCookie(cookie)
			r.Header.Set("X-CSRFToken", token)
			if mode == "streamed" {
				r.ContentLength = -1
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			if out.Code != 413 || b.begins != 0 {
				t.Fatal(out.Code, b)
			}
		})
	}
}

func TestGenericCreateDenialsCancellationAndTransferAbort(t *testing.T) {
	for _, failure := range []error{auth.ErrUnauthenticated, auth.ErrPermissionDenied, errors.New("private outage")} {
		o, b := genericCreateOptions(t)
		o.Authorize = func(*http.Request) error { return failure }
		o.Factory = func() models.Model { t.Fatal("denied factory invoked"); return nil }
		h, err := NewCreateView(o)
		if err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericCreateRequest("GET", ""))
		want := 503
		if failure == auth.ErrUnauthenticated {
			want = 401
		}
		if failure == auth.ErrPermissionDenied {
			want = 403
		}
		if out.Code != want || b.begins != 0 || strings.Contains(out.Body.String(), "private") {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	o, _ := genericCreateOptions(t)
	h, err := NewCreateView(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := httptest.NewRecorder()
	h.ServeHTTP(out, genericCreateRequest("GET", "").WithContext(ctx))
	if out.Code != 503 {
		t.Fatal(out.Code)
	}
	writer := &genericCreateShortWriter{header: make(http.Header)}
	func() {
		defer func() {
			if value := recover(); value != http.ErrAbortHandler {
				t.Fatal("transfer did not abort", value)
			}
		}()
		h.ServeHTTP(writer, genericCreateRequest("GET", ""))
	}()
	if writer.writes != 1 {
		t.Fatal("retried short response", writer.writes)
	}
}

type genericCreateShortWriter struct {
	header http.Header
	writes int
}

func (w *genericCreateShortWriter) Header() http.Header         { return w.header }
func (w *genericCreateShortWriter) WriteHeader(int)             {}
func (w *genericCreateShortWriter) Write(p []byte) (int, error) { w.writes++; return len(p) - 1, nil }

func TestGenericCreateFactoryInitialValuesAreBoundedBeforeFormatting(t *testing.T) {
	for _, mode := range []string{"opaque", "bytes", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			options, backend := genericCreateOptions(t)
			schema, _ := options.Store.Registry.Get(options.Model)
			calls := 0
			options.Factory = func() models.Model {
				record, _ := models.NewRecord(schema)
				var value any
				switch mode {
				case "opaque":
					value = genericCreateMetadataTripwire{calls: &calls}
				case "bytes":
					value = strings.Repeat("x", templateContextMaxBytes+1)
				case "cycle":
					cycle := map[string]any{}
					cycle["self"] = cycle
					value = cycle
				}
				_ = record.Set("title", value)
				return record
			}
			handler, err := NewCreateView(options)
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, genericCreateRequest("GET", ""))
			if out.Code != 503 || calls != 0 || backend.queries != 0 || backend.begins != 0 || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, calls, backend, out.Body.String())
			}
		})
	}
}
