package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type apiUpdatedItem struct {
	models.Base
	ID           int64
	Tenant, Name string
	Quantity     int64
	Notes        *string
	Metadata     map[string]any
	UpdatedAt    time.Time
}

func (*apiUpdatedItem) Schema() models.Schema {
	return models.Schema{AppLabel: "shop", Name: "UpdatedItem", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.CharField("tenant", models.ReadOnly, models.WithStructField("Tenant")),
		models.CharField("name", models.WithStructField("Name")),
		models.IntegerField("quantity", models.WithDefault(int64(9)), models.WithStructField("Quantity")),
		models.CharField("notes", models.Nullable, models.WithStructField("Notes")),
		models.JSONField("metadata", models.WithStructField("Metadata")),
		models.DateTimeField("updated_at", models.WithStructField("UpdatedAt"), func(f *models.Field) { f.AutoNow = true }),
	}}
}

func (row *apiUpdatedItem) Clean(context.Context) error {
	if row.Name == "model-denied" {
		return models.Invalid("model_denied", "This name is unavailable.")
	}
	if row.Name == "normalize-me" {
		row.Name = "NORMALIZED"
	}
	return nil
}

func TestPostgresAPIHTTPUpdatePresenceConditionalReceiptsAndRollback(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	schema := (&apiUpdatedItem{}).Schema()
	for _, definition := range append(api.IdempotencySchemas(), schema) {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, definition); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backend.Exec(ctx, `CREATE TABLE api_update_audit (object_id bigint NOT NULL, action text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	note := "keep"
	row := &apiUpdatedItem{Tenant: "one", Name: "initial", Quantity: 9, Notes: &note, Metadata: map[string]any{"large": json.Number("9007199254740993")}}
	hidden := &apiUpdatedItem{Tenant: "two", Name: "hidden", Quantity: 1, Metadata: map[string]any{}}
	for _, item := range []*apiUpdatedItem{row, hidden} {
		if err := store.Save(ctx, item, orm.SaveOptions{ForceInsert: true}); err != nil {
			t.Fatal(err)
		}
	}
	input, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name", "quantity", "notes", "metadata"}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name", "quantity", "notes", "metadata", "updated_at"}})
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Value
	mode.Store("")
	var audits atomic.Int64
	resource, err := api.NewResource(api.ResourceConfig{Store: store, Model: schema.Key(), Serializer: output, EntityTags: true, Policy: auth.ModelPolicy{}, Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
		return orm.Q("tenant", p.ID), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	permissions := []string{"shop.change_updateditem", "shop.view_updateditem"}
	actor, err := auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: permissions}, permissions)
	if err != nil {
		t.Fatal(err)
	}
	key := func(r *http.Request) (api.Values, error) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		return api.Values{"id": parts[len(parts)-1]}, nil
	}
	path := "https://example.test/items/" + strconv.FormatInt(row.ID, 10) + "/"
	writeRoot := func(ctx context.Context) error {
		_, err := db.ExecutorFor(ctx, backend).Exec(ctx, `UPDATE shop_updateditem SET name='unauthorized' WHERE id=$1`, row.ID)
		return err
	}
	store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if mode.Load() == "retarget" {
			return event.Record.Set("id", hidden.ID)
		}
		if mode.Load() == "before_sql" {
			return writeRoot(ctx)
		}
		return nil
	}}
	store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		switch mode.Load() {
		case "after_sql":
			return writeRoot(ctx)
		case "after_panic":
			panic("synthetic private save detail")
		case "foreign_commit":
			return &db.CommittedCallbackError{Errors: []error{errors.New("foreign commit")}}
		case "foreign_unknown":
			return &db.Error{Code: db.UnknownCommit, Cause: errors.New("foreign uncertainty")}
		case "postcommit":
			return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("synthetic postcommit detail") }, false)
		}
		return nil
	}}
	options := api.UpdateOptions{Input: input, Factory: func() models.Model { return &apiUpdatedItem{} }, RequireMatch: true,
		ValidateWrite: func(ctx context.Context, p auth.Principal, proposed models.Record) error {
			tenant, err := proposed.Get("tenant")
			if err != nil || tenant != p.ID {
				return auth.ErrPermissionDenied
			}
			if mode.Load() == "policy_sql" {
				return writeRoot(ctx)
			}
			return nil
		},
		Audit: func(ctx context.Context, event api.MutationEvent) error {
			audits.Add(1)
			if !db.InTransaction(ctx, backend.Alias()) || event.Action != "change" {
				return errors.New("invalid audit context")
			}
			if _, err := db.ExecutorFor(ctx, backend).Exec(ctx, `INSERT INTO api_update_audit VALUES ($1,$2)`, event.ObjectKey["id"].(json.Number).String(), event.Action); err != nil {
				return err
			}
			if mode.Load() == "audit_sql" {
				return writeRoot(ctx)
			}
			if mode.Load() == "audit_error" {
				return errors.New("synthetic private audit detail")
			}
			return nil
		},
		Idempotency: &api.MutationIdempotencyOptions{Version: "v1", Scope: func(_ context.Context, p auth.Principal) (string, error) { return p.ID, nil }, Redact: func(_ context.Context, _ auth.Principal, _ models.Record, body api.Values) (api.Values, error) {
			return body, nil
		}},
	}
	handler, err := resource.UpdateHandler(key, options)
	if err != nil {
		t.Fatal(err)
	}
	call := func(h http.Handler, p auth.Principal, method, target, body, operation, tag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(auth.WithPrincipal(ctx, p))
		r.Header.Set("Content-Type", "application/json")
		if operation != "" {
			r.Header.Set("Idempotency-Key", operation)
		}
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	detail := resource.DetailHandler(key)
	get := func() *httptest.ResponseRecorder { return call(detail, actor, "GET", path, "", "", "") }
	page := get()
	oldTag := page.Header().Get("ETag")
	if page.Code != 200 || oldTag == "" || strings.Contains(page.Body.String(), "tenant") {
		t.Fatal("detail validator unavailable", page.Code, page.Body.String())
	}
	w := call(handler, actor, "PATCH", path, `{"quantity":0,"notes":null}`, "patch-one", oldTag)
	if w.Code != 200 || w.Header().Get("X-Gogo-Mutation") != "committed" || w.Header().Get("ETag") != "" || !strings.Contains(w.Body.String(), `"name":"initial"`) || !strings.Contains(w.Body.String(), `"quantity":0`) || !strings.Contains(w.Body.String(), `"notes":null`) || !strings.Contains(w.Body.String(), "9007199254740993") {
		t.Fatal("PATCH presence semantics failed", w.Code, w.Body.String(), w.Header())
	}
	original := w.Body.String()
	page = get()
	if page.Header().Get("ETag") == oldTag {
		t.Fatal("public representation change retained old tag")
	}
	w = call(handler, actor, "PATCH", path, `{"quantity":0,"notes":null}`, "patch-one", oldTag)
	if w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" || w.Body.String() != original || audits.Load() != 1 {
		t.Fatal("same-key stale precondition did not reconcile prior success", w.Code, w.Body.String())
	}
	if w := call(handler, actor, "PATCH", path, `{"quantity":1}`, "patch-one", oldTag); w.Code != 409 {
		t.Fatal("conflicting receipt input allowed", w.Code)
	}
	if w := call(handler, actor, "PATCH", path, `{"quantity":1}`, "stale", oldTag); w.Code != 412 {
		t.Fatal("stale update allowed", w.Code, w.Body.String())
	}
	if w := call(handler, actor, "PATCH", path, `{}`, "missing-tag", ""); w.Code != 428 {
		t.Fatal("required condition missing", w.Code)
	}
	if w := call(handler, actor, "PUT", path, `{}`, "missing-name", "*"); w.Code != 422 {
		t.Fatal("PUT missing required fields accepted", w.Code, w.Body.String())
	}
	w = call(handler, actor, "PUT", path, `{"name":"normalize-me","metadata":{}}`, "put-one", "*")
	if w.Code != 200 || w.Header().Get("ETag") != "" || !strings.Contains(w.Body.String(), `"name":"NORMALIZED"`) || !strings.Contains(w.Body.String(), `"quantity":9`) {
		t.Fatal("PUT normalization/defaults failed", w.Code, w.Body.String())
	}
	beforeFailures := get().Body.String()
	var committedAudits int64
	if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM api_update_audit`, nil, &committedAudits); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"retarget", "before_sql", "after_sql", "after_panic", "foreign_commit", "foreign_unknown", "policy_sql", "audit_sql", "audit_error"} {
		mode.Store(failure)
		w := call(handler, actor, "PATCH", path, `{"name":"not saved"}`, failure, "*")
		if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || strings.Contains(w.Body.String(), "private") || w.Header().Get("Idempotency-Replayed") != "" {
			t.Fatal("failed update reported success", failure, w.Code, w.Body.String(), w.Header())
		}
		mode.Store("")
		if current := get(); current.Code != 200 || current.Body.String() != beforeFailures {
			t.Fatal("failed update persisted", failure, current.Code, current.Body.String())
		}
		var n int64
		if err := db.QueryRow(ctx, backend, `SELECT count(*) FROM api_update_audit`, nil, &n); err != nil || n != committedAudits {
			t.Fatal("failed audit survived", failure, n, err)
		}
	}
	if w := call(handler, actor, "PATCH", "https://example.test/items/"+strconv.FormatInt(hidden.ID, 10)+"/", `{"name":"forged"}`, "hidden", "*"); w.Code != 404 {
		t.Fatal("hidden root update exposed", w.Code, w.Body.String())
	}
	if w := call(handler, actor, "PATCH", path, `{"name":"model-denied"}`, "model-denied", "*"); w.Code != 422 {
		t.Fatal("typed update Clean skipped", w.Code, w.Body.String())
	}
	mode.Store("postcommit")
	w = call(handler, actor, "PATCH", path, `{"name":"committed"}`, "postcommit", "*")
	if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "committed" {
		t.Fatal("postcommit update outcome lost", w.Code, w.Header())
	}
	mode.Store("")
	if w := call(handler, actor, "PATCH", path, `{"name":"committed"}`, "postcommit", "*"); w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("postcommit update receipt missing", w.Code, w.Body.String())
	}
	store.Backend = lostAPICommitBackend{backend}
	lost, err := resource.UpdateHandler(key, options)
	if err != nil {
		t.Fatal(err)
	}
	w = call(lost, actor, "PATCH", path, `{"name":"uncertain"}`, "uncertain", "*")
	store.Backend = backend
	if w.Code != 503 || w.Header().Get("X-Gogo-Mutation") != "unknown" {
		t.Fatal("unknown update reported success", w.Code, w.Body.String())
	}
	if w := call(handler, actor, "PATCH", path, `{"name":"uncertain"}`, "uncertain", "*"); w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("unknown update did not reconcile", w.Code, w.Body.String())
	}
	// Different operation keys with one observed version race under the root
	// lock: only one may update; the other must see a stale representation.
	tag := get().Header().Get("ETag")
	results := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for i := range 2 {
		group.Go(func() {
			results <- call(handler, actor, "PATCH", path, `{"quantity":`+strconv.Itoa(i+1)+`}`, "concurrent-"+strconv.Itoa(i), tag)
		})
	}
	group.Wait()
	close(results)
	statuses := map[int]int{}
	for w := range results {
		statuses[w.Code]++
		if w.Code != 200 && w.Code != 412 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if statuses[200] != 1 || statuses[412] != 1 {
		t.Fatal("conditional updates lost a concurrent write", statuses)
	}
}
