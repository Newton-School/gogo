package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type product struct {
	models.Base
	ID         int64
	Name       string
	Amount     string
	Created    time.Time
	CleanCalls int
}

func (p *product) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "Product", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.TextField("name", models.WithStructField("Name")), models.DecimalField("amount", 10, 2, models.WithStructField("Amount")), models.DateTimeField("created", models.WithStructField("Created"), func(f *models.Field) { f.AutoNowAdd = true })}}
}
func (p *product) Clean(context.Context) error { p.CleanCalls++; return nil }
func openTest(t *testing.T) *postgres.Backend {
	t.Helper()
	dsn := os.Getenv("GOGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOGO_TEST_POSTGRES_DSN not set; real PostgreSQL integration not run")
	}
	backend, err := postgres.Open(context.Background(), postgres.Config{DSN: dsn, MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	return backend
}
func setupProducts(t *testing.T) (*postgres.Backend, *orm.Store) {
	t.Helper()
	backend := openTest(t)
	ctx := context.Background()
	if _, err := backend.Exec(ctx, `DROP TABLE IF EXISTS tests_product`); err != nil {
		t.Fatal(err)
	}
	if err := backend.SchemaEditor().CreateModel(ctx, backend, (&product{}).Schema()); err != nil {
		t.Fatal(err)
	}
	return backend, orm.New(backend, nil)
}
func TestSaveDjangoRegression(t *testing.T) {
	backend, store := setupProducts(t)
	ctx := context.Background()
	before, after := 0, 0
	store.BeforeSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { before++; return nil }}
	store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { after++; return nil }}
	p := &product{Name: "", Amount: "123.45"}
	if err := store.Save(ctx, p, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if p.CleanCalls != 0 || p.ID == 0 || p.Created.IsZero() || p.ModelState().Adding() {
		t.Fatalf("bad saved state %#v", p)
	}
	record, _ := models.Bind(p)
	if err := models.FullClean(ctx, record, models.CleanOptions{}, nil); err == nil {
		t.Fatal("explicit FullClean accepted blank name")
	}
	if err := store.Save(ctx, p, orm.SaveOptions{UpdateFields: []string{}}); err != nil {
		t.Fatal(err)
	}
	if before != 1 || after != 1 {
		t.Fatal("empty update_fields sent signals")
	}
	fallback := &product{ID: 500, Name: "fallback", Amount: "8.25"}
	if err := store.Save(ctx, fallback, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if fallback.Created.IsZero() {
		t.Fatal("fallback skipped insert pre_save")
	}
	missing := &product{ID: 501, Name: "absent", Amount: "1.00"}
	if err := store.Save(ctx, missing, orm.SaveOptions{ForceUpdate: true}); !errors.Is(err, orm.ErrNotUpdated) {
		t.Fatalf("forced missing row: %v", err)
	}
	if err := store.Save(ctx, &product{Name: "unset", Amount: "1.00"}, orm.SaveOptions{ForceUpdate: true}); err == nil {
		t.Fatal("forced update without PK inserted")
	}
	p.Name = "changed"
	if err := store.Save(ctx, p, orm.SaveOptions{UpdateFields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := orm.For(store, func() *product { return &product{} }).Filter(orm.Q("id", p.ID)).Get(ctx)
	if err != nil || loaded.Name != "changed" || loaded.Amount != "123.45" {
		t.Fatalf("load=%#v err=%v", loaded, err)
	}
	count, err := orm.For(store, func() *product { return &product{} }).Count(ctx)
	if err != nil || count != 2 {
		t.Fatalf("count=%d %v", count, err)
	}
	_ = backend
}
func TestTransactionsAndSignalFailure(t *testing.T) {
	backend, store := setupProducts(t)
	ctx := context.Background()
	failure := errors.New("receiver failed")
	store.AfterSave = []orm.SaveReceiver{func(context.Context, orm.SaveEvent) error { return failure }}
	outside := &product{Name: "outside", Amount: "1.00"}
	if err := store.Save(ctx, outside, orm.SaveOptions{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if count, _ := orm.For(store, func() *product { return &product{} }).Count(ctx); count != 1 {
		t.Fatal("autocommit row rolled back on post-save error")
	}
	inside := &product{Name: "inside", Amount: "2.00"}
	err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(txCtx context.Context) error { return store.Save(txCtx, inside, orm.SaveOptions{}) })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if count, _ := orm.For(store, func() *product { return &product{} }).Count(ctx); count != 1 {
		t.Fatal("caller Atomic failed to rollback")
	}
	store.AfterSave = nil
	calls := []string{}
	err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(txCtx context.Context) error {
		if err := db.OnCommit(txCtx, backend.Alias(), func(context.Context) error { calls = append(calls, "outer"); return nil }, false); err != nil {
			return err
		}
		nestedErr := db.Atomic(txCtx, backend, db.AtomicOptions{}, func(inner context.Context) error {
			db.OnCommit(inner, backend.Alias(), func(context.Context) error { calls = append(calls, "discard"); return nil }, false)
			return errors.New("rollback nested")
		})
		if nestedErr == nil {
			return errors.New("nested failure lost")
		}
		if len(calls) != 0 {
			return errors.New("callback ran before commit")
		}
		return store.Save(txCtx, &product{Name: "after-savepoint", Amount: "3.00"}, orm.SaveOptions{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "outer" {
		t.Fatalf("callbacks=%v", calls)
	}
	err = db.Atomic(ctx, backend, db.AtomicOptions{}, func(txCtx context.Context) error {
		return db.OnCommit(txCtx, backend.Alias(), func(context.Context) error { return failure }, false)
	})
	var committed *db.CommittedCallbackError
	if !errors.As(err, &committed) {
		t.Fatalf("expected postcommit classification: %v", err)
	}
}
func TestQueriesBindUntrustedValues(t *testing.T) {
	_, store := setupProducts(t)
	ctx := context.Background()
	for _, name := range []string{"x%_", "safe", "Robert'); DROP TABLE tests_product;--"} {
		if err := store.Save(ctx, &product{Name: name, Amount: "1.00"}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	base := orm.For(store, func() *product { return &product{} })
	a := base.Filter(orm.Q("name__contains", "%_"))
	b := base.Filter(orm.Q("name", "safe"))
	if rows, err := a.All(ctx); err != nil || len(rows) != 1 || rows[0].Name != "x%_" {
		t.Fatalf("literal LIKE mismatch: %#v %v", rows, err)
	}
	if count, err := b.Count(ctx); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if count, err := base.Count(ctx); err != nil || count != 3 {
		t.Fatal("query clone mutated base", count, err)
	}
	if _, _, err := base.OrderBy("name;drop table tests_product").SQL(); err == nil {
		t.Fatal("untrusted identifier accepted")
	}
	if _, err := base.Get(ctx); !errors.Is(err, orm.ErrMultipleObjects) {
		t.Fatal(err)
	}
	if _, err := base.Filter(orm.Q("id__in", []int64{})).Get(ctx); !errors.Is(err, orm.ErrNotFound) {
		t.Fatal(err)
	}
	p, err := base.Filter(orm.Q("name", "safe")).Only("name").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := models.Bind(p)
	if _, err := r.Get("amount"); err == nil {
		t.Fatal("deferred field exposed as zero")
	}
	if err := store.RefreshFromDB(ctx, p, "amount"); err != nil {
		t.Fatal(err)
	}
	if p.Amount != "1.00" {
		t.Fatal(p.Amount)
	}
}
func TestConnectionGuardsAndIntrospection(t *testing.T) {
	backend, _ := setupProducts(t)
	if _, err := postgres.Open(context.Background(), postgres.Config{DSN: "postgres://user:secret@localhost/test?sslmode=disable", Production: true}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("TLS guard missing or secret leaked")
	}
	schema, err := backend.Introspector().Introspect(context.Background(), backend, "tests_product")
	if err != nil || len(schema.Fields) != 4 || len(schema.PKFields()) != 1 {
		t.Fatalf("schema=%#v %v", schema, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.Query(ctx, "SELECT 1"); !db.IsCode(err, db.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
