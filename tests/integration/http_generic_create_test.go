package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/templates"
	"github.com/Newton-School/gogo/core/urls"
)

type genericCreateNativeBackend struct {
	db.Backend
	begins, queries, execs, commits, rollbacks int
	commitMode                                 string
	cancel                                     context.CancelFunc
}
type genericCreateNativeTx struct {
	db.Transaction
	owner *genericCreateNativeBackend
}

func (b *genericCreateNativeBackend) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	b.queries++
	return b.Backend.Query(ctx, q, args...)
}
func (b *genericCreateNativeBackend) Exec(ctx context.Context, q string, args ...any) (db.Result, error) {
	b.execs++
	return b.Backend.Exec(ctx, q, args...)
}
func (b *genericCreateNativeBackend) BeginTx(ctx context.Context, o db.TxOptions) (db.Transaction, error) {
	b.begins++
	tx, err := b.Backend.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &genericCreateNativeTx{Transaction: tx, owner: b}, nil
}
func (tx *genericCreateNativeTx) Query(ctx context.Context, q string, args ...any) (db.Rows, error) {
	tx.owner.queries++
	return tx.Transaction.Query(ctx, q, args...)
}
func (tx *genericCreateNativeTx) Exec(ctx context.Context, q string, args ...any) (db.Result, error) {
	tx.owner.execs++
	return tx.Transaction.Exec(ctx, q, args...)
}
func (tx *genericCreateNativeTx) Commit() error {
	tx.owner.commits++
	err := tx.Transaction.Commit()
	if err != nil {
		return err
	}
	if tx.owner.commitMode == "unknown" {
		return &db.Error{Code: db.UnknownCommit}
	}
	if tx.owner.cancel != nil {
		tx.owner.cancel()
	}
	return nil
}
func (tx *genericCreateNativeTx) Rollback() error {
	tx.owner.rollbacks++
	return tx.Transaction.Rollback()
}

func genericCreateNativeOptions(t *testing.T) (ghttp.CreateViewOptions, *genericCreateNativeBackend) {
	t.Helper()
	backend := testservice.Postgres(t)
	schema := models.Schema{AppLabel: "forms", Name: "Article", Fields: []models.Field{
		models.BigAutoField("id"), models.TextField("title", models.WithColumn("headline")), models.TextField("tenant"),
	}}
	if err := backend.SchemaEditor().CreateModel(context.Background(), backend, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Exec(context.Background(), `CREATE TABLE create_audit (article_id bigint NOT NULL, event text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	observed := &genericCreateNativeBackend{Backend: backend}
	return ghttp.CreateViewOptions{
		TemplateViewOptions: ghttp.TemplateViewOptions{
			ReadViewOptions: ghttp.ReadViewOptions{Authorize: func(*http.Request) error { return nil }, AllowOptions: true},
			TemplateName:    "article/create.html", Templates: templates.Config{Loaders: []templates.Loader{templates.MapLoader{
				"article/create.html": `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">{{ form.html }}<button type="submit">Create article</button></form>`,
			}}},
		},
		Store: orm.New(observed, registry), Model: schema.Key(), Fields: []string{"title", "tenant"}, ReadonlyFields: []string{"tenant"},
		Factory: func() models.Model { record, _ := models.NewRecord(schema); return record },
		Scope: func(_ context.Context, p auth.Principal, _ models.Schema) (db.Predicate, error) {
			return orm.Q("tenant", p.ID), nil
		},
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
		Prepare: func(ctx context.Context, r models.Record) error { return r.Set("tenant", auth.FromContext(ctx).ID) },
		ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
			tenant, err := r.Get("tenant")
			if err != nil || tenant != p.ID {
				return auth.ErrPermissionDenied
			}
			return nil
		},
		SuccessURL: func(_ context.Context, id map[string]any) (string, error) {
			return fmt.Sprintf("/articles/%v/", id["id"]), nil
		},
	}, observed
}

func genericCreateNativeRouter(t *testing.T, o ghttp.CreateViewOptions) http.Handler {
	t.Helper()
	h, err := ghttp.NewCreateView(o)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("articles/new/", h, "article-create"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := router.Reverse("article-create", nil, nil)
	if err != nil || path != "/articles/new/" {
		t.Fatal(path, err)
	}
	return router
}
func genericCreateNativeRequest(ctx context.Context, method, body string) *http.Request {
	r := httptest.NewRequest(method, "https://example.test/articles/new/", strings.NewReader(body))
	if method != "POST" && body == "" {
		r.Body = http.NoBody
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://example.test")
	}
	return r.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
}
func genericCreateNativePost(t *testing.T, h http.Handler, ctx context.Context, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	get := httptest.NewRecorder()
	h.ServeHTTP(get, genericCreateNativeRequest(ctx, "GET", ""))
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(get.Body.String())
	if get.Code != 200 || len(match) != 2 || len(get.Result().Cookies()) != 1 {
		t.Fatal("unbound form", get.Code, get.Body.String(), get.Header())
	}
	values.Set("csrfmiddlewaretoken", match[1])
	r := genericCreateNativeRequest(ctx, "POST", values.Encode())
	r.AddCookie(get.Result().Cookies()[0])
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}
func genericCreateNativeCount(t *testing.T, b db.Backend, table string) int64 {
	t.Helper()
	var count int64
	if table != "forms_article" && table != "create_audit" {
		t.Fatal("unknown owned fixture table")
	}
	if err := db.QueryRow(context.Background(), b, "SELECT count(*) FROM "+table, nil, &count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresGenericCreateFormAndAtomicAudit(t *testing.T) {
	o, b := genericCreateNativeOptions(t)
	o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		id, _ := event.Record.Get("id")
		_, err := db.ExecutorFor(ctx, b).Exec(ctx, `INSERT INTO create_audit VALUES ($1,'created')`, id)
		return err
	}}
	h := genericCreateNativeRouter(t, o)
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericCreateNativeRequest(context.Background(), method, ""))
		if out.Code != 200 || b.begins != 0 || b.queries != 0 || b.execs != 0 || method == "HEAD" && out.Body.Len() != 0 {
			t.Fatal(method, out.Code, b)
		}
	}
	for _, values := range []url.Values{{"title": {"ok"}, "tenant": {"two"}}, {"title": {""}}} {
		out := genericCreateNativePost(t, h, context.Background(), values)
		if out.Code != 400 && out.Code != 422 {
			t.Fatal(out.Code, out.Body.String())
		}
		if b.begins != 0 || b.queries != 0 || b.execs != 0 {
			t.Fatal("invalid form queried", b)
		}
	}
	out := genericCreateNativePost(t, h, context.Background(), url.Values{"title": {"<escaped article>"}})
	if out.Code != 303 || out.Header().Get("Location") != "/articles/1/" || b.begins != 1 || b.commits != 1 || b.rollbacks != 0 {
		t.Fatal(out.Code, out.Body.String(), out.Header(), b)
	}
	if genericCreateNativeCount(t, b.Backend, "forms_article") != 1 || genericCreateNativeCount(t, b.Backend, "create_audit") != 1 {
		t.Fatal("parent and audit did not commit together")
	}
	var title, tenant string
	if err := db.QueryRow(context.Background(), b.Backend, `SELECT headline,tenant FROM forms_article WHERE id=1`, nil, &title, &tenant); err != nil || title != "<escaped article>" || tenant != "one" {
		t.Fatal(title, tenant, err)
	}
}

func TestPostgresGenericCreateFinalCallbacksCannotChangeSavedState(t *testing.T) {
	for _, mode := range []string{"after-save ownership", "success URL title", "final policy ownership", "unsafe location", "denied proposed"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateNativeOptions(t)
			o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
				id, _ := event.Record.Get("id")
				exec := db.ExecutorFor(ctx, b)
				if _, err := exec.Exec(ctx, `INSERT INTO create_audit VALUES ($1,'created')`, id); err != nil {
					return err
				}
				if mode == "after-save ownership" {
					_, err := exec.Exec(ctx, `UPDATE forms_article SET tenant='two' WHERE id=$1`, id)
					return err
				}
				return nil
			}}
			if mode == "success URL title" {
				o.SuccessURL = func(ctx context.Context, id map[string]any) (string, error) {
					_, err := db.ExecutorFor(ctx, b).Exec(ctx, `UPDATE forms_article SET headline='retargeted' WHERE id=$1`, id["id"])
					return "/done/", err
				}
			}
			if mode == "unsafe location" {
				o.SuccessURL = func(context.Context, map[string]any) (string, error) { return "//other.test/", nil }
			}
			if mode == "denied proposed" {
				o.ValidateWrite = func(context.Context, auth.Principal, models.Record) error { return auth.ErrPermissionDenied }
			}
			if mode == "final policy ownership" {
				o.ValidateWrite = func(ctx context.Context, _ auth.Principal, r models.Record) error {
					if r.State().Persisted {
						id, _ := r.Get("id")
						_, err := db.ExecutorFor(ctx, b).Exec(ctx, `UPDATE forms_article SET tenant='two' WHERE id=$1`, id)
						return err
					}
					return nil
				}
			}
			out := genericCreateNativePost(t, genericCreateNativeRouter(t, o), context.Background(), url.Values{"title": {"new"}})
			if out.Code != 403 && out.Code != 503 {
				t.Fatal(mode, out.Code, out.Body.String())
			}
			if out.Header().Get("Location") != "" || b.commits != 0 || b.rollbacks != 1 || genericCreateNativeCount(t, b.Backend, "forms_article") != 0 || genericCreateNativeCount(t, b.Backend, "create_audit") != 0 {
				t.Fatal("drift was not atomic rollback", out.Code, b)
			}
		})
	}
}

func TestPostgresGenericCreateDeferredCommitAndUnknownOutcomes(t *testing.T) {
	for _, mode := range []string{"deferred foreign key", "known committed callback", "committed unknown", "late cancellation"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericCreateNativeOptions(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "deferred foreign key":
				for _, statement := range []string{`CREATE TABLE allowed_title (title text PRIMARY KEY)`, `ALTER TABLE forms_article ADD CONSTRAINT allowed_title_fk FOREIGN KEY (headline) REFERENCES allowed_title(title) DEFERRABLE INITIALLY DEFERRED`} {
					if _, err := b.Backend.Exec(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			case "known committed callback":
				o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error {
					return db.OnCommit(ctx, b.Alias(), func(context.Context) error { return errors.New("private after-commit failure") }, false)
				}}
			case "committed unknown":
				b.commitMode = "unknown"
			case "late cancellation":
				b.cancel = cancel
			}
			out := genericCreateNativePost(t, genericCreateNativeRouter(t, o), ctx, url.Values{"title": {"new"}})
			count := genericCreateNativeCount(t, b.Backend, "forms_article")
			if mode == "deferred foreign key" {
				if out.Code != 422 || count != 0 {
					t.Fatal(out.Code, out.Body.String(), count)
				}
			} else if mode == "late cancellation" {
				if out.Code != 303 || count != 1 {
					t.Fatal(out.Code, out.Body.String(), count)
				}
			} else {
				if out.Code != 503 || count != 1 || out.Header().Get("Location") != "" {
					t.Fatal(out.Code, out.Body.String(), count)
				}
				word := "committed"
				if mode == "committed unknown" {
					word = "unknown"
				}
				if !strings.Contains(out.Body.String(), word) {
					t.Fatal(out.Body.String())
				}
			}
		})
	}
}
