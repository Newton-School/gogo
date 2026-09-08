package postgres_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/contrib/redirects"
	"github.com/Newton-School/gogo/core/contrib/sites"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

func setupRedirects(t *testing.T) (*postgres.Backend, *orm.Store, *sites.Site, *migrations.Executor) {
	t.Helper()
	b := openTest(t)
	runner := &migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: append(sites.Migrations(), redirects.Migrations()...)}
	if err := runner.Apply(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{(&sites.Site{}).Schema(), (&redirects.Redirect{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	site, err := sites.NewSite("example.test", "Example")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), site, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
		t.Fatal(err)
	}
	return b, store, site, runner
}

func saveRedirect(t *testing.T, store *orm.Store, siteID, old, target string, permanent bool) *redirects.Redirect {
	t.Helper()
	r, err := redirects.NewRedirect(siteID, old, target)
	if err != nil {
		t.Fatal(err)
	}
	r.Permanent = permanent
	if err := store.Save(context.Background(), r, orm.SaveOptions{ForceInsert: true, Prepare: redirects.Prepare}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresRedirectsMigrationConstraintsAndLookup(t *testing.T) {
	b, store, site, runner := setupRedirects(t)
	ctx := context.Background()
	first := saveRedirect(t, store, site.ID, "/old?x=1", "/next", false)
	saveRedirect(t, store, site.ID, "/next?x=1", "", true)
	other, err := sites.NewSite("other.test", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, other, orm.SaveOptions{ForceInsert: true, Prepare: sites.Prepare}); err != nil {
		t.Fatal(err)
	}
	saveRedirect(t, store, other.ID, "/old?x=1", "/other", true)
	duplicate, _ := redirects.NewRedirect(site.ID, "/old?x=1", "/duplicate")
	if err := store.Save(ctx, duplicate, orm.SaveOptions{ForceInsert: true, Prepare: redirects.Prepare}); !db.IsCode(err, db.UniqueViolation) {
		t.Fatal("missing composite uniqueness", err)
	}
	missing, _ := redirects.NewRedirect("00000000-0000-4000-8000-000000000099", "/missing", "/")
	// A deferred autocommit failure can arrive only after the RETURNING row.
	// Save must report it instead of returning a false successful write.
	saveErr := store.Save(ctx, missing, orm.SaveOptions{ForceInsert: true, Prepare: redirects.Prepare})
	missingCount, countErr := orm.For(store, func() *redirects.Redirect { return &redirects.Redirect{} }).Filter(orm.Q("id", missing.ID)).Count(ctx)
	if !db.IsCode(saveErr, db.ForeignKeyViolation) || countErr != nil || missingCount != 0 {
		t.Fatal("autocommit FK outcome", saveErr, "persisted rows", missingCount, countErr)
	}
	r, err := redirects.New(redirects.Config{Backend: b, PreserveQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	input := redirects.LookupInput{SiteID: site.ID, URI: "/old?x=1", Origin: "https://example.test"}
	match, found, err := r.Lookup(ctx, input)
	if err != nil || !found || match.ID != first.ID || match.Location != "/next?x=1" || match.Permanent {
		t.Fatal(match, found, err)
	}
	input.URI = "/old"
	if match, found, err := r.Lookup(ctx, input); err != nil || found || match != (redirects.Match{}) {
		t.Fatal(match, found, err)
	}
	input.URI = "/old?x=1"
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(txctx context.Context) error {
		if match, found, err := r.Lookup(txctx, input); !errors.Is(err, redirects.ErrTransaction) || found || match != (redirects.Match{}) {
			t.Fatal(match, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, other); err != nil {
		t.Fatal(err)
	}
	if count, err := orm.For(store, func() *redirects.Redirect { return &redirects.Redirect{} }).Filter(orm.Q("old_path", "/old?x=1")).Count(ctx); err != nil || count != 1 {
		t.Fatal("cascade", count, err)
	}
	if err := runner.Reverse(ctx, "gogo_redirects.zero"); err != nil {
		t.Fatal(err)
	}
	if match, found, err := r.Lookup(ctx, input); !errors.Is(err, redirects.ErrUnavailable) || found || match != (redirects.Match{}) {
		t.Fatal(match, found, err)
	}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if match, found, err := r.Lookup(ctx, input); err != nil || found || match != (redirects.Match{}) {
		t.Fatal("reapply", match, found, err)
	}
}

type redirectSnapshotBackend struct {
	db.Backend
	t                *testing.T
	afterSite        func()
	commits, queries int
}

func (b *redirectSnapshotBackend) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	var isolation, readOnly string
	if err := db.QueryRow(ctx, tx, "SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only')", nil, &isolation, &readOnly); err != nil {
		tx.Rollback()
		return nil, err
	}
	if isolation != "repeatable read" || readOnly != "on" {
		b.t.Fatal(isolation, readOnly)
	}
	return &redirectSnapshotTx{Transaction: tx, owner: b}, nil
}

type redirectSnapshotTx struct {
	db.Transaction
	owner *redirectSnapshotBackend
}

func (tx *redirectSnapshotTx) Query(ctx context.Context, s string, args ...any) (db.Rows, error) {
	tx.owner.queries++
	rows, err := tx.Transaction.Query(ctx, s, args...)
	if err == nil && strings.Contains(s, `FROM "gogo_sites"`) && tx.owner.afterSite != nil {
		f := tx.owner.afterSite
		tx.owner.afterSite = nil
		f()
	}
	return rows, err
}
func (tx *redirectSnapshotTx) Commit() error { tx.owner.commits++; return tx.Transaction.Commit() }

func TestPostgresRedirectsReadOnlyRepeatableSnapshot(t *testing.T) {
	b, store, site, _ := setupRedirects(t)
	saveRedirect(t, store, site.ID, "/a", "/b", true)
	second := saveRedirect(t, store, site.ID, "/b", "/end", true)
	wrapped := &redirectSnapshotBackend{Backend: b, t: t}
	wrapped.afterSite = func() {
		if _, err := b.Exec(context.Background(), `UPDATE "gogo_redirects" SET "new_path"=$1 WHERE "id"=$2`, "/a", second.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Exec(context.Background(), `UPDATE "gogo_sites" SET "active"=false WHERE "id"=$1`, site.ID); err != nil {
			t.Fatal(err)
		}
	}
	r, err := redirects.New(redirects.Config{Backend: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	input := redirects.LookupInput{SiteID: site.ID, URI: "/a", Origin: "https://example.test"}
	if match, found, err := r.Lookup(context.Background(), input); err != nil || !found || match.Location != "/b" {
		t.Fatal("snapshot changed", match, found, err)
	}
	if wrapped.commits != 1 || wrapped.queries != 4 {
		t.Fatal(wrapped.commits, wrapped.queries)
	}
	if match, found, err := r.Lookup(context.Background(), input); !errors.Is(err, redirects.ErrSiteNotConfigured) || found || match != (redirects.Match{}) {
		t.Fatal("inactive next snapshot", match, found, err)
	}
	if _, err := b.Exec(context.Background(), `UPDATE "gogo_sites" SET "active"=true WHERE "id"=$1`, site.ID); err != nil {
		t.Fatal(err)
	}
	if match, found, err := r.Lookup(context.Background(), input); !errors.Is(err, redirects.ErrCycle) || found || match != (redirects.Match{}) {
		t.Fatal("cycle next snapshot", match, found, err)
	}
}

func TestPostgresRedirectsRouterHTTPBoundary(t *testing.T) {
	b, store, site, _ := setupRedirects(t)
	for _, row := range []struct {
		old, target string
		permanent   bool
	}{
		{"/permanent", "/end", true}, {"/temporary", "/end", false}, {"/gone", "", true},
		{"/cycle", "/cycle", true}, {"/external", "https://other.test/end", true}, {"/slash/?x=1", "/end", true},
	} {
		saveRedirect(t, store, site.ID, row.old, row.target, row.permanent)
	}
	selector, err := sites.New(sites.Config{Backend: b, SiteID: site.ID, AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := redirects.New(redirects.Config{Backend: b, AppendSlash: true, PreserveQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	denied, externalAllowed := false, false
	options := redirects.HandlerOptions{Sites: selector, Authorize: func(ctx context.Context, info sites.Info) error {
		current, err := orm.For(store, func() *sites.Site { return &sites.Site{} }).Filter(orm.Q("id", info.ID), orm.Q("active", true)).Get(ctx)
		if err != nil {
			return redirects.ErrUnavailable
		}
		if denied || current.ID != site.ID {
			return redirects.ErrForbidden
		}
		return nil
	}, AllowExternal: func(context.Context, sites.Info, string) error {
		if !externalAllowed {
			return redirects.ErrForbidden
		}
		return nil
	}}
	handler, err := redirects.NewHandler(r, options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("/view", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }), "view"))
	if err != nil {
		t.Fatal(err)
	}
	router, err = router.WithNotFound(handler)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		uri      string
		status   int
		location string
	}{
		{"/permanent", 301, "/end"}, {"/temporary", 302, "/end"}, {"/gone", 410, ""}, {"/missing", 404, ""},
		{"/view", 404, ""}, {"/cycle", 503, ""}, {"/external", 403, ""}, {"/slash?x=1", 301, "/end?x=1"},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "https://example.test"+test.uri, nil))
			if response.Code != test.status || response.Header().Get("Location") != test.location {
				t.Fatal(test, method, response.Code, response.Header())
			}
			if method == "HEAD" && response.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		}
	}
	externalAllowed = true
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test/external", nil))
	if response.Code != 301 || response.Header().Get("Location") != "https://other.test/end" {
		t.Fatal(response.Code, response.Header())
	}
	denied = true
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "https://example.test/permanent", nil))
	if response.Code != 403 || response.Header().Get("Location") != "" {
		t.Fatal(response.Code, response.Header())
	}
}
