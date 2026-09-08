package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	ghttp "github.com/Newton-School/gogo/core/http"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/urls"
)

func genericUpdateNativeOptions(t *testing.T) (ghttp.UpdateViewOptions, *genericCreateNativeBackend) {
	t.Helper()
	create, backend := genericCreateNativeOptions(t)
	if _, err := backend.Exec(context.Background(), `INSERT INTO forms_article (id, headline, tenant) VALUES (1, 'Current title', 'one'), (2, 'Hidden title', 'other')`); err != nil {
		t.Fatal(err)
	}
	return ghttp.UpdateViewOptions{
		TemplateViewOptions: create.TemplateViewOptions, Store: create.Store, Model: create.Model,
		Scope: create.Scope, Factory: create.Factory, Fields: create.Fields, ReadonlyFields: create.ReadonlyFields,
		ValidateWrite: create.ValidateWrite, SuccessURL: create.SuccessURL,
		Key: func(r *http.Request) (map[string]any, error) {
			return map[string]any{"id": urls.Param(r, "id")}, nil
		},
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

func genericUpdateNativeRouter(t *testing.T, options ghttp.UpdateViewOptions) http.Handler {
	t.Helper()
	h, err := ghttp.NewUpdateView(options)
	if err != nil {
		t.Fatal(err)
	}
	router, err := urls.New(urls.Path("articles/<int:id>/edit/", h, "article-update"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := router.Reverse("article-update", map[string]any{"id": int64(1)}, nil)
	if err != nil || path != "/articles/1/edit/" {
		t.Fatal(path, err)
	}
	return router
}

func genericUpdateNativeRequest(ctx context.Context, method string, id int, body string) *http.Request {
	r := httptest.NewRequest(method, fmt.Sprintf("https://example.test/articles/%d/edit/", id), strings.NewReader(body))
	if method != "POST" && body == "" {
		r.Body = http.NoBody
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://example.test")
	}
	return r.WithContext(auth.WithPrincipal(ctx, auth.Principal{ID: "one", Authenticated: true, Active: true}))
}

func genericUpdateNativeTokens(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	out := httptest.NewRecorder()
	h.ServeHTTP(out, genericUpdateNativeRequest(context.Background(), "GET", 1, ""))
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if out.Code != 200 || len(match) != 2 || len(out.Result().Cookies()) != 1 {
		t.Fatal("unbound update", out.Code, out.Body.String())
	}
	return out.Result().Cookies()[0], match[1]
}

func genericUpdateNativePost(t *testing.T, h http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	cookie, token := genericUpdateNativeTokens(t, h)
	values.Set("csrfmiddlewaretoken", token)
	req := genericUpdateNativeRequest(context.Background(), "POST", 1, values.Encode())
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}

func genericUpdateNativeTitle(t *testing.T, backend db.Backend) string {
	t.Helper()
	var title string
	if err := db.QueryRow(context.Background(), backend, `SELECT headline FROM forms_article WHERE id = 1`, nil, &title); err != nil {
		t.Fatal(err)
	}
	return title
}

func TestGenericUpdateNativeScopedHTMLAndDurableWrite(t *testing.T) {
	o, b := genericUpdateNativeOptions(t)
	o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		id, _ := event.Record.Get("id")
		_, err := db.ExecutorFor(ctx, b).Exec(ctx, `INSERT INTO create_audit (article_id,event) VALUES ($1,'update')`, id)
		return err
	}}
	h := genericUpdateNativeRouter(t, o)
	beforeBegins := b.begins
	for _, method := range []string{"GET", "HEAD"} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericUpdateNativeRequest(context.Background(), method, 1, ""))
		if out.Code != 200 || b.begins != beforeBegins || method == "HEAD" && out.Body.Len() != 0 {
			t.Fatal("safe method wrote", method, out.Code)
		}
	}
	for _, id := range []int{2, 999} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, genericUpdateNativeRequest(context.Background(), "GET", id, ""))
		if out.Code != 404 || strings.Contains(out.Body.String(), "Hidden") {
			t.Fatal("scope leak", out.Code)
		}
	}
	out := genericUpdateNativePost(t, h, url.Values{"title": {"Updated <headline>"}})
	if out.Code != 303 || out.Header().Get("Location") != "/articles/1/" || b.commits != 1 || genericUpdateNativeTitle(t, b) != "Updated <headline>" || genericCreateNativeCount(t, b, "forms_article") != 2 || genericCreateNativeCount(t, b, "create_audit") != 1 {
		t.Fatal("native update", out.Code, out.Body.String(), b.commits)
	}
}

func TestGenericUpdateNativeCallbacksAndSuppressedUpdateRollBack(t *testing.T) {
	for _, mode := range []string{"before", "after", "success", "final grant", "suppressed", "invalid callback", "unknown commit", "committed callback"} {
		t.Run(mode, func(t *testing.T) {
			o, b := genericUpdateNativeOptions(t)
			want, stored := 403, "Current title"
			mutate := func(ctx context.Context) error {
				_, err := db.ExecutorFor(ctx, b).Exec(ctx, `UPDATE forms_article SET headline='nested drift' WHERE id=1`)
				return err
			}
			switch mode {
			case "before":
				o.Store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error { return mutate(ctx) }}
			case "after":
				o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error { return mutate(ctx) }}
			case "success":
				o.SuccessURL = func(ctx context.Context, _ map[string]any) (string, error) { return "/done/", mutate(ctx) }
			case "final grant":
				calls := 0
				o.ValidateWrite = func(ctx context.Context, _ auth.Principal, _ models.Record) error {
					calls++
					if calls == 3 {
						return mutate(ctx)
					}
					return nil
				}
			case "suppressed":
				want = 409
				if _, err := b.Exec(context.Background(), `CREATE FUNCTION suppress_html_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`); err != nil {
					t.Fatal(err)
				}
				if _, err := b.Exec(context.Background(), `CREATE TRIGGER suppress_html_update BEFORE UPDATE ON forms_article FOR EACH ROW EXECUTE FUNCTION suppress_html_update()`); err != nil {
					t.Fatal(err)
				}
			case "invalid callback":
				o.Prepare = func(context.Context, models.Record) error {
					return &db.CommittedCallbackError{Errors: []error{errors.New("forged")}}
				}
				want = 503
			case "unknown commit":
				b.commitMode = "unknown"
				want, stored = 503, "Changed"
			case "committed callback":
				want, stored = 503, "Changed"
				o.Store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, _ orm.SaveEvent) error {
					return db.OnCommit(ctx, b.Alias(), func(context.Context) error { return errors.New("private callback") }, false)
				}}
			}
			h := genericUpdateNativeRouter(t, o)
			out := genericUpdateNativePost(t, h, url.Values{"title": {"Changed"}})
			if out.Code != want || genericUpdateNativeTitle(t, b) != stored || genericCreateNativeCount(t, b, "forms_article") != 2 || strings.Contains(out.Body.String(), "private") {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
}

func TestGenericUpdateNativeJSONAndTriggerNormalization(t *testing.T) {
	o, b := genericUpdateNativeOptions(t)
	schema, _ := o.Store.Registry.Get(o.Model)
	jsonField := models.JSONField("payload", models.Nullable, models.Optional)
	provider, ok := b.Backend.(interface{ SchemaEditor() db.SchemaEditor })
	if !ok {
		t.Fatal("native fixture requires a schema editor")
	}
	if err := provider.SchemaEditor().AddField(context.Background(), b, schema, jsonField); err != nil {
		t.Fatal(err)
	}
	schema.Fields = append(schema.Fields, jsonField)
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	o.Store = orm.New(b, registry)
	o.Factory = func() models.Model { record, _ := models.NewRecord(schema); return record }
	o.Fields = append(o.Fields, "payload")
	h := genericUpdateNativeRouter(t, o)
	for _, raw := range []string{`"a <string>"`, `"null"`, `900719925474099312345`, `null`, `{"key":[1,null]}`, `[]`, ``} {
		out := genericUpdateNativePost(t, h, url.Values{"title": {"Changed"}, "payload": {raw}})
		if out.Code != 303 {
			t.Fatal("JSON update", raw, out.Code, out.Body.String())
		}
		var value any
		if err := db.QueryRow(context.Background(), b, `SELECT payload::text FROM forms_article WHERE id=1`, nil, &value); err != nil {
			t.Fatal(err)
		}
		if raw == "" {
			if value != nil {
				t.Fatal("SQL NULL lost", value)
			}
			continue
		}
		text := fmt.Sprint(value)
		if bytes, ok := value.([]byte); ok {
			text = string(bytes)
		}
		var expected, actual any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		_ = decoder.Decode(&expected)
		decoder = json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		_ = decoder.Decode(&actual)
		expectedJSON, _ := json.Marshal(expected)
		actualJSON, _ := json.Marshal(actual)
		if string(expectedJSON) != string(actualJSON) {
			t.Fatal("JSON representation changed", raw, text)
		}
		get := httptest.NewRecorder()
		h.ServeHTTP(get, genericUpdateNativeRequest(context.Background(), "GET", 1, ""))
		if get.Code != 200 || strings.Contains(get.Body.String(), "a <string>") {
			t.Fatal("unsafe JSON initial", get.Code, get.Body.String())
		}
	}
	if _, err := b.Exec(context.Background(), `CREATE FUNCTION normalize_html_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.headline := upper(NEW.headline); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(context.Background(), `CREATE TRIGGER normalize_html_update BEFORE UPDATE ON forms_article FOR EACH ROW EXECUTE FUNCTION normalize_html_update()`); err != nil {
		t.Fatal(err)
	}
	seen := false
	o.ValidateWrite = func(_ context.Context, _ auth.Principal, record models.Record) error {
		title, _ := record.Get("title")
		seen = seen || title == "NORMALIZED"
		return nil
	}
	h = genericUpdateNativeRouter(t, o)
	out := genericUpdateNativePost(t, h, url.Values{"title": {"normalized"}, "payload": {"null"}})
	if out.Code != 303 || !seen || genericUpdateNativeTitle(t, b) != "NORMALIZED" {
		t.Fatal("stored trigger value not authorized", out.Code, out.Body.String(), seen)
	}
}

func TestGenericUpdateNativeLocksCurrentRowUntilCommit(t *testing.T) {
	o, observed := genericUpdateNativeOptions(t)
	// The counting fixture is deliberately not shared between concurrent
	// requests. Exercise the actual connector and its transaction-owned lock.
	o.Store = orm.New(observed.Backend, o.Store.Registry)
	entered, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var blockerID int64
	defer releaseOnce.Do(func() { close(release) })
	o.Prepare = func(ctx context.Context, record models.Record) error {
		title, _ := record.Get("title")
		if title == "first" {
			if err := db.QueryRow(ctx, db.ExecutorFor(ctx, observed.Backend), `SELECT pg_backend_pid()`, nil, &blockerID); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		close(second)
		return nil
	}
	h := genericUpdateNativeRouter(t, o)
	cookie, token := genericUpdateNativeTokens(t, h)
	done := make(chan int, 2)
	send := func(title string) {
		req := genericUpdateNativeRequest(context.Background(), "POST", 1, url.Values{"title": {title}, "csrfmiddlewaretoken": {token}}.Encode())
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		done <- out.Code
	}
	go send("first")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first update did not acquire lock")
	}
	go send("second")
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRow(probeCtx, observed.Backend, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.wait_event_type='Lock' AND $1::integer=ANY(pg_blocking_pids(a.pid)))`, []any{blockerID}, &blocked); err != nil {
			t.Fatal("observe native lock waiter", err)
		}
		if blocked {
			break
		}
		select {
		case <-second:
			t.Fatal("second request escaped row lock")
		case <-probeCtx.Done():
			t.Fatal("second request never became a native lock waiter")
		case <-ticker.C:
		}
	}
	select {
	case <-second:
		t.Fatal("blocked waiter entered preparation")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case status := <-done:
			if status != 303 {
				t.Fatal("concurrent update", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("update failed to complete")
		}
	}
	if genericUpdateNativeTitle(t, observed.Backend) != "second" {
		t.Fatal("current row update order")
	}
}

func TestGenericUpdateNativeCompositeIdentityAndReadonlyColumns(t *testing.T) {
	dated, backend := genericDateNativeOptions(t)
	schema, _ := dated.Store.Registry.Get(dated.Model)
	if _, err := backend.Exec(context.Background(), `INSERT INTO calendar_article (tenant,id,title) VALUES ('one',1,'Original'),('other',1,'Hidden')`); err != nil {
		t.Fatal(err)
	}
	options := ghttp.UpdateViewOptions{
		TemplateViewOptions: genericHTMLTemplate("composite-update.html", `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="{{ csrf_token }}">{{ form.html }}<button>Save</button></form>`),
		Store:               dated.Store, Model: dated.Model, Policy: dated.Policy, Scope: dated.Scope,
		Fields: []string{"title", "tenant", "id"}, ReadonlyFields: []string{"tenant", "id"}, Key: dated.Key,
		Factory: func() models.Model { record, _ := models.NewRecord(schema); return record },
		ValidateWrite: func(_ context.Context, p auth.Principal, record models.Record) error {
			tenant, _ := record.Get("tenant")
			id, _ := record.Get("id")
			if tenant != p.ID || id != int64(1) {
				return auth.ErrPermissionDenied
			}
			return nil
		},
		SuccessURL: func(_ context.Context, identity map[string]any) (string, error) {
			if len(identity) != 2 || identity["tenant"] != "one" || identity["id"] != int64(1) {
				return "", errors.New("unexpected composite identity")
			}
			identity["tenant"] = "discarded callback mutation"
			return "/articles/1/", nil
		},
	}
	h := genericUpdateNativeRouter(t, options)
	out := genericUpdateNativePost(t, h, url.Values{"title": {"Updated"}})
	if out.Code != 303 {
		t.Fatal("composite update", out.Code, out.Body.String())
	}
	var title, hidden string
	if err := db.QueryRow(context.Background(), backend, `SELECT title FROM calendar_article WHERE tenant='one' AND id=1`, nil, &title); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), backend, `SELECT title FROM calendar_article WHERE tenant='other' AND id=1`, nil, &hidden); err != nil {
		t.Fatal(err)
	}
	if title != "Updated" || hidden != "Hidden" {
		t.Fatal("composite scope or identity widened", title, hidden)
	}
	options.Prepare = func(_ context.Context, record models.Record) error { return record.Set("tenant", "other") }
	h = genericUpdateNativeRouter(t, options)
	out = genericUpdateNativePost(t, h, url.Values{"title": {"Retarget"}})
	if out.Code != 503 {
		t.Fatal("composite key retarget was accepted", out.Code, out.Body.String())
	}
	if err := db.QueryRow(context.Background(), backend, `SELECT title FROM calendar_article WHERE tenant='one' AND id=1`, nil, &title); err != nil || title != "Updated" {
		t.Fatal("rejected key change persisted", title, err)
	}
}
