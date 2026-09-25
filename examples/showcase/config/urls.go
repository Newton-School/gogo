package config

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"example.com/gogo-showcase/apps/catalog"
	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/admin"
	connector "github.com/Newton-School/gogo/connectors/redis"
	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/app"
	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/cache"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/messages"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
	"github.com/Newton-School/gogo/core/urls"
)

func (c *Connections) Handler(_ *app.Registry, settings conf.Values) (http.Handler, error) {
	production := settings.String("GOGO_ENV") == "production"
	key := []byte(settings.Secret("GOGO_SECRET_KEY").Reveal())
	signer, err := security.NewSigner(security.SigningKey{ID: "primary", Value: key}, nil, "showcase-admin")
	if err != nil {
		return nil, err
	}
	adapter, err := c.accountStore()
	if err != nil {
		return nil, err
	}
	site, err := admin.NewSite(admin.Config{
		Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}, Header: "Gogo showcase", SiteURL: "/",
		LoginURL: "/admin/login/", LogoutURL: "/admin/logout/", PasswordChangeURL: "/admin/password-change/", Messages: true,
		CSRF: security.CSRFConfig{Secure: production, MaxBodyBytes: settings.Int("GOGO_MAX_BODY_BYTES")},
	})
	if err != nil {
		return nil, err
	}
	if err := catalog.RegisterAdmin(site, c.Store); err != nil {
		return nil, err
	}
	if err := site.Register(adapter.UserAdmin()); err != nil {
		return nil, err
	}
	if err := site.Register(adapter.GroupAdmin()); err != nil {
		return nil, err
	}
	if err := registerFieldAdmin(site); err != nil {
		return nil, err
	}
	accounts := adapter.Accounts()
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		return nil, err
	}
	limiter := &connector.Limiter{Connection: c.Sessions}
	login, err := site.LoginHandler(authviews.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: limiter, RateSecret: key})
	if err != nil {
		return nil, err
	}
	logout, err := site.LogoutHandler(authviews.LogoutConfig{})
	if err != nil {
		return nil, err
	}
	password, err := site.PasswordChangeHandler(authviews.PasswordChangeConfig{Changer: accounts, Limiter: limiter, RateSecret: key, PreserveSession: true})
	if err != nil {
		return nil, err
	}
	routes, err := catalog.Routes(c.Store)
	if err != nil {
		return nil, err
	}
	router, err := urls.New(urls.Include("api/v1/", "api", routes...))
	if err != nil {
		return nil, err
	}
	document, err := api.OpenAPI(context.Background(), router, api.OpenAPIOptions{Title: "Gogo showcase public catalog", Version: "1.0.0-alpha.2"})
	if err != nil {
		return nil, err
	}
	csrf, err := security.CSRF(security.CSRFConfig{Secure: production, MaxBodyBytes: 1 << 20})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	dashboard, err := c.Dashboard()
	if err != nil {
		return nil, err
	}
	mux.Handle("/async/", dashboard)
	mux.HandleFunc("/", catalog.Index)
	mux.HandleFunc("/assets/showcase.css", catalog.Styles)
	mux.HandleFunc("/fields/", catalog.Fields)
	mux.Handle("/forms/", csrf(http.HandlerFunc(catalog.Form)))
	mux.Handle("/api/v1/", router)
	mux.HandleFunc("GET /api/schema/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(document)
	})
	mux.Handle("/admin/login/", login)
	mux.Handle("/admin/logout/", logout)
	mux.Handle("/admin/password-change/", password)
	mux.Handle("/admin/", site)
	mux.HandleFunc("GET /health/live/", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if c.Database.Ping(ctx) != nil || c.Sessions.Ping(ctx) != nil {
			writeJSON(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	readCache := &cache.Cache{Store: &connector.Cache{Connection: c.Cache}, TTL: 15 * time.Second}
	mux.HandleFunc("GET /cache-demo/", func(w http.ResponseWriter, r *http.Request) {
		data, err := readCache.GetOrSet(r.Context(), cache.Key("showcase-clock", 1, "public"), func(context.Context) ([]byte, error) {
			return json.Marshal(map[string]any{"generated_at": time.Now().UTC(), "ttl_seconds": 15})
		})
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "cache unavailable"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	sessionSigner, err := security.NewSigner(security.SigningKey{ID: "primary", Value: key}, nil, "showcase-session")
	if err != nil {
		return nil, err
	}
	withSessions, err := sessions.Middleware(sessions.MiddlewareConfig{Store: &connector.Sessions{Connection: c.Sessions}, Signer: sessionSigner, Secure: production, TTL: settings.Duration("GOGO_SESSION_TTL"), CookieName: settings.String("GOGO_SESSION_COOKIE_NAME")})
	if err != nil {
		return nil, err
	}
	identity, err := auth.SessionMiddleware(accounts)
	if err != nil {
		return nil, err
	}
	flash, err := messages.Middleware(messages.Config{Mode: messages.Session})
	if err != nil {
		return nil, err
	}
	headers, err := SecurityHeaders(settings)
	if err != nil {
		return nil, err
	}
	return headers(http.TimeoutHandler(withSessions(identity(flash(mux))), settings.Duration("GOGO_DB_QUERY_TIMEOUT"), "Request timed out")), nil
}

func registerFieldAdmin(site *admin.Site) error {
	for _, schema := range fieldlab.Schemas() {
		options := admin.ModelAdmin{Schema: schema, ListDisplay: []string{schema.PKFields()[0].Name}, ReadonlyFields: []string{schema.PKFields()[0].Name}}
		for _, field := range schema.Fields {
			if field.Kind == models.Binary || field.Kind == models.File || field.Kind == models.Image || field.Relation != nil {
				options.ReadonlyFields = append(options.ReadonlyFields, field.Name)
			}
		}
		options.Authorize = func(_ context.Context, _ auth.Principal, action string, _ admin.Object) error {
			if action == "view" || schema.Name == "Specimen" && action == "change" {
				return nil
			}
			return auth.ErrPermissionDenied
		}
		if err := site.Register(options); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
