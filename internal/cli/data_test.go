package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func TestDumpdataCommandFiltersNaturalKeysAndDatabase(t *testing.T) {
	store := &recordingFixtureStore{
		records: []FixtureRecord{{
			Model:      "contenttypes.ContentType",
			PK:         1,
			NaturalKey: []any{"blog", "article"},
			Fields:     map[string]any{"app_label": "blog", "model": "article"},
		}},
	}
	command := NewDumpdataCommand(store)
	var stdout bytes.Buffer

	err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--format", "json",
		"--indent", "2",
		"--database", "replica",
		"--natural-primary",
		"contenttypes.ContentType",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("dumpdata error = %v", err)
	}
	if store.query.Database != "replica" || !store.query.NaturalPrimary || !reflect.DeepEqual(store.query.Labels, []string{"contenttypes.ContentType"}) {
		t.Fatalf("query = %#v", store.query)
	}
	output := stdout.String()
	if !strings.Contains(output, `"natural_key":`) || strings.Contains(output, `"pk":`) {
		t.Fatalf("dump output = %s", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("dump output should end with newline: %q", output)
	}
}

func TestFixtureSerializersJSONLXMLAndCustom(t *testing.T) {
	records := []FixtureRecord{{Model: "blog.Article", PK: 1, Fields: map[string]any{"title": "Go"}}}
	registry := NewFixtureSerializerRegistry()
	registry.Register(customFixtureSerializer{})

	for _, format := range []string{"json", "jsonl", "xml", "custom"} {
		serializer, ok := registry.Get(format)
		if !ok {
			t.Fatalf("missing serializer %s", format)
		}
		encoded, err := serializer.Dump(records, 2)
		if err != nil {
			t.Fatalf("Dump(%s) error = %v", format, err)
		}
		decoded, err := serializer.Load(encoded)
		if err != nil {
			t.Fatalf("Load(%s) error = %v; encoded=%s", format, err, encoded)
		}
		if len(decoded) != 1 || decoded[0].Model != "blog.Article" {
			t.Fatalf("decoded(%s) = %#v", format, decoded)
		}
	}
}

func TestLoaddataCommandRejectsDuplicateFixtures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.jsonl")
	content := `{"model":"blog.Article","pk":1,"fields":{"title":"A"}}` + "\n" +
		`{"model":"blog.Article","pk":1,"fields":{"title":"B"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &recordingFixtureStore{}
	command := NewLoaddataCommand(store)
	err := command.Run(context.Background(), []string{"--format", "jsonl", "--database", "replica", path})
	if !errors.Is(err, ErrInvalidFixture) {
		t.Fatalf("loaddata duplicate error = %v, want ErrInvalidFixture", err)
	}
	if len(store.loaded) != 0 {
		t.Fatalf("store loaded duplicate records: %#v", store.loaded)
	}
}

func TestLoaddataCommandLoadsWithTransaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	content := `[{"model":"auth.Permission","natural_key":["blog","article","view"],"fields":{"codename":"view_article"}}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &recordingFixtureStore{}
	command := NewLoaddataCommand(store)
	if err := command.Run(context.Background(), []string{"--format", "json", "--database", "default", "--transaction", path}); err != nil {
		t.Fatalf("loaddata error = %v", err)
	}
	if store.loadOptions.Database != "default" || !store.loadOptions.Transaction {
		t.Fatalf("load options = %#v", store.loadOptions)
	}
	if len(store.loaded) != 1 || store.loaded[0].Model != "auth.Permission" {
		t.Fatalf("loaded = %#v", store.loaded)
	}
}

func TestLoaddataCommandReportsLoadedObjectCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	content := `[{"model":"blog.Post","pk":1,"fields":{"title":"Loaded"}}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &recordingFixtureStore{}
	command := NewLoaddataCommand(store)
	var stdout bytes.Buffer
	if err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{"--format", "json", path}, &stdout, io.Discard); err != nil {
		t.Fatalf("loaddata error = %v", err)
	}
	if stdout.String() != "loaded 1 object(s) from 1 fixture(s)\n" {
		t.Fatalf("loaddata stdout = %q", stdout.String())
	}
}

func TestRootDataCommandsShareDefaultFixtureStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.json")
	content := `[{"model":"blog.Post","pk":1,"fields":{"title":"Loaded"}}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	root := NewRoot()
	if err := root.Execute(context.Background(), []string{"loaddata", "--format", "json", path}, io.Discard, io.Discard); err != nil {
		t.Fatalf("loaddata error = %v", err)
	}
	var stdout bytes.Buffer
	if err := root.Execute(context.Background(), []string{"dumpdata", "--format", "json", "blog.Post"}, &stdout, io.Discard); err != nil {
		t.Fatalf("dumpdata error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"model":"blog.Post"`) || !strings.Contains(stdout.String(), `"title":"Loaded"`) {
		t.Fatalf("dumpdata output = %s", stdout.String())
	}
}

func TestMetadataFixtureStorePersistsToDatabase(t *testing.T) {
	ctx := context.Background()
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{Name: orm.DefaultDatabase, Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "fixtures.sqlite3"), Dialect: sqlitedialect.New()})
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer database.Close()
	if _, err := database.SQLDB().ExecContext(ctx, `CREATE TABLE blog_post (id bigint PRIMARY KEY, title text NOT NULL, slug text NOT NULL, created_at timestamp, updated_at timestamp)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	store := NewMetadataFixtureStore(orm.NewMetadataStore(database, fixturePostMeta()))
	if err := store.Load(ctx, []FixtureRecord{{Model: "blog.Post", PK: int64(1), Fields: map[string]any{"title": "Loaded", "slug": "loaded"}}}, FixtureLoadOptions{}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	records, err := store.Dump(ctx, FixtureQuery{Labels: []string{"blog.Post"}})
	if err != nil {
		t.Fatalf("Dump() error = %v", err)
	}
	if len(records) != 1 || records[0].Model != "blog.Post" || records[0].PK != int64(1) || records[0].Fields["title"] != "Loaded" {
		t.Fatalf("records = %#v", records)
	}
}

func fixturePostMeta() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", Column: "id", PrimaryKey: true},
			{Name: "title", Column: "title"},
			{Name: "slug", Column: "slug"},
			{Name: "created_at", Column: "created_at"},
			{Name: "updated_at", Column: "updated_at"},
		},
	}
}

type recordingFixtureStore struct {
	query       FixtureQuery
	records     []FixtureRecord
	loaded      []FixtureRecord
	loadOptions FixtureLoadOptions
}

func (s *recordingFixtureStore) Dump(_ context.Context, query FixtureQuery) ([]FixtureRecord, error) {
	s.query = query
	return append([]FixtureRecord(nil), s.records...), nil
}

func (s *recordingFixtureStore) Load(_ context.Context, records []FixtureRecord, options FixtureLoadOptions) error {
	s.loaded = append([]FixtureRecord(nil), records...)
	s.loadOptions = options
	return nil
}

type customFixtureSerializer struct{}

func (customFixtureSerializer) Format() string { return "custom" }

func (customFixtureSerializer) Dump(records []FixtureRecord, _ int) ([]byte, error) {
	return []byte("custom:" + records[0].Model), nil
}

func (customFixtureSerializer) Load(data []byte) ([]FixtureRecord, error) {
	parts := strings.SplitN(string(data), ":", 2)
	return []FixtureRecord{{Model: parts[1], Fields: map[string]any{}}}, nil
}
