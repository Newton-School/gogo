// Command admin_demo is a loopback-only UI review fixture with persisted staff
// accounts and Redis sessions. It refuses databases outside the test cluster.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/connectors/postgres"
	connector "github.com/Newton-School/gogo/connectors/redis"
	"github.com/Newton-School/gogo/core/auth"
	authviews "github.com/Newton-School/gogo/core/auth/views"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/messages"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/sessions"
	"github.com/jackc/pgx/v5"
)

type Product struct {
	models.Base
	ID                                    int64
	Tenant, Name, Description, Price, SKU string
	Stock                                 int64
	Published                             bool
	CreatedAt                             time.Time
}

func (*Product) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "Product", LabelPlural: "Products", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly), models.CharField("name", models.WithStructField("Name"), models.WithMaxLength(120), models.WithLabel("Product name")), models.TextField("description", models.WithStructField("Description"), models.Optional, models.WithHelpText("A concise description shown to customers.")), models.DecimalField("price", 12, 2, models.WithStructField("Price"), models.WithLabel("Price")), models.CharField("sku", models.WithStructField("SKU"), models.WithMaxLength(50), models.WithLabel("SKU")), models.IntegerField("stock", models.WithStructField("Stock"), models.WithLabel("Available stock")), models.BooleanField("published", models.WithStructField("Published"), models.WithLabel("Published")), models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.WithLabel("Created at"), func(f *models.Field) { f.AutoNowAdd = true })}, Ordering: []string{"name"}}
}

type ProductNote struct {
	models.Base
	ID, ProductID int64
	Tenant, Body  string
	CreatedAt     time.Time
}

func (*ProductNote) Schema() models.Schema {
	return models.Schema{AppLabel: "catalog", Name: "ProductNote", LabelPlural: "Product notes", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.CharField("tenant", models.WithStructField("Tenant"), models.WithMaxLength(128), models.ReadOnly), models.ForeignKeyField("product", models.Relation{Target: "catalog.Product", OnDelete: models.Cascade}, models.WithStructField("ProductID")), models.TextField("body", models.WithStructField("Body"), models.WithLabel("Note"), models.WithHelpText("Internal note stored together with the product.")), models.DateTimeField("created_at", models.WithStructField("CreatedAt"), models.WithLabel("Created at"), func(f *models.Field) { f.AutoNowAdd = true })}}
}

func main() {
	if err := run(); err != nil {
		log.Fatal("Admin review fixture could not start: ", err)
	}
}
func run() error {
	dsn := os.Getenv("GOGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("GOGO_TEST_POSTGRES_DSN is required")
	}
	redisURL, password := os.Getenv("GOGO_ADMIN_DEMO_REDIS_URL"), os.Getenv("GOGO_ADMIN_DEMO_PASSWORD")
	if redisURL == "" || password == "" {
		return errors.New("GOGO_ADMIN_DEMO_REDIS_URL and GOGO_ADMIN_DEMO_PASSWORD are required")
	}
	identifier := os.Getenv("GOGO_ADMIN_DEMO_USERNAME")
	if identifier == "" {
		identifier = "staff-reviewer"
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil || parsed.User != "gogo_test" || !(strings.HasPrefix(parsed.Host, "/") || parsed.Host == "127.0.0.1" || parsed.Host == "::1") {
		return errors.New("the isolated local gogo_test cluster is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	owner, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		return err
	}
	defer owner.Close()
	var directory, role string
	if err = db.QueryRow(ctx, owner, "SELECT current_setting('data_directory'), current_user", nil, &directory, &role); err != nil {
		return err
	}
	if role != "gogo_test" || !strings.HasPrefix(filepath.Base(filepath.Dir(directory)), "gogo-postgres.") {
		return errors.New("refusing fixture DDL outside the temporary test cluster")
	}
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	schema := "gogo_admin_ui_" + hex.EncodeToString(random[:])
	quoted, err := owner.Dialect().QuoteIdentifier(schema)
	if err != nil {
		return err
	}
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := owner.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			log.Print("Could not remove owned UI fixture schema")
		}
	}()
	backend, err := postgres.Open(ctx, postgres.Config{DSN: dsn, SearchPath: schema, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		return err
	}
	defer backend.Close()
	descriptors := []models.Schema{(&Product{}).Schema(), (&ProductNote{}).Schema(), admin.LogSchema()}
	registry := &models.Registry{}
	for _, descriptor := range append(append([]models.Schema{}, descriptors...), append(auth.Schemas(), (&contenttypes.ContentType{}).Schema())...) {
		if err = registry.Register(descriptor); err != nil {
			return err
		}
	}
	if err = registry.Freeze(); err != nil {
		return err
	}
	editor, err := backend.SchemaEditor().(db.SchemaResolverEditor).WithSchemas(registry.All())
	if err != nil {
		return err
	}
	for _, descriptor := range descriptors {
		if err = editor.CreateModel(ctx, backend, descriptor); err != nil {
			return err
		}
	}
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(contenttypes.Migrations(), auth.Migrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		return err
	}
	store := orm.New(backend, registry)
	if _, err := contenttypes.Sync(ctx, store, registry, nil); err != nil {
		return err
	}
	if err := auth.SyncPermissions(ctx, store, registry, nil); err != nil {
		return err
	}
	accounts, err := auth.NewAccounts(auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		actor := auth.FromContext(ctx)
		if change.Action == "change_own_password" && actor.Authenticated && actor.Active && actor.ID == change.UserID {
			return nil
		}
		if auth.FromContext(ctx).ID != "fixture-bootstrap" {
			return auth.ErrPermissionDenied
		}
		return nil
	}})
	if err != nil {
		return err
	}
	bootstrap := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-bootstrap", Authenticated: true, Active: true})
	staff, err := accounts.CreateUser(bootstrap, identifier, password, auth.CreateUserOptions{Staff: true})
	if err != nil {
		return err
	}
	codenames := []string{}
	for _, model := range []string{"product", "productnote"} {
		for _, action := range []string{"view", "add", "change", "delete"} {
			codenames = append(codenames, action+"_"+model)
		}
	}
	permissions, err := orm.For(store, func() *auth.Permission { return &auth.Permission{} }).Filter(orm.Q("codename__in", codenames)).All(ctx)
	if err != nil || len(permissions) != len(codenames) {
		return errors.New("fixture model permission setup failed")
	}
	grantIDs := make([]int64, len(permissions))
	for i, permission := range permissions {
		grantIDs[i] = permission.ID
	}
	if err := accounts.SetUserPermissions(bootstrap, staff.ID, grantIDs); err != nil {
		return err
	}
	rows := []Product{{Name: "Workspace notebook", Description: "Lay-flat pages for ideas, planning, and everyday notes.", Price: "18.00", SKU: "NOTE-001", Stock: 120, Published: true}, {Name: "Everyday tote", Description: "A sturdy carryall for daily essentials.", Price: "24.00", SKU: "BAG-002", Stock: 42, Published: true}, {Name: "Ceramic travel mug", Description: "Keep your morning coffee close.", Price: "32.00", SKU: "MUG-003", Stock: 86, Published: true}, {Name: "Desk organizer", Description: "A clear space for focused work.", Price: "46.00", SKU: "DESK-004", Stock: 18, Published: false}, {Name: "Weekly planner", Description: "Make room for what matters this week.", Price: "22.00", SKU: "PLAN-005", Stock: 64, Published: true}, {Name: "Reading lamp", Description: "Warm light for the end of a long day.", Price: "74.00", SKU: "LAMP-006", Stock: 12, Published: false}}
	for i := range rows {
		rows[i].Tenant = staff.ID
		if err = store.Save(ctx, &rows[i], orm.SaveOptions{ForceInsert: true}); err != nil {
			return err
		}
	}
	for _, body := range []string{"Check stock before the autumn collection launch.", "Use recycled paper for the next production run."} {
		if err = store.Save(ctx, &ProductNote{Tenant: staff.ID, ProductID: rows[0].ID, Body: body}, orm.SaveOptions{}); err != nil {
			return err
		}
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"catalog.Product": func() models.Model { return &Product{} }, "catalog.ProductNote": func() models.Model { return &ProductNote{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
		return admin.QueryScope{Predicate: orm.Q("tenant", p.ID), Identity: p.ID}, nil
	}, ValidateWrite: func(_ context.Context, p auth.Principal, r models.Record) error {
		tenant, err := r.Get("tenant")
		if err != nil || tenant != p.ID {
			return auth.ErrPermissionDenied
		}
		return nil
	}, Initialize: func(_ context.Context, p auth.Principal, r models.Record) error {
		if err := r.Set("tenant", p.ID); err != nil {
			return err
		}
		if r.Schema().Key() != "catalog.Product" {
			return nil
		}
		token, err := security.RandomToken(16)
		if err != nil {
			return err
		}
		return r.Set("sku", strings.ToUpper(token[:8]))
	}})
	if err != nil {
		return err
	}
	key, err := security.RandomToken(32)
	if err != nil {
		return err
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-review")
	if err != nil {
		return err
	}
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{}, LoginURL: "/admin/login/", LogoutURL: "/admin/logout/", PasswordChangeURL: "/admin/password-change/", Messages: true, CSRF: security.CSRFConfig{MaxBodyBytes: 10 << 20}})
	if err != nil {
		return err
	}
	if err = site.Register(admin.ModelAdmin{Schema: (&Product{}).Schema(), Fieldsets: []admin.Fieldset{{Name: "Product details", Fields: []string{"name", "description"}}, {Name: "Availability", Fields: []string{"price", "stock", "published"}}, {Name: "Record information", Fields: []string{"sku", "created_at"}, Classes: []string{"collapse"}}}, ReadonlyFields: []string{"sku", "created_at"}, ListDisplay: []string{"name", "sku", "price", "stock", "published"}, ListFilter: []string{"published"}, SearchFields: []string{"name", "sku"}, Ordering: []string{"name"}, ConstraintChecker: store, Inlines: []admin.Inline{{Name: "notes", Schema: (&ProductNote{}).Schema(), FKName: "product", Fields: []string{"body", "created_at"}, Readonly: []string{"created_at"}, Extra: 1, Maximum: 20, CanDelete: true, ConstraintChecker: store}}}); err != nil {
		return err
	}
	if err = site.Register(admin.ModelAdmin{Schema: (&ProductNote{}).Schema(), Fields: []string{"product", "body", "created_at"}, ReadonlyFields: []string{"created_at"}, AutocompleteFields: []string{"product"}, ListDisplay: []string{"body", "product", "created_at"}, SearchFields: []string{"body"}, ConstraintChecker: store, ResolveRelation: func(ctx context.Context, _ models.Field, ids []string) ([]any, error) {
		if len(ids) != 1 {
			return nil, auth.ErrPermissionDenied
		}
		id, err := strconv.ParseInt(ids[0], 10, 64)
		if err != nil {
			return nil, auth.ErrPermissionDenied
		}
		object, err := orm.For(store, func() *Product { return &Product{} }).Filter(orm.Q("id", id), orm.Q("tenant", auth.FromContext(ctx).ID)).Get(ctx)
		if err != nil {
			return nil, auth.ErrPermissionDenied
		}
		return []any{object.ID}, nil
	}}); err != nil {
		return err
	}
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}})
	if err != nil {
		return err
	}
	connection, err := connector.Open(ctx, connector.Config{URL: redisURL, Namespace: schema, Role: connector.SessionRole, Development: true})
	if err != nil {
		return err
	}
	defer connection.Close()
	sessionSigner, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(key)}, nil, "admin-review-session")
	if err != nil {
		return err
	}
	sessionMiddleware, err := sessions.Middleware(sessions.MiddlewareConfig{Store: &connector.Sessions{Connection: connection}, Signer: sessionSigner, TTL: time.Hour})
	if err != nil {
		return err
	}
	identityMiddleware, err := auth.SessionMiddleware(accounts)
	if err != nil {
		return err
	}
	flash, err := messages.Middleware(messages.Config{Mode: messages.Session})
	if err != nil {
		return err
	}
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		return err
	}
	login, err := site.LoginHandler(authviews.LoginConfig{Authenticator: authenticator, NormalizeIdentifier: accounts.NormalizeLoginIdentifier, Limiter: &connector.Limiter{Connection: connection}, RateSecret: []byte(key)})
	if err != nil {
		return err
	}
	logout, err := site.LogoutHandler(authviews.LogoutConfig{})
	if err != nil {
		return err
	}
	passwordChange, err := site.PasswordChangeHandler(authviews.PasswordChangeConfig{Changer: accounts, Limiter: &connector.Limiter{Connection: connection}, RateSecret: []byte(key), PreserveSession: true})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/admin/login/", login)
	mux.Handle("/admin/logout/", logout)
	mux.Handle("/admin/password-change/", passwordChange)
	mux.Handle("/admin/", site)
	handler := headers(sessionMiddleware(identityMiddleware(flash(mux))))
	server := &http.Server{Addr: "127.0.0.1:8099", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	fmt.Println("Local-only Admin review fixture: http://127.0.0.1:8099/admin/")
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return server.Shutdown(shutdown)
	}
}
