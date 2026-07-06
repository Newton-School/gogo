package admin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Newton-School/gogo/orm"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func TestSQLLogStorePersistsEntriesAndEnsuresSchema(t *testing.T) {
	ctx := context.Background()
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     filepath.Join(t.TempDir(), "admin-log.sqlite3"),
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()

	now := time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)
	store := NewSQLLogStore(database)
	store.Now = func() time.Time { return now }
	if err := store.Log(AdminLogEntry{
		UserID:        7,
		ContentType:   "blog.Post",
		ObjectID:      "42",
		ObjectRepr:    "First post",
		ActionFlag:    ActionFlagAddition,
		ChangeMessage: "Added post",
	}); err != nil {
		t.Fatalf("Log(addition) error = %v", err)
	}
	store.Now = func() time.Time { return now.Add(time.Minute) }
	if err := store.Log(AdminLogEntry{
		UserID:        7,
		ContentType:   "blog.Post",
		ObjectID:      "42",
		ObjectRepr:    "First post",
		ActionFlag:    ActionFlagChange,
		ChangeMessage: "Changed title",
	}); err != nil {
		t.Fatalf("Log(change) error = %v", err)
	}

	entries, err := store.EntriesForObject("blog.Post", "42")
	if err != nil {
		t.Fatalf("EntriesForObject() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].ActionFlag != ActionFlagAddition || !entries[0].ActionTime.Equal(now) || entries[1].ChangeMessage != "Changed title" {
		t.Fatalf("entries content = %#v", entries)
	}
	if other, err := store.EntriesForObject("blog.Post", "missing"); err != nil || len(other) != 0 {
		t.Fatalf("missing entries = %#v err=%v", other, err)
	}
}
