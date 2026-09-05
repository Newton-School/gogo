package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func TestMigrationApplyPreviewReverseAndChecksums(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&product{}).Schema()
	initial := migrations.Migration{App: "tests", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
	note := models.TextField("note", models.Nullable)
	second := migrations.Migration{App: "tests", Name: "0002_note", Dependencies: []string{initial.Key()}, Operations: []migrations.Operation{migrations.AddField(schema, note)}}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{second, initial}}
	history, err := e.History(ctx)
	if err != nil || len(history) != 0 {
		t.Fatal(history, err)
	}
	preview, err := e.SQL(ctx, initial.Key(), false)
	if err != nil || len(preview) != 1 || !strings.Contains(preview[0].SQL, "CREATE TABLE") {
		t.Fatal(preview, err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 0 {
		t.Fatal("preview caused DDL", tables, err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, ""); err != nil {
		t.Fatal("idempotent apply", err)
	}
	history, err = e.History(ctx)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	actual, err := b.Introspector().Introspect(ctx, b, schema.DBTable())
	if err != nil || len(actual.Fields) != 5 {
		t.Fatal(actual, err)
	}
	mutated := second
	mutated.Operations = []migrations.Operation{migrations.RunSQL("SELECT 1", nil, "SELECT 1", nil)}
	e.Migrations = []migrations.Migration{initial, mutated}
	if err := e.Apply(ctx, ""); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("source mutation accepted", err)
	}
	e.Migrations = []migrations.Migration{initial, second}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	actual, err = b.Introspector().Introspect(ctx, b, schema.DBTable())
	if err != nil || len(actual.Fields) != 4 {
		t.Fatal(actual, err)
	}
	if err := e.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal(err)
	}
	history, err = e.History(ctx)
	if err != nil || len(history) != 0 {
		t.Fatal(history, err)
	}
	tables, err = b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 1 || tables[0] != "gogo_migrations" {
		t.Fatal(tables, err)
	}
}

func TestMigrationAtomicFailureAndLockCancellation(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001_fail", Operations: []migrations.Operation{migrations.CreateModel((&product{}).Schema()), migrations.RunSQL("SELECT 1 / 0", nil, "", nil)}}}}
	if err := e.Apply(ctx, ""); err == nil {
		t.Fatal("failure ignored")
	}
	history, err := e.History(ctx)
	if err != nil || len(history) != 0 {
		t.Fatal("failed migration recorded", history, err)
	}
	tables, err := b.Introspector().Tables(ctx, b)
	if err != nil || len(tables) != 1 || tables[0] != "gogo_migrations" {
		t.Fatal("failed migration left DDL", tables, err)
	}
	unlock, err := b.LockMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if other, err := b.LockMigrations(deadline); err == nil {
		other()
		t.Fatal("second session acquired held lock")
	}
	var count int
	if err := db.QueryRow(ctx, b, "SELECT count(*) FROM gogo_migrations", nil, &count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
