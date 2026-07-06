package benchmarks

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Newton-School/gogo/admin"
	gogoapi "github.com/Newton-School/gogo/api"
	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
	sqlitedialect "github.com/Newton-School/gogo/orm/dialects/sqlite"

	_ "modernc.org/sqlite"
)

func BenchmarkORMCompileSelect(b *testing.B) {
	compiler := orm.NewCompiler(postgres.New())
	query := orm.NewQuery(benchmarkModel()).
		Select("id", "title", "created_at").
		AddFilter(orm.Predicate{Field: "active", Lookup: orm.LookupExact, Value: true}).
		AddFilter(orm.Predicate{Field: "title", Lookup: orm.LookupContains, Value: "go"}).
		Order("-created_at").
		LimitTo(20).
		OffsetBy(40)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compiled, err := compiler.CompileSelect(query)
		if err != nil {
			b.Fatalf("CompileSelect() error = %v", err)
		}
		if compiled.SQL == "" {
			b.Fatal("CompileSelect() returned empty SQL")
		}
	}
}

func BenchmarkORMCompileInsert(b *testing.B) {
	compiler := orm.NewCompiler(postgres.New())
	meta := benchmarkModel()
	values := map[string]any{"title": "Gogo", "active": true, "views": 7}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compiled, err := compiler.CompileInsert(meta, values, []string{"id"})
		if err != nil {
			b.Fatalf("CompileInsert() error = %v", err)
		}
		if compiled.SQL == "" {
			b.Fatal("CompileInsert() returned empty SQL")
		}
	}
}

func BenchmarkORMScan(b *testing.B) {
	ctx := context.Background()
	database := openBenchmarkDB(b)
	defer database.Close()
	executor := orm.NewRawExecutor(database)
	query := orm.ParameterizedRaw(`SELECT id, name FROM items WHERE id > ?`, 0)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items, err := orm.RawSelect(ctx, executor, query, func(scanner orm.RowScanner) (benchmarkItem, error) {
			var item benchmarkItem
			err := scanner.Scan(&item.ID, &item.Name)
			return item, err
		})
		if err != nil {
			b.Fatalf("RawSelect() error = %v", err)
		}
		if len(items) != 128 {
			b.Fatalf("items = %d, want 128", len(items))
		}
	}
}

func BenchmarkMigrationAutodetectSmallApp(b *testing.B) {
	from, to := benchmarkMigrationStates()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		changes := migrations.NewAutodetector(from, to).Changes()
		if len(changes) == 0 {
			b.Fatal("Changes() returned no changes")
		}
	}
}

func BenchmarkAdminChangelistQueryPlanning(b *testing.B) {
	modelAdmin := admin.ModelAdmin{
		ListDisplay:       []string{"title", "published", "summary"},
		ListEditable:      []string{"published"},
		ListPerPage:       25,
		ListMaxShowAll:    200,
		DateHierarchy:     "created_at",
		EmptyValueDisplay: "(none)",
		ComputedColumns: map[string]admin.ComputedColumn{
			"summary": func(row map[string]any) any { return row["title"].(string) + "!" },
		},
	}
	rows := make([]map[string]any, 128)
	for i := range rows {
		rows[i] = map[string]any{
			"id":         i + 1,
			"title":      fmt.Sprintf("Post %03d", i),
			"published":  i%2 == 0,
			"created_at": time.Date(2026, time.Month((i%12)+1), 1, 0, 0, 0, 0, time.UTC),
		}
	}
	query := url.Values{"o": {"title"}, "p": {"3"}, "status": {"published"}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		changeList, err := admin.BuildChangeList(modelAdmin, rows, query)
		if err != nil {
			b.Fatalf("BuildChangeList() error = %v", err)
		}
		if changeList.Total != len(rows) {
			b.Fatalf("Total = %d, want %d", changeList.Total, len(rows))
		}
	}
}

func BenchmarkSerializerValidation(b *testing.B) {
	serializer := gogoapi.NewSerializer(
		gogoapi.BooleanField("active", gogoapi.FieldOptions{}),
		gogoapi.IntegerField("count", gogoapi.FieldOptions{}),
		gogoapi.FloatField("ratio", gogoapi.FieldOptions{}),
		gogoapi.StringField("title", gogoapi.FieldOptions{Required: true}),
		gogoapi.EmailField("email", gogoapi.FieldOptions{}),
		gogoapi.URLField("site", gogoapi.FieldOptions{}),
		gogoapi.SlugField("slug", gogoapi.FieldOptions{}),
		gogoapi.DateTimeField("created_at", gogoapi.FieldOptions{}),
		gogoapi.ChoiceField("status", gogoapi.FieldOptions{}, []string{"draft", "published"}),
		gogoapi.ListField("tags", gogoapi.FieldOptions{}, gogoapi.StringField("tag", gogoapi.FieldOptions{})),
		gogoapi.NestedObjectField("profile", gogoapi.FieldOptions{}, gogoapi.NewSerializer(gogoapi.StringField("name", gogoapi.FieldOptions{Required: true}))),
	)
	input := map[string]any{
		"active": true, "count": "42", "ratio": "3.5", "title": "Gogo",
		"email": "dev@example.com", "site": "https://example.com", "slug": "gogo-api",
		"created_at": "2026-06-28T12:30:00Z", "status": "draft",
		"tags": []any{"go", "api"}, "profile": map[string]any{"name": "Saksham"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, fieldErrors, ok := serializer.Validate(input)
		if !ok {
			b.Fatalf("Validate() errors = %#v", fieldErrors)
		}
	}
}

type benchmarkItem struct {
	ID   int64
	Name string
}

func benchmarkModel() models.Metadata {
	return models.Metadata{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Fields: []models.FieldMeta{
			{Name: "id", PrimaryKey: true},
			{Name: "title"},
			{Name: "created_at"},
			{Name: "views"},
			{Name: "active"},
		},
	}
}

func benchmarkMigrationStates() (migrations.ProjectState, migrations.ProjectState) {
	from := migrations.NewProjectState()
	from.AddModel(migrations.ModelState{
		AppLabel:  "blog",
		Name:      "Post",
		TableName: "blog_post",
		Fields:    []migrations.FieldState{{Name: "title", Kind: "text"}},
		Indexes:   []migrations.IndexState{{Name: "idx_title", Fields: []string{"title"}}},
	})
	to := migrations.NewProjectState()
	to.AddModel(migrations.ModelState{
		AppLabel:  "blog",
		Name:      "Post",
		TableName: "blog_post",
		Fields: []migrations.FieldState{
			{Name: "title", Kind: "text"},
			{Name: "published", Kind: "boolean"},
		},
		Indexes:     []migrations.IndexState{{Name: "idx_title", Fields: []string{"title"}}},
		Constraints: []migrations.ConstraintState{{Name: "uniq_title", Type: "unique", Fields: []string{"title"}}},
	})
	return from, to
}

func openBenchmarkDB(b *testing.B) *orm.Database {
	b.Helper()
	ctx := context.Background()
	database, err := orm.OpenDatabase(ctx, orm.DatabaseConfig{
		Name:    orm.DefaultDatabase,
		Driver:  "sqlite",
		DSN:     ":memory:",
		Dialect: sqlitedialect.New(),
	})
	if err != nil {
		b.Fatalf("OpenDatabase() error = %v", err)
	}
	if _, err := database.SQLDB().Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		b.Fatalf("create table: %v", err)
	}
	tx, err := database.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin insert tx: %v", err)
	}
	for i := 0; i < 128; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items(name) VALUES (?)`, fmt.Sprintf("item-%03d", i)); err != nil {
			_ = tx.Rollback()
			b.Fatalf("insert item: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit insert tx: %v", err)
	}
	return database
}
