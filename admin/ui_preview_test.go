package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/sessions"
)

// Explicitly opted-in, loopback-only visual fixture. No real accounts, network
// providers, or persistent records are used. Never included in client binaries.
// GOGO_ADMIN_UI_PREVIEW=1 go test ./admin -run '^TestAdminUIPreview$' -v -timeout 20m
func TestAdminUIPreview(t *testing.T) {
	if os.Getenv("GOGO_ADMIN_UI_PREVIEW") != "1" {
		t.Skip("manual browser fixture")
	}
	site := newPresentationTestSite(t)
	rateSecret := make([]byte, 32)
	if _, err := rand.Read(rateSecret); err != nil {
		t.Fatal(err)
	}
	login, err := site.LoginHandler(authviews.LoginConfig{
		Authenticator: credentialFunc(func(context.Context, string, string) (auth.Principal, error) {
			return auth.Principal{}, auth.ErrCredentials // Fixture rejects every credential.
		}),
		NormalizeIdentifier: func(value string) (string, error) { return value, nil },
		Limiter:             loginTestLimiter{}, RateSecret: rateSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPresentationPreview(t, site, login)
}

func newPresentationTestSite(t *testing.T) *Site {
	t.Helper()
	base, database := newTestSite(t)
	base.config.Store = relationStore{database}
	site, err := NewSite(base.config)
	if err != nil {
		t.Fatal(err)
	}
	options := base.models["shop.Product"]
	options.Schema.LabelPlural = "Products"
	options.Fields = nil
	options.Fieldsets = []Fieldset{
		{Name: "Product", Fields: []string{"Name"}},
		{Name: "Internal", Fields: []string{"Secret"}, Classes: []string{"collapse"}},
	}
	name := forms.NewField("Name", forms.Char)
	name.HelpText = "The product name shown in the catalog."
	options.FormOverrides = map[string]forms.Field{"Name": name}
	options.Actions = []Action{{Name: "review", Description: "Review selected products", Permission: "change", Confirm: true,
		Run: func(context.Context, ScopedStore, []Object) error { return nil },
	}}
	if err := site.Register(options); err != nil {
		t.Fatal(err)
	}
	if err := site.Register(ModelAdmin{Schema: (&relationSource{}).Schema(), Fields: []string{"parent"}, AutocompleteFields: []string{"parent"}, ResolveRelation: func(_ context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 || ids[0] != "1" {
			return nil, auth.ErrPermissionDenied
		}
		return []any{int64(1)}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestPresentationFixtureRoutes(t *testing.T) {
	for _, path := range []string{"/admin/", "/admin/shop/product/", "/admin/shop/product/add/", "/admin/shop/product/1/change/", "/admin/shop/product/1/history/", "/admin/shop/product/1/delete/"} {
		page := perform(newPresentationTestSite(t), "GET", path, principal(), nil, nil)
		if page.Code != 200 {
			t.Fatal(path, page.Code, page.Body.String())
		}
	}
}

func runPresentationPreview(t *testing.T, site *Site, login http.Handler) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:8092")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// The shared test store is intentionally serialized, including reads.
	var requests sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != listener.Addr().String() {
			http.Error(w, "Invalid host", http.StatusBadRequest)
			return
		}
		requests.Lock()
		defer requests.Unlock()
		if r.URL.Path == "/preview/widgets/" {
			renderPresentationGallery(t, site, w, r)
			return
		}
		if r.URL.Path == "/preview/login/" {
			// Only the credential-error flow is exposed; each request gets a
			// disposable session and no submitted identity can authenticate.
			login.ServeHTTP(w, r.WithContext(sessions.WithSession(r.Context(), sessions.New(sessions.Record{}))))
			return
		}
		site.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal())))
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	t.Log("Synthetic Admin review: http://127.0.0.1:8092/admin/; login: /preview/login/")
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
