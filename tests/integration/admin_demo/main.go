// Command admin_demo is a loopback-only UI review fixture. It deliberately uses
// a synthetic staff identity and refuses any database outside the test cluster.
// Application projects must use the normal authentication/session middleware.
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
	"strings"
	"syscall"
	"time"

	"github.com/Newton-School/gogo/admin"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/security"
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
	for _, descriptor := range []models.Schema{(&Product{}).Schema(), admin.LogSchema()} {
		if err = backend.SchemaEditor().CreateModel(ctx, backend, descriptor); err != nil {
			return err
		}
	}
	store := orm.New(backend, nil)
	rows := []Product{{Name: "Workspace notebook", Description: "Lay-flat pages for ideas, planning, and everyday notes.", Price: "18.00", SKU: "NOTE-001", Stock: 120, Published: true}, {Name: "Everyday tote", Description: "A sturdy carryall for daily essentials.", Price: "24.00", SKU: "BAG-002", Stock: 42, Published: true}, {Name: "Ceramic travel mug", Description: "Keep your morning coffee close.", Price: "32.00", SKU: "MUG-003", Stock: 86, Published: true}, {Name: "Desk organizer", Description: "A clear space for focused work.", Price: "46.00", SKU: "DESK-004", Stock: 18, Published: false}, {Name: "Weekly planner", Description: "Make room for what matters this week.", Price: "22.00", SKU: "PLAN-005", Stock: 64, Published: true}, {Name: "Reading lamp", Description: "Warm light for the end of a long day.", Price: "74.00", SKU: "LAMP-006", Stock: 12, Published: false}}
	for i := range rows {
		rows[i].Tenant = "review-workspace"
		if err = store.Save(ctx, &rows[i], orm.SaveOptions{ForceInsert: true}); err != nil {
			return err
		}
	}
	adapter, err := admin.NewORMStore(admin.ORMConfig{Store: store, Factories: map[string]func() models.Model{"catalog.Product": func() models.Model { return &Product{} }}, QueryScope: func(_ context.Context, p auth.Principal, _ models.Schema) (admin.QueryScope, error) {
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
	site, err := admin.NewSite(admin.Config{Store: adapter, Signer: signer, Policy: auth.ModelPolicy{AllowSuperuser: true}, CSRF: security.CSRFConfig{MaxBodyBytes: 10 << 20}})
	if err != nil {
		return err
	}
	if err = site.Register(admin.ModelAdmin{Schema: (&Product{}).Schema(), Fields: []string{"name", "description", "price", "stock", "published", "sku", "created_at"}, ReadonlyFields: []string{"sku", "created_at"}, ListDisplay: []string{"name", "sku", "price", "stock", "published"}, ListFilter: []string{"published"}, SearchFields: []string{"name", "sku"}, Ordering: []string{"name"}, ConstraintChecker: store}); err != nil {
		return err
	}
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}})
	if err != nil {
		return err
	}
	handler := headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := auth.Principal{ID: "review-workspace", Authenticated: true, Active: true, Staff: true, Superuser: true}
		site.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), identity)))
	}))
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
