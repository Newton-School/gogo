package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type genericUpdateBackend struct {
	*genericModelTestBackend
	stored, pending                                       map[string]any
	names                                                 []string
	begins, updates, commits, rollbacks, locks            int
	queryErr, closeErr, updateErr, commitErr, rollbackErr error
	commitThenError                                       bool
	zeroUpdate                                            bool
	commitHook                                            func()
	readHook                                              func(bool)
}

func (b *genericUpdateBackend) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true, "row_locks": true}
}
func (b *genericUpdateBackend) Query(_ context.Context, sql string, args ...any) (db.Rows, error) {
	return b.read(sql, args, false)
}
func (b *genericUpdateBackend) read(sql string, args []any, tx bool) (db.Rows, error) {
	b.queries++
	if b.readHook != nil {
		b.readHook(tx)
	}
	if strings.Contains(sql, "FOR UPDATE") {
		if !tx {
			return nil, errors.New("unexpected autocommit lock")
		}
		b.locks++
	}
	row := b.stored
	if tx {
		row = b.pending
	}
	var data []map[string]any
	if row != nil && len(args) >= 2 && reflect.DeepEqual(args[0], row["tenant"]) && reflect.DeepEqual(args[1], row["id"]) {
		data = []map[string]any{row}
	}
	return &genericModelRowsReader{names: b.names, data: data, closeErr: b.closeErr}, b.queryErr
}
func (b *genericUpdateBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	b.begins++
	b.pending = genericUpdateCopy(b.stored)
	return &genericUpdateTx{backend: b}, nil
}

type genericUpdateTx struct {
	db.Transaction
	backend *genericUpdateBackend
}

func (tx *genericUpdateTx) Query(_ context.Context, sql string, args ...any) (db.Rows, error) {
	b := tx.backend
	if strings.HasPrefix(sql, "UPDATE ") {
		b.updates++
		if b.updateErr != nil {
			return nil, b.updateErr
		}
		if b.zeroUpdate {
			return &genericModelRowsReader{names: b.names}, nil
		}
		if b.pending == nil || len(args) != len(b.names) || !reflect.DeepEqual(args[len(args)-1], b.pending["id"]) {
			return nil, fmt.Errorf("invalid update fixture statement: %s", sql)
		}
		for i, name := range b.names[1:] {
			b.pending[name] = args[i]
		}
		return &genericModelRowsReader{names: b.names, data: []map[string]any{b.pending}}, nil
	}
	return b.read(sql, args, true)
}
func (tx *genericUpdateTx) Commit() error {
	b := tx.backend
	b.commits++
	if b.commitErr != nil && !b.commitThenError {
		return b.commitErr
	}
	b.stored = genericUpdateCopy(b.pending)
	if b.commitHook != nil {
		b.commitHook()
	}
	return b.commitErr
}
func (tx *genericUpdateTx) Rollback() error {
	tx.backend.rollbacks++
	tx.backend.pending = nil
	return tx.backend.rollbackErr
}
func genericUpdateCopy(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for name, value := range in {
		out[name], _ = cloneGenericCreateMetadata(value)
	}
	return out
}

func genericUpdateOptions(t *testing.T) (UpdateViewOptions, *genericUpdateBackend) {
	t.Helper()
	create, _ := genericCreateOptions(t)
	backend := &genericUpdateBackend{
		genericModelTestBackend: &genericModelTestBackend{}, names: []string{"id", "title", "tenant"},
		stored: map[string]any{"id": int64(1), "title": "Current <title>", "tenant": "one"},
	}
	return UpdateViewOptions{
		TemplateViewOptions: create.TemplateViewOptions,
		Store:               orm.New(backend, create.Store.Registry), Model: create.Model,
		Scope: create.Scope, Factory: create.Factory, Fields: create.Fields, ReadonlyFields: create.ReadonlyFields,
		Key:           func(*http.Request) (map[string]any, error) { return map[string]any{"id": int64(1)}, nil },
		ValidateWrite: create.ValidateWrite, SuccessURL: create.SuccessURL,
		Policy: auth.PolicyFunc(func(_ context.Context, p auth.Principal, action string, resource auth.Resource) error {
			if action != "change" {
				return auth.ErrPermissionDenied
			}
			if resource.Object != nil {
				tenant, err := resource.Object.(models.Record).Get("tenant")
				if err != nil || tenant != p.ID {
					return auth.ErrPermissionDenied
				}
			}
			return nil
		}),
	}, backend
}

func genericUpdatePost(t *testing.T, h http.Handler, data url.Values) *httptest.ResponseRecorder {
	t.Helper()
	cookie, token := genericCreateTokens(t, h)
	data.Set("csrfmiddlewaretoken", token)
	req := genericCreateRequest("POST", data.Encode())
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}

func TestGenericUpdateSafeMethodsAndSuccessfulForceUpdate(t *testing.T) {
	options, backend := genericUpdateOptions(t)
	writes := 0
	options.ValidateWrite = func(_ context.Context, _ auth.Principal, record models.Record) error {
		writes++
		if !record.State().Persisted || record.State().Database != backend.Alias() {
			t.Fatal("proposed existing record state")
		}
		return nil
	}
	h, err := NewUpdateView(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericCreateRequest(method, ""))
		if out.Code != 200 || backend.begins != 0 || backend.updates != 0 || writes != 0 || backend.locks != 0 {
			t.Fatal("safe method persisted", method, out.Code)
		}
		if method == "GET" && (!strings.Contains(out.Body.String(), "Current &lt;title&gt;") || strings.Contains(out.Body.String(), `name="tenant"`) || strings.Contains(out.Body.String(), `name="id"`)) {
			t.Fatal("current form leaked fields or unsafe markup", out.Body.String())
		}
		if method == "HEAD" && out.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	out := genericUpdatePost(t, h, url.Values{"title": {"Changed"}})
	if out.Code != 303 || out.Header().Get("Location") != "/articles/1/" || backend.updates != 1 || backend.commits != 1 || backend.rollbacks != 0 || backend.locks < 4 || backend.stored["title"] != "Changed" || backend.stored["tenant"] != "one" || writes != 3 {
		t.Fatal("update failed", out.Code, out.Body.String(), backend, writes)
	}
}

func TestGenericUpdateMissingDenialAndTerminalReadFailure(t *testing.T) {
	for _, mode := range []string{"missing", "scope", "object denial", "model denial", "key", "key error", "close", "query"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			want := 404
			switch mode {
			case "missing":
				b.stored = nil
			case "scope":
				b.stored["tenant"] = "other"
			case "object denial":
				o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, _ string, r auth.Resource) error {
					if r.Object != nil {
						return auth.ErrPermissionDenied
					}
					return nil
				})
			case "model denial":
				o.Authorize = func(*http.Request) error { return auth.ErrPermissionDenied }
				want = 403
			case "key":
				o.Key = func(*http.Request) (map[string]any, error) { return nil, ErrInvalidLookup }
			case "key error":
				o.Key = func(*http.Request) (map[string]any, error) { return nil, errors.New("private key failure") }
				want = 503
			case "close":
				b.closeErr = errors.New("private close")
				want = 503
			case "query":
				b.queryErr = errors.New("private query")
				want = 503
			}
			h, err := NewUpdateView(o)
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, genericCreateRequest("GET", ""))
			if out.Code != want || b.begins != 0 || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
}

func TestGenericUpdateKeyCancellationPrecedesInvalidLookup(t *testing.T) {
	for _, mode := range []string{"shape", "value", "explicit invalid"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o.Key = func(*http.Request) (map[string]any, error) {
				cancel()
				if mode == "explicit invalid" {
					return nil, ErrInvalidLookup
				}
				if mode == "value" {
					return map[string]any{"id": "invalid"}, nil
				}
				return nil, nil
			}
			h, err := NewUpdateView(o)
			if err != nil {
				t.Fatal(err)
			}
			req := genericCreateRequest("GET", "").WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != 503 || b.queries != 0 || b.begins != 0 {
				t.Fatal("canceled key became object absence", out.Code, b.queries, b.begins)
			}
		})
	}
}

func TestGenericUpdateInputAndExactInvalidRollback(t *testing.T) {
	for _, mode := range []string{"invalid", "invalid cleanup", "readonly", "identity", "duplicate", "csrf", "media", "method", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			h, err := NewUpdateView(o)
			if err != nil {
				t.Fatal(err)
			}
			cookie, token := genericCreateTokens(t, h)
			data := url.Values{"title": {"Updated"}, "csrfmiddlewaretoken": {token}}
			method, want := "POST", 400
			switch mode {
			case "invalid":
				data.Set("title", "")
				want = 422
			case "invalid cleanup":
				data.Set("title", "")
				b.rollbackErr = errors.New("private rollback")
				want = 503
			case "readonly":
				data.Set("tenant", "other")
			case "identity":
				data.Set("id", "2")
			case "duplicate":
				data["title"] = []string{"one", "two"}
			case "csrf":
				data.Set("csrfmiddlewaretoken", "invalid")
				want = 403
			case "media":
				want = 415
			case "method":
				method, want = "DELETE", 405
			case "oversize":
				data.Set("title", strings.Repeat("x", (1<<20)+1))
				want = 413
			}
			req := genericCreateRequest(method, data.Encode())
			req.AddCookie(cookie)
			if mode == "media" {
				req.Header.Set("Content-Type", "application/json")
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != want || b.updates != 0 || b.commits != 0 || b.stored["title"] != "Current <title>" {
				t.Fatal(out.Code, out.Body.String(), b)
			}
			if mode == "invalid" || mode == "invalid cleanup" {
				if b.begins != 1 || b.rollbacks != 1 {
					t.Fatal("invalid form not rolled back", b)
				}
			} else if b.begins != 0 {
				t.Fatal("rejected input entered transaction")
			}
		})
	}
}

func TestGenericUpdateIdentityAndSameTransactionDrift(t *testing.T) {
	for _, mode := range []string{"prepare identity", "before drift", "after drift", "success drift", "final grant drift", "external location", "proposed denial", "zero update"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			want := 503
			switch mode {
			case "prepare identity":
				o.Prepare = func(_ context.Context, r models.Record) error { return r.Set("id", int64(2)) }
			case "before drift":
				o.Store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { b.pending["title"] = "nested write"; return nil }}
				want = 403
			case "after drift":
				o.Store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { b.pending["title"] = "nested write"; return nil }}
				want = 403
			case "success drift":
				o.SuccessURL = func(context.Context, map[string]any) (string, error) {
					b.pending["title"] = "nested write"
					return "/done/", nil
				}
				want = 403
			case "final grant drift":
				calls := 0
				o.ValidateWrite = func(context.Context, auth.Principal, models.Record) error {
					calls++
					if calls == 3 {
						b.pending["title"] = "nested write"
					}
					return nil
				}
				want = 403
			case "external location":
				o.SuccessURL = func(context.Context, map[string]any) (string, error) { return "https://other.test/", nil }
			case "proposed denial":
				o.ValidateWrite = func(context.Context, auth.Principal, models.Record) error { return auth.ErrPermissionDenied }
				want = 403
			case "zero update":
				b.zeroUpdate = true
				want = 409
			}
			h, err := NewUpdateView(o)
			if err != nil {
				t.Fatal(err)
			}
			out := genericUpdatePost(t, h, url.Values{"title": {"Changed"}})
			if out.Code != want || b.commits != 0 || b.rollbacks != 1 || b.stored["title"] != "Current <title>" {
				t.Fatal(out.Code, out.Body.String(), b)
			}
		})
	}
}

func TestGenericUpdateCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"known constraint", "joined constraint", "unknown", "actual unknown", "late cancel", "postcommit", "forged committed"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			want, persists := 503, false
			switch mode {
			case "known constraint":
				b.commitErr = &db.Error{Code: db.UniqueViolation}
				want = 422
			case "joined constraint":
				b.commitErr = errors.Join(&db.Error{Code: db.UniqueViolation}, errors.New("private cleanup"))
			case "unknown":
				b.commitErr = &db.Error{Code: db.UnknownCommit}
			case "actual unknown":
				b.commitErr = &db.Error{Code: db.UnknownCommit}
				b.commitThenError, persists = true, true
			case "late cancel":
				want, persists = 303, true
			case "postcommit":
				persists = true
				o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error {
					return db.OnCommit(ctx, b.Alias(), func(context.Context) error { return errors.New("private callback") }, false)
				}}
			case "forged committed":
				o.Prepare = func(context.Context, models.Record) error {
					return &db.CommittedCallbackError{Errors: []error{errors.New("forged")}}
				}
			}
			h, err := NewUpdateView(o)
			if err != nil {
				t.Fatal(err)
			}
			cookie, token := genericCreateTokens(t, h)
			req := genericCreateRequest("POST", url.Values{"title": {"Changed"}, "csrfmiddlewaretoken": {token}}.Encode())
			req.AddCookie(cookie)
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			if mode == "late cancel" {
				b.commitHook = cancel
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != want || (b.stored["title"] == "Changed") != persists || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String(), b)
			}
			if mode == "postcommit" && !strings.Contains(out.Body.String(), "committed") {
				t.Fatal("known commit hidden")
			}
			if mode == "actual unknown" && !strings.Contains(out.Body.String(), "unknown") {
				t.Fatal("uncertainty hidden")
			}
		})
	}
}

func TestGenericUpdateJSONControlPreservesWireValues(t *testing.T) {
	for _, raw := range []string{`"scalar"`, `"null"`, `900719925474099312345`, `null`, `{"a":[null,1,"<x>"]}`, `[]`, ``} {
		t.Run(raw, func(t *testing.T) {
			base := forms.NewField("data", forms.JSON)
			base.Required = false
			field := genericUpdateJSONField(base)
			form, err := forms.New([]forms.Field{field}, forms.WithData(url.Values{"data": {raw}}))
			if err != nil || !form.IsValid() {
				t.Fatal(err, form.Errors())
			}
			value := form.CleanedData()["data"]
			if raw == "" {
				if value != nil {
					t.Fatal("blank was not SQL NULL", value)
				}
				return
			}
			if raw == "null" && value != models.JSONNull {
				t.Fatal("JSON null lost")
			}
			encoded, err := json.Marshal(value)
			var source, actual any
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			_ = decoder.Decode(&source)
			decoder = json.NewDecoder(strings.NewReader(string(encoded)))
			decoder.UseNumber()
			_ = decoder.Decode(&actual)
			if err != nil || !reflect.DeepEqual(source, actual) {
				t.Fatal(raw, string(encoded), err)
			}
		})
	}
	base := forms.NewField("data", forms.JSON)
	for raw, want := range map[string]bool{"": false, "null": true, `""`: false, `[]`: false, `{}`: true, `invalid`: false} {
		form, err := forms.New([]forms.Field{genericUpdateJSONField(base)}, forms.WithData(url.Values{"data": {raw}}))
		if err != nil || form.IsValid() != want {
			t.Fatal("required JSON distinction", raw, err, form.Errors())
		}
	}
	called := 0
	base.Required = false
	base.Validators = []forms.Validator{func(_ context.Context, value any) error {
		called++
		if value == models.JSONNull {
			return forms.Error{Code: "null_denied", Message: "Null is not allowed."}
		}
		return nil
	}}
	form, _ := forms.New([]forms.Field{genericUpdateJSONField(base)}, forms.WithData(url.Values{"data": {"null"}}))
	if form.IsValid() || called != 1 {
		t.Fatal("JSON null skipped validation")
	}
}

type genericUpdateNoLocks struct{ db.Backend }

func (genericUpdateNoLocks) Capabilities() db.Capabilities {
	return db.Capabilities{"transactions": true}
}

func TestGenericUpdateConfigurationAndRegistrationSnapshots(t *testing.T) {
	for _, mode := range []string{"key", "locks", "scope", "write policy", "readonly only"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateOptions(t)
			switch mode {
			case "key":
				o.Key = nil
			case "locks":
				o.Store.Backend = genericUpdateNoLocks{Backend: b}
			case "scope":
				o.Scope = nil
			case "write policy":
				o.ValidateWrite = nil
			case "readonly only":
				o.ReadonlyFields = []string{"title", "tenant"}
			}
			if h, err := NewUpdateView(o); h != nil || err != ErrGenericConfiguration || b.begins != 0 || b.queries != 0 {
				t.Fatal("invalid update configuration", h, err)
			}
		})
	}
	o, b := genericUpdateOptions(t)
	before := 0
	o.Store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { before++; return nil }}
	o.Authorize = func(r *http.Request) error {
		r.Method = "DELETE"
		r.URL.Path = "/other/"
		r.PostForm = url.Values{"tenant": {"other"}}
		return nil
	}
	h, err := NewUpdateView(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Fields[0] = "tenant"
	o.Store.BeforeSave[0] = func(context.Context, orm.SaveEvent) error { t.Fatal("changed registration hook ran"); return nil }
	o.Key = func(*http.Request) (map[string]any, error) { return nil, ErrInvalidLookup }
	o.Factory = func() models.Model { panic("changed factory") }
	out := genericUpdatePost(t, h, url.Values{"title": {"Saved"}})
	if out.Code != 303 || before != 1 || b.stored["title"] != "Saved" || b.stored["tenant"] != "one" {
		t.Fatal("registration/request retarget", out.Code, out.Body.String())
	}
}

type genericUpdateCleanModel struct {
	models.Base
	ID            int64
	Title, Tenant string
	schema        models.Schema
	clean         func(context.Context) error
}

func (m *genericUpdateCleanModel) Schema() models.Schema           { return m.schema.Clone() }
func (m *genericUpdateCleanModel) Clean(ctx context.Context) error { return m.clean(ctx) }

func TestGenericUpdateInvalidModelCleanNeverCommitsCallbacks(t *testing.T) {
	o, b := genericUpdateOptions(t)
	schema, _ := o.Store.Registry.Get(o.Model)
	for i, name := range []string{"ID", "Title", "Tenant"} {
		schema.Fields[i].StructField = name
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	o.Store = orm.New(b, registry)
	callbacks, cleans := 0, 0
	o.Factory = func() models.Model {
		return &genericUpdateCleanModel{schema: schema, clean: func(ctx context.Context) error {
			cleans++
			if err := db.OnCommit(ctx, b.Alias(), func(context.Context) error { callbacks++; return nil }, false); err != nil {
				return err
			}
			b.pending["title"] = "invalid hook effect"
			return models.Invalid("invalid", "Model rejected the submitted value.")
		}}
	}
	h, err := NewUpdateView(o)
	if err != nil {
		t.Fatal(err)
	}
	out := genericUpdatePost(t, h, url.Values{"title": {"Changed"}})
	if out.Code != 422 || cleans != 1 || callbacks != 0 || b.updates != 0 || b.commits != 0 || b.rollbacks != 1 || b.stored["title"] != "Current <title>" {
		t.Fatal("invalid hook effects committed", out.Code, out.Body.String(), cleans, callbacks, b)
	}
}

func TestGenericUpdateDeliveredRecordsDoNotRetargetLaterGrants(t *testing.T) {
	o, b := genericUpdateOptions(t)
	schema, _ := o.Store.Registry.Get(o.Model)
	schema.Fields[1].Default = map[string]any{"marker": "original"}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	o.Store = orm.New(b, registry)
	o.Factory = func() models.Model {
		record, _ := models.NewRecord(schema)
		_ = record.Set("title", "Factory replacement")
		return record
	}
	check := func(record models.Record) {
		metadata, _ := record.Schema().Field("title")
		if metadata.Default.(map[string]any)["marker"] != "original" {
			t.Fatal("callback metadata alias")
		}
		metadata.Default.(map[string]any)["marker"] = "changed"
		id, _ := record.Get("id")
		tenant, _ := record.Get("tenant")
		if id != int64(1) || tenant != "one" {
			t.Fatal("callback row alias")
		}
		_ = record.Set("id", int64(8))
		_ = record.Set("tenant", "other")
		record.State().Database = "other"
	}
	o.Scope = func(_ context.Context, _ auth.Principal, schema models.Schema) (db.Predicate, error) {
		field, _ := schema.Field("title")
		if field.Default.(map[string]any)["marker"] != "original" {
			t.Fatal("scope schema alias")
		}
		field.Default.(map[string]any)["marker"] = "scope changed"
		return orm.Q("tenant", "one"), nil
	}
	o.ValidateWrite = func(_ context.Context, _ auth.Principal, record models.Record) error { check(record); return nil }
	o.Policy = auth.PolicyFunc(func(_ context.Context, _ auth.Principal, action string, resource auth.Resource) error {
		if action != "change" {
			t.Fatal("wrong action", action)
		}
		if resource.Object != nil {
			check(resource.Object.(models.Record))
			resource.ID.(map[string]any)["id"] = int64(9)
		}
		return nil
	})
	h, err := NewUpdateView(o)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out := genericUpdatePost(t, h, url.Values{"title": {"Stable"}})
		if out.Code != 303 || b.stored["tenant"] != "one" || b.stored["id"] != int64(1) {
			t.Fatal(out.Code, out.Body.String())
		}
	}
}

func TestGenericUpdateRejectsAmbientTransactionAndAbortsShortWrite(t *testing.T) {
	o, b := genericUpdateOptions(t)
	h, err := NewUpdateView(o)
	if err != nil {
		t.Fatal(err)
	}
	cookie, token := genericCreateTokens(t, h)
	err = db.Atomic(context.Background(), b, db.AtomicOptions{}, func(ctx context.Context) error {
		req := genericCreateRequest("POST", url.Values{"title": {"Changed"}, "csrfmiddlewaretoken": {token}}.Encode())
		req.AddCookie(cookie)
		req = req.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 503 || b.begins != 1 || b.updates != 0 {
			t.Fatal("borrowed outer transaction", out.Code, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := &genericCreateShortWriter{header: make(http.Header)}
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("short write did not abort")
			}
		}()
		h.ServeHTTP(writer, genericCreateRequest("GET", ""))
	}()
	if writer.writes != 1 {
		t.Fatal("short response was retried", writer.writes)
	}
}

func FuzzGenericUpdateURLFormBoundary(f *testing.F) {
	for _, body := range []string{
		"title=changed", "", "title=", "title=%3Cscript%3Ealert%281%29%3C%2Fscript%3E",
		"title=a&title=b", "title=changed&tenant=other", "title=changed&id=8",
		"title=changed&csrfmiddlewaretoken=forged", "title=%00", "title=%ff",
		"title=%xx", "title=changed;tenant=other", "title=" + strings.Repeat("a", 4096),
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		o, b := genericUpdateOptions(t)
		o.MaxBodyBytes = 4096
		h, err := NewUpdateView(o)
		if err != nil {
			t.Fatal(err)
		}
		cookie, token := genericCreateTokens(t, h)
		req := genericCreateRequest("POST", body)
		req.AddCookie(cookie)
		req.Header.Set("X-CSRFToken", token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Header().Get("Cache-Control") != "private, no-store" || b.stored["tenant"] != "one" || b.stored["id"] != int64(1) {
			t.Fatal("scope or response escaped", out.Code)
		}
		switch out.Code {
		case 303:
			if b.updates != 1 || b.commits != 1 || b.rollbacks != 0 || out.Header().Get("Location") != "/articles/1/" {
				t.Fatal("unconfirmed update success", out.Code)
			}
		case 400, 403, 413, 422:
			if b.updates != 0 || b.commits != 0 || b.stored["title"] != "Current <title>" || out.Header().Get("Location") != "" {
				t.Fatal("invalid input changed stored row", out.Code)
			}
			if out.Code == 422 && (b.begins != 1 || b.rollbacks != 1) {
				t.Fatal("invalid form escaped rollback")
			}
		default:
			t.Fatal("unexpected update response", out.Code, out.Body.String())
		}
		if strings.Contains(strings.ToLower(out.Body.String()), "<script>") {
			t.Fatal("submitted markup was not escaped")
		}
	})
}
