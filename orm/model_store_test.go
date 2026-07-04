package orm

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cybersaksham/gogo/models"
	sqlitedialect "github.com/cybersaksham/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func TestMetadataStoreCRUDDumpAndLoad(t *testing.T) {
	ctx := context.Background()
	database, err := OpenDatabase(ctx, DatabaseConfig{
		Name:    DefaultDatabase,
		Driver:  "sqlite",
		DSN:     filepath.Join(t.TempDir(), "db.sqlite3"),
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()

	_, err = database.SQLDB().ExecContext(ctx, `CREATE TABLE notes_item (id bigint PRIMARY KEY, name text NOT NULL, slug text NOT NULL, created_at timestamp, updated_at timestamp)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	store := NewMetadataStore(database, notesItemMeta())
	created, err := store.Create(ctx, notesItemMeta(), map[string]any{"name": "First", "slug": "first"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created["id"] != int64(1) || created["name"] != "First" || created["slug"] != "first" || created["created_at"] == nil || created["updated_at"] == nil {
		t.Fatalf("created row = %#v", created)
	}

	list, err := store.List(ctx, notesItemMeta())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 || !reflect.DeepEqual(list[0]["name"], "First") {
		t.Fatalf("list = %#v", list)
	}

	got, ok, err := store.Get(ctx, notesItemMeta(), "1")
	if err != nil || !ok {
		t.Fatalf("Get() = %#v, %v, %v", got, ok, err)
	}
	if got["slug"] != "first" {
		t.Fatalf("got row = %#v", got)
	}

	updated, err := store.Update(ctx, notesItemMeta(), "1", map[string]any{"name": "Updated"}, true)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated["name"] != "Updated" || updated["slug"] != "first" {
		t.Fatalf("updated row = %#v", updated)
	}

	dump, err := store.Dump(ctx, []string{"notes.Item"})
	if err != nil {
		t.Fatalf("Dump() error = %v", err)
	}
	if len(dump) != 1 || dump[0].Model != "notes.Item" || dump[0].PK != int64(1) || dump[0].Fields["name"] != "Updated" {
		t.Fatalf("dump = %#v", dump)
	}

	if err := store.Delete(ctx, notesItemMeta(), "1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok, err := store.Get(ctx, notesItemMeta(), "1"); err != nil || ok {
		t.Fatalf("Get(deleted) ok=%v err=%v", ok, err)
	}

	if err := store.Load(ctx, []MetadataFixtureRecord{{
		Model:  "notes.Item",
		PK:     int64(7),
		Fields: map[string]any{"name": "Loaded", "slug": "loaded"},
	}}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loaded, ok, err := store.Get(ctx, notesItemMeta(), "7")
	if err != nil || !ok {
		t.Fatalf("Get(loaded) = %#v, %v, %v", loaded, ok, err)
	}
	if loaded["name"] != "Loaded" || loaded["slug"] != "loaded" {
		t.Fatalf("loaded row = %#v", loaded)
	}
}

func TestMetadataStoreCreateUsesDatabaseGeneratedPrimaryKey(t *testing.T) {
	ctx := context.Background()
	database, err := OpenDatabase(ctx, DatabaseConfig{
		Name:    DefaultDatabase,
		Driver:  "sqlite",
		DSN:     filepath.Join(t.TempDir(), "generated.sqlite3"),
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()

	_, err = database.SQLDB().ExecContext(ctx, `
		CREATE TABLE generated_item (
			id text PRIMARY KEY DEFAULT 'generated-id',
			name text NOT NULL,
			created_at timestamp,
			updated_at timestamp
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	meta := generatedItemMeta()
	store := NewMetadataStore(database, meta)
	created, err := store.Create(ctx, meta, map[string]any{"name": "First"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created["id"] != "generated-id" || created["name"] != "First" {
		t.Fatalf("created row = %#v", created)
	}
}

func notesItemMeta() models.Metadata {
	return models.Metadata{
		AppLabel:    "notes",
		ModelName:   "Item",
		TableName:   "notes_item",
		DBTable:     "notes_item",
		VerboseName: "item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "name", Column: "name"},
			{Name: "slug", Column: "slug"},
			{Name: "created_at", Column: "created_at"},
			{Name: "updated_at", Column: "updated_at"},
		},
	}
}

func generatedItemMeta() models.Metadata {
	return models.Metadata{
		AppLabel:  "generated",
		ModelName: "Item",
		TableName: "generated_item",
		DBTable:   "generated_item",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", Kind: "text", PrimaryKey: true, DBDefault: models.DefaultValue("generated-id")},
			{Name: "name", Column: "name", Kind: "text"},
			{Name: "created_at", Column: "created_at", Kind: "timestamp", Null: true},
			{Name: "updated_at", Column: "updated_at", Kind: "timestamp", Null: true},
		},
	}
}
