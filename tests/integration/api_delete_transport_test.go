package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
)

type deleteUnreadBody struct{ reads int }

func (b *deleteUnreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}
func (*deleteUnreadBody) Close() error { return nil }

func TestPostgresAPIHTTPDeleteTransportAndConditionalBoundary(t *testing.T) {
	f := newAPIDeleteFixture(t, models.Cascade, nil)
	h := f.handler()
	for _, test := range []struct {
		name, method string
		id           int
		body         string
		headers      map[string]string
		status       int
	}{
		{"get", "GET", 1, "", nil, 405}, {"post", "POST", 1, "", nil, 405}, {"missing", "DELETE", 1, "", nil, 428},
		{"stale", "DELETE", 1, "", map[string]string{"If-Match": `"stale"`}, 412},
		{"malformed", "DELETE", 1, "", map[string]string{"If-Match": "invalid"}, 400},
		{"body", "DELETE", 1, "{}", map[string]string{"If-Match": "*"}, 400},
		{"receipt", "DELETE", 1, "", map[string]string{"If-Match": "*", "Idempotency-Key": "unsupported"}, 400},
		{"encoding", "DELETE", 1, "", map[string]string{"If-Match": "*", "Content-Encoding": "gzip"}, 400},
		{"other_condition", "DELETE", 1, "", map[string]string{"If-Match": "*", "If-None-Match": "*"}, 400},
		{"hidden", "DELETE", 3, "", map[string]string{"If-Match": "*"}, 404},
		{"absent", "DELETE", 999, "", map[string]string{"If-Match": "*"}, 404},
		{"negotiation", "DELETE", 1, "", map[string]string{"If-Match": "*", "Accept": "text/html"}, 406},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := f.request(h, test.method, test.id, test.body, test.headers)
			if w.Code != test.status || f.count("deletion_parent") != 3 || f.count("deletion_audit") != 0 {
				t.Fatal("transport refusal failed", w.Code, w.Body.String())
			}
		})
	}
	for _, kind := range []string{"query", "empty_query", "reader", "transfer", "duplicate"} {
		r := httptest.NewRequest("DELETE", "https://example.test/items/1/", nil).WithContext(auth.WithPrincipal(context.Background(), f.actor))
		r.Header.Set("If-Match", "*")
		body := &deleteUnreadBody{}
		switch kind {
		case "query":
			r.URL.RawQuery = "hidden=true"
		case "empty_query":
			r.URL.ForceQuery = true
		case "reader":
			r.Body = body
		case "transfer":
			r.TransferEncoding = []string{"chunked"}
		case "duplicate":
			r.Header.Add("If-Match", "*")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || body.reads != 0 {
			t.Fatal("unsupported request accepted or body read", kind, w.Code, w.Body.String(), body.reads)
		}
	}
	if f.checks != 0 || f.audits != 0 {
		t.Fatal("invalid transport reached mutation policy")
	}
	get := f.request(f.resource.DetailHandler(f.key), "GET", 1, "", nil)
	tag := get.Header().Get("ETag")
	if get.Code != 200 || tag == "" {
		t.Fatal("missing strong validator", get.Code)
	}
	w := f.request(h, "DELETE", 1, "", map[string]string{"If-Match": tag})
	if w.Code != 204 || w.Header().Get("ETag") != "" {
		t.Fatal("matching representation rejected", w.Code, w.Body.String())
	}
}

func TestPostgresAPIHTTPDeleteAuthenticationAndCookieCSRF(t *testing.T) {
	f := newAPIDeleteFixture(t, models.Cascade, nil)
	h := f.handler()
	bearer := f.actor
	for _, p := range []auth.Principal{{}, {ID: "one", Authenticated: true}, {ID: "one", Authenticated: true, Active: true}} {
		f.actor = p
		w := f.delete(h)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("invalid identity allowed", w.Code, w.Body.String())
		}
	}
	f.actor = auth.Principal{ID: "one", Authenticated: true, Active: true, Permissions: bearer.Permissions}
	if w := f.delete(h); w.Code != 403 {
		t.Fatal("cookie request without CSRF accepted", w.Code)
	}
	protect, err := security.CSRF(security.CSRFConfig{Secure: true})
	if err != nil {
		t.Fatal(err)
	}
	seed := httptest.NewRecorder()
	protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, security.CSRFToken(r)) })).ServeHTTP(seed, httptest.NewRequest("GET", "https://example.test/form/", nil))
	for _, origin := range []string{"https://evil.test", "https://example.test"} {
		r := httptest.NewRequest("DELETE", "https://example.test/items/1/", nil).WithContext(auth.WithPrincipal(context.Background(), f.actor))
		r.Header.Set("If-Match", "*")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRFToken", seed.Body.String())
		for _, cookie := range seed.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 403
		if origin == "https://example.test" {
			want = 204
		}
		if w.Code != want {
			t.Fatal("CSRF origin/token boundary failed", origin, w.Code, w.Body.String())
		}
	}
}

func TestPostgresAPIHTTPDeleteCommitOutcomesAndRegistrationSnapshot(t *testing.T) {
	for _, mode := range []string{"postcommit", "unknown", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPIDeleteFixture(t, models.Cascade, nil)
			f.mode = mode
			if mode == "unknown" {
				f.store.Backend = lostAPICommitBackend{f.backend}
			}
			h := f.handler()
			if mode == "snapshot" {
				f.store.Backend = nil
				f.store.Registry = nil
				f.store.AfterDelete[0] = func(context.Context, orm.DeleteEvent) error { return errors.New("changed after registration") }
			}
			w := f.delete(h)
			wantStatus, wantOutcome := 204, "committed"
			if mode == "postcommit" {
				wantStatus = 500
			}
			if mode == "unknown" {
				wantStatus, wantOutcome = 503, "unknown"
			}
			if w.Code != wantStatus || w.Header().Get("X-Gogo-Mutation") != wantOutcome || f.count("deletion_parent") != 2 || f.count("deletion_child") != 0 || f.count("deletion_audit") != 1 || strings.Contains(w.Body.String(), "private") {
				t.Fatal("outer commit misclassified", mode, w.Code, w.Body.String(), w.Header())
			}
			if mode == "unknown" && !strings.Contains(w.Body.String(), "MUTATION_UNKNOWN") {
				t.Fatal("uncertainty hidden")
			}
		})
	}
}

func TestPostgresAPIHTTPDeleteCancellationNestingAndLimits(t *testing.T) {
	f := newAPIDeleteFixture(t, models.Cascade, nil)
	for _, keyErr := range []error{nil, api.ErrInvalidKey} {
		ctx, cancel := context.WithCancel(auth.WithPrincipal(context.Background(), f.actor))
		f.key = func(*http.Request) (api.Values, error) { cancel(); return api.Values{"id": 1}, keyErr }
		h := f.handler()
		r := httptest.NewRequest("DELETE", "https://example.test/items/1/", nil).WithContext(ctx)
		r.Header.Set("If-Match", "*")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		cancel()
		if w.Code != 503 || w.Header().Get("X-Gogo-Mutation") != "unchanged" {
			t.Fatal("cancellation became not found", w.Code, w.Body.String())
		}
	}
	f.key = func(*http.Request) (api.Values, error) { return api.Values{"id": 1}, nil }
	h := f.handler()
	err := db.Atomic(context.Background(), f.backend, db.AtomicOptions{}, func(ctx context.Context) error {
		r := httptest.NewRequest("DELETE", "https://example.test/items/1/", nil).WithContext(auth.WithPrincipal(ctx, f.actor))
		r.Header.Set("If-Match", "*")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 || w.Header().Get("X-Gogo-Mutation") != "unchanged" {
			t.Fatal("nested durable delete accepted", w.Code, w.Body.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.options.MaxObjects = 1
	f.options.MaxWork = 100
	w := f.delete(f.handler())
	if w.Code != 409 || !strings.Contains(w.Body.String(), "DELETE_LIMIT") {
		t.Fatal("graph cap not enforced", w.Code, w.Body.String())
	}
	if f.count("deletion_parent") != 3 || f.count("deletion_child") != 1 || f.count("deletion_audit") != 0 || f.checks != 0 {
		t.Fatal("rejected deletion wrote effects")
	}
}

func TestPostgresAPIHTTPDeleteConcurrentRootHasOneCommittedAudit(t *testing.T) {
	f := newAPIDeleteFixture(t, models.Cascade, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	original := f.options.ValidateDelete
	f.options.ValidateDelete = func(ctx context.Context, p auth.Principal, plan orm.DeletionPlan) error {
		once.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		return original(ctx, p, plan)
	}
	h := f.handler()
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- f.delete(h) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first deletion never locked")
	}
	go func() { done <- f.delete(h) }()
	close(release)
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case w := <-done:
			statuses[w.Code]++
		case <-time.After(5 * time.Second):
			t.Fatal("deletion did not finish")
		}
	}
	if statuses[204] != 1 || statuses[404] != 1 || f.count("deletion_audit") != 1 {
		t.Fatal("concurrent deletion duplicated effects", statuses)
	}
}

func TestPostgresAPIHTTPDeleteHiddenDependencyAndCanceledAudit(t *testing.T) {
	t.Run("hidden_cascade", func(t *testing.T) {
		f := newAPIDeleteFixture(t, models.Cascade, nil)
		f.sql(context.Background(), `UPDATE deletion_child SET tenant='two'`)
		w := f.delete(f.handler())
		if w.Code != 409 || w.Header().Get("X-Gogo-Mutation") != "unchanged" || f.count("deletion_parent") != 3 || f.count("deletion_child") != 1 || f.count("deletion_audit") != 0 {
			t.Fatal("hidden referencing row was deleted or partial graph committed", w.Code, w.Body.String(), w.Header())
		}
	})
	for _, afterCommit := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel_after_commit_", afterCommit), func(t *testing.T) {
			f := newAPIDeleteFixture(t, models.Cascade, nil)
			ctx, cancel := context.WithCancel(auth.WithPrincipal(context.Background(), f.actor))
			defer cancel()
			audit := f.options.Audit
			f.options.Audit = func(ctx context.Context, event api.MutationEvent) error {
				if err := audit(ctx, event); err != nil {
					return err
				}
				if afterCommit {
					return db.OnCommit(ctx, f.backend.Alias(), func(context.Context) error { cancel(); return nil }, false)
				}
				cancel()
				return nil
			}
			r := httptest.NewRequest("DELETE", "https://example.test/items/1/", nil).WithContext(ctx)
			r.Header.Set("If-Match", "*")
			w := httptest.NewRecorder()
			f.handler().ServeHTTP(w, r)
			status, outcome, parents, audits := 503, "unchanged", int64(3), int64(0)
			if afterCommit {
				status, outcome, parents, audits = 204, "committed", 2, 1
			}
			if w.Code != status || w.Header().Get("X-Gogo-Mutation") != outcome || f.count("deletion_parent") != parents || f.count("deletion_audit") != audits {
				t.Fatal("cancellation changed durable truth", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}

func TestPostgresAPIHTTPDeleteDecoderJoinedFailureIsNotInvalidIdentity(t *testing.T) {
	f := newAPIDeleteFixture(t, models.Cascade, nil)
	f.key = func(*http.Request) (api.Values, error) {
		return nil, errors.Join(api.ErrInvalidKey, &db.Error{Code: db.Unavailable})
	}
	w := f.delete(f.handler())
	if w.Code != 503 || f.checks != 0 || f.count("deletion_audit") != 0 {
		t.Fatal("decoder outage became absence", w.Code, w.Body.String())
	}
}
