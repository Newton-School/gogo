package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type createReceiptFixture struct {
	backend    db.Backend
	store      *orm.Store
	resource   *api.Resource
	options    api.CreateOptions
	actor      auth.Principal
	hide, deny atomic.Bool
	mode       atomic.Value
	mutations  atomic.Int64
}

func newCreateReceiptFixture(t *testing.T) *createReceiptFixture {
	t.Helper()
	backend := testservice.Postgres(t)
	f := &createReceiptFixture{backend: backend}
	f.mode.Store("")
	ctx := context.Background()
	schema := (&apiCreatedProduct{}).Schema()
	for _, definition := range append(api.IdempotencySchemas(), schema) {
		if err := backend.SchemaEditor().CreateModel(ctx, backend, definition); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.backend.Exec(ctx, `CREATE TABLE api_receipt_audit (object_id bigint NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	f.store = orm.New(f.backend, registry)
	input, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"id", "name", "created_at"}})
	if err != nil {
		t.Fatal(err)
	}
	f.resource, err = api.NewResource(api.ResourceConfig{Store: f.store, Model: schema.Key(), Serializer: output, Policy: auth.ModelPolicy{},
		Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
			return orm.Q("tenant", p.ID), nil
		},
		AllowField: func(_ context.Context, _ auth.Principal, _ models.Record, name string) (bool, error) {
			return name != "name" || !f.hide.Load(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.actor, err = auth.ConstrainPrincipal(auth.Principal{ID: "one", Authenticated: true, Active: true, AuthVersion: 1, Permissions: []string{"shop.add_createdproduct"}}, []string{"shop.add_createdproduct"})
	if err != nil {
		t.Fatal(err)
	}
	f.options = api.CreateOptions{Input: input, Factory: func() models.Model { return &apiCreatedProduct{} },
		Prepare: func(ctx context.Context, row models.Record) error {
			if err := row.Set("tenant", auth.FromContext(ctx).ID); err != nil {
				return err
			}
			return row.Set("secret", "server-only")
		},
		ValidateWrite: func(_ context.Context, p auth.Principal, row models.Record) error {
			tenant, err := row.Get("tenant")
			if err != nil || tenant != p.ID || f.deny.Load() {
				return auth.ErrPermissionDenied
			}
			return nil
		},
		Audit: func(ctx context.Context, event api.MutationEvent) error {
			f.mutations.Add(1)
			if _, err := db.ExecutorFor(ctx, f.backend).Exec(ctx, `INSERT INTO api_receipt_audit VALUES ($1)`, event.ObjectKey["id"].(json.Number).String()); err != nil {
				return err
			}
			if f.mode.Load() == "audit_error" {
				return errors.New("synthetic private audit detail")
			}
			if f.mode.Load() == "postcommit" {
				return db.OnCommit(ctx, f.backend.Alias(), func(context.Context) error { return errors.New("synthetic private postcommit detail") }, false)
			}
			return nil
		},
		Location: func(_ context.Context, key api.Values) (string, error) {
			return "/products/" + key["id"].(json.Number).String() + "/", nil
		},
		Idempotency: &api.CreateIdempotencyOptions{Version: "v1", Scope: func(_ context.Context, p auth.Principal) (string, error) { return "tenant:" + p.ID, nil },
			Vary: func(r *http.Request) (api.Values, error) { return api.Values{"locale": r.Header.Get("X-Locale")}, nil },
			Redact: func(ctx context.Context, _ auth.Principal, row models.Record, body api.Values) (api.Values, error) {
				switch f.mode.Load() {
				case "redact":
					delete(body, "created_at")
				case "broaden":
					body["name"] = "changed receipt"
				case "panic":
					panic("synthetic private redactor detail")
				case "record_mutation":
					return body, row.Set("name", "unauthorized")
				case "nested_mutation":
					id, _ := row.Get("id")
					_, err := db.ExecutorFor(ctx, f.backend).Exec(ctx, `UPDATE shop_createdproduct SET name='unauthorized' WHERE id=$1`, id)
					return body, err
				}
				return body, nil
			},
		},
	}
	return f
}

func (f *createReceiptFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	h, err := f.resource.CreateHandler(f.options)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func receiptHTTP(h http.Handler, actor auth.Principal, path, body string, headers http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), actor))
	r.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		r.Header[key] = values
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (f *createReceiptFixture) counts(t *testing.T, expected int64) {
	t.Helper()
	for _, table := range []string{"shop_createdproduct", "api_receipt_audit", "gogo_idempotency"} {
		var n int64
		if err := db.QueryRow(context.Background(), f.backend, "SELECT count(*) FROM "+table, nil, &n); err != nil || n != expected {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, expected, err)
		}
	}
}

func TestPostgresAPIHTTPCreateReceiptsConcurrentReplayCurrentAuthorityAndInputBinding(t *testing.T) {
	f := newCreateReceiptFixture(t)
	h := f.handler(t)
	headers := http.Header{"Idempotency-Key": {"create-one"}, "X-Locale": {"en"}}
	const path = "https://example.test/products/"
	const body = `{"name":"normalize-me"}`
	results := make(chan *httptest.ResponseRecorder, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { results <- receiptHTTP(h, f.actor, path, body, headers) })
	}
	group.Wait()
	close(results)
	var original string
	replays := 0
	for w := range results {
		if w.Code != 201 || w.Header().Get("X-Gogo-Mutation") != "committed" || w.Header().Get("Location") != "/products/1/" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(w.Code, w.Body.String(), w.Header())
		}
		if w.Header().Get("Idempotency-Replayed") == "true" {
			replays++
		}
		if original != "" && w.Body.String() != original {
			t.Fatal("concurrent receipts differ")
		}
		original = w.Body.String()
	}
	if replays != 7 || f.mutations.Load() != 1 || !strings.Contains(original, `"name":"NORMALIZED"`) || strings.Contains(original, "server-only") {
		t.Fatal("write repeated or typed normalization lost", replays, f.mutations.Load(), original)
	}
	f.counts(t, 1)
	for _, test := range []struct{ path, body, locale string }{
		{path, `{"name":"different"}`, "en"},
		{"https://example.test/other-products/", body, "en"},
		{"https://another.test/products/", body, "en"},
		{path, body, "fr"},
	} {
		w := receiptHTTP(h, f.actor, test.path, test.body, http.Header{"Idempotency-Key": {"create-one"}, "X-Locale": {test.locale}})
		if w.Code != 409 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "NORMALIZED") {
			t.Fatal("operation arguments not bound", w.Code, w.Body.String())
		}
	}
	f.hide.Store(true)
	f.mode.Store("redact")
	changedActor := f.actor
	changedActor.AuthVersion++
	w := receiptHTTP(h, changedActor, path, body, headers)
	if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" || strings.Contains(w.Body.String(), "name") || strings.Contains(w.Body.String(), "created_at") {
		t.Fatal("current visibility not reapplied", w.Code, w.Body.String())
	}
	f.hide.Store(false)
	f.mode.Store("")
	if w := receiptHTTP(h, f.actor, path, body, headers); w.Body.String() != original {
		t.Fatal("redaction modified stored receipt", w.Body.String())
	}
	f.deny.Store(true)
	if w := receiptHTTP(h, f.actor, path, body, headers); w.Code != 403 || w.Header().Get("Location") != "" {
		t.Fatal("current write denial bypassed", w.Code)
	}
	f.deny.Store(false)
	noGrant, err := auth.ConstrainPrincipal(auth.Principal{ID: f.actor.ID, Active: true, Authenticated: true}, []string{"shop.add_createdproduct"})
	if err != nil {
		t.Fatal(err)
	}
	if w := receiptHTTP(h, noGrant, path, body, headers); w.Code != 403 {
		t.Fatal("current grant loss bypassed", w.Code)
	}
	if _, err := f.backend.Exec(context.Background(), `UPDATE shop_createdproduct SET tenant='hidden' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if w := receiptHTTP(h, f.actor, path, body, headers); w.Code != 404 || w.Header().Get("Location") != "" {
		t.Fatal("hidden replay disclosed receipt", w.Code, w.Body.String())
	}
	if _, err := f.backend.Exec(context.Background(), `UPDATE shop_createdproduct SET tenant='one' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"broaden", "panic", "record_mutation", "nested_mutation"} {
		f.mode.Store(mode)
		w := receiptHTTP(h, f.actor, path, body, headers)
		if w.Code < 400 || w.Header().Get("Idempotency-Replayed") != "" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "private") {
			t.Fatal("unsafe replay callback", mode, w.Code, w.Body.String())
		}
	}
	f.mode.Store("")
	if w := receiptHTTP(h, f.actor, path, body, headers); w.Code != 201 || w.Body.String() != original {
		t.Fatal("failed replay mutated current object or receipt", w.Code, w.Body.String())
	}
	f.counts(t, 1)
	if f.mutations.Load() != 1 {
		t.Fatal("replay repeated mutation")
	}
	other, err := auth.ConstrainPrincipal(auth.Principal{ID: "two", Active: true, Authenticated: true, Permissions: []string{"shop.add_createdproduct"}}, []string{"shop.add_createdproduct"})
	if err != nil {
		t.Fatal(err)
	}
	if w := receiptHTTP(h, other, path, body, headers); w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("actor/scope operation not independent", w.Code, w.Body.String())
	}
	f.counts(t, 2)
}

func TestPostgresAPIHTTPCreateReceiptsRejectAmbiguityAndReconcileCommitFailures(t *testing.T) {
	f := newCreateReceiptFixture(t)
	h := f.handler(t)
	const path = "https://example.test/products/"
	const body = `{"name":"product"}`
	for _, headers := range []http.Header{nil, {"Idempotency-Key": {""}}, {"Idempotency-Key": {"a", "b"}}, {"Idempotency-Key": {"bad key"}}, {"Idempotency-Key": {"a"}, "If-Match": {"*"}}, {"Idempotency-Key": {"a"}, "If-None-Match": {"*"}}} {
		if w := receiptHTTP(h, f.actor, path, body, headers); w.Code != 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" {
			t.Fatal("ambiguous operation accepted", w.Code, w.Body.String())
		}
	}
	f.counts(t, 0)
	for _, mode := range []string{"audit_error", "broaden", "panic", "record_mutation", "nested_mutation"} {
		f.mode.Store(mode)
		w := receiptHTTP(h, f.actor, path, body, http.Header{"Idempotency-Key": {mode}})
		if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "private") {
			t.Fatal("failed create receipt returned success", mode, w.Code, w.Body.String())
		}
		f.counts(t, 0)
	}
	f.mode.Store("postcommit")
	w := receiptHTTP(h, f.actor, path, body, http.Header{"Idempotency-Key": {"postcommit"}})
	if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "committed" || w.Header().Get("Location") != "" {
		t.Fatal("committed callback failure lost outcome", w.Code, w.Header())
	}
	f.counts(t, 1)
	f.mode.Store("")
	w = receiptHTTP(h, f.actor, path, body, http.Header{"Idempotency-Key": {"postcommit"}})
	if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("committed callback failure repeated mutation", w.Code, w.Body.String())
	}
	f.counts(t, 1)
	// Construct the complete resource operation against a lost-ack backend. A
	// later handler on the healthy backend must reconcile the SAME key.
	f.store.Backend = lostAPICommitBackend{f.backend}
	lost := f.handler(t)
	w = receiptHTTP(lost, f.actor, path, body, http.Header{"Idempotency-Key": {"uncertain"}})
	f.store.Backend = f.backend
	if w.Code != 503 || w.Header().Get("X-Gogo-Mutation") != "unknown" || w.Header().Get("Location") != "" {
		t.Fatal("uncertain create reported success", w.Code, w.Body.String())
	}
	f.counts(t, 2)
	w = receiptHTTP(h, f.actor, path, body, http.Header{"Idempotency-Key": {"uncertain"}})
	if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("unknown commit did not reconcile", w.Code, w.Body.String())
	}
	f.counts(t, 2)
	f.options.Idempotency.Optional = true
	unkeyed := f.handler(t)
	for range 2 {
		if w := receiptHTTP(unkeyed, f.actor, path, body, nil); w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "" {
			t.Fatal("explicit optional key policy failed", w.Code, w.Body.String())
		}
	}
	var n int64
	if err := db.QueryRow(context.Background(), f.backend, `SELECT count(*) FROM shop_createdproduct`, nil, &n); err != nil || n != 4 {
		t.Fatal("unkeyed create deduplicated", n, err)
	}
}
