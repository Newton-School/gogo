package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type countedBackend struct {
	db.Backend
	queries atomic.Int32
}

func TestQueryScopeCarriesIntoDeletionGraph(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	child.Tenant = 2
	if err := store.Save(ctx, child, orm.SaveOptions{UpdateFields: []string{"tenant"}}); err != nil {
		t.Fatal(err)
	}
	query := orm.For(store, func() *relationRow { return &relationRow{Definition: parent.Schema()} }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	if _, err := query.Delete(ctx); !db.IsCode(err, db.ForeignKeyViolation) {
		t.Fatal("hidden cascade target deleted", err)
	}
	if count, err := query.Count(ctx); err != nil || count != 1 {
		t.Fatal("scope failure did not rollback root", count, err)
	}
}

func (b *countedBackend) Query(ctx context.Context, statement string, args ...any) (db.Rows, error) {
	b.queries.Add(1)
	return b.Backend.Query(ctx, statement, args...)
}

func TestSelectRelatedSingleQueryNullableScopedAndReverse(t *testing.T) {
	store, parent, child := setupRelations(t, models.Cascade, false)
	ctx := context.Background()
	hidden := &relationRow{Definition: parent.Schema(), Tenant: 2}
	if err := store.Save(ctx, hidden, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, parentID := range []*int64{nil, &hidden.ID} {
		if err := store.Save(ctx, &relationRow{Definition: child.Schema(), Tenant: 1, Parent: parentID}, orm.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	counted := &countedBackend{Backend: store.Backend}
	store.Backend = counted
	query := orm.For(store, func() *relationRow { return &relationRow{Definition: child.Schema()} }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil }).SelectRelated("parent").OrderBy("id")
	statement, args, err := query.SQLContext(ctx)
	if err != nil || !strings.Contains(statement, "LEFT JOIN") || len(args) != 2 {
		t.Fatal(statement, args, err)
	}
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 3 || counted.queries.Load() != 1 {
		t.Fatal(rows, err, counted.queries.Load())
	}
	for i, row := range rows {
		record, _ := models.Bind(row)
		related, loaded := orm.RelatedOne(record, "parent")
		if !loaded {
			t.Fatal("related path absent")
		}
		if i == 0 {
			if related == nil || mustValue(t, related, "id") != parent.ID {
				t.Fatal(related)
			}
		} else if related != nil {
			t.Fatal("nullable/hidden relation leaked", related)
		}
	}
	if _, err := query.Only("tenant").All(ctx); err == nil {
		t.Fatal("deferred connector field accepted")
	}
	if _, err := orm.For(store, func() *relationRow { return &relationRow{Definition: parent.Schema()} }).SelectRelated("child_set").All(ctx); err == nil {
		t.Fatal("collection SelectRelated accepted")
	}
	count, err := query.Count(ctx)
	if err != nil || count != 3 {
		t.Fatal(count, err)
	}
	values, err := query.Values(ctx, "tenant")
	if err != nil || len(values) != 3 || len(values[0]) != 1 {
		t.Fatal(values, err)
	}
	if _, err := query.SelectForUpdateOf("self").Count(ctx); err == nil {
		t.Fatal("locked count accepted without Atomic")
	}
	if _, err := query.SelectForUpdateOf("self").Values(ctx); err == nil {
		t.Fatal("locked values accepted without Atomic")
	}
}

func TestSelectRelatedNestedAndReverseOneToOne(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	company := models.Schema{AppLabel: "tests", Name: "Company", Fields: []models.Field{models.BigAutoField("id"), models.TextField("name")}}
	author := models.Schema{AppLabel: "tests", Name: "Author", Fields: []models.Field{models.BigAutoField("id"), models.OneToOneField("company", models.Relation{Target: company.Key(), OnDelete: models.Cascade, RelatedName: "author"}, models.Nullable)}}
	book := models.Schema{AppLabel: "tests", Name: "Book", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("author", models.Relation{Target: author.Key(), OnDelete: models.Cascade}, models.Nullable)}}
	registry := &models.Registry{}
	ops := []migrations.Operation{}
	for _, schema := range []models.Schema{company, author, book} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, migrations.CreateModel(schema))
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: ops}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	c := saveMap(t, store, company, map[string]any{"name": "Publisher"})
	a := saveMap(t, store, author, map[string]any{"company": mustValue(t, c, "id")})
	saveMap(t, store, book, map[string]any{"author": mustValue(t, a, "id")})
	saveMap(t, store, book, map[string]any{"author": nil})
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(book); return r }).SelectRelated("author", "author__company").OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 2 || counted.queries.Load() != 1 {
		t.Fatal(rows, err, counted.queries.Load())
	}
	loaded, ok := orm.RelatedOne(rows[0], "author")
	if !ok || loaded == nil {
		t.Fatal(loaded, ok)
	}
	nested, ok := orm.RelatedOne(loaded, "company")
	if !ok || nested == nil || mustValue(t, nested, "name") != "Publisher" {
		t.Fatal(nested, ok)
	}
	if len(rows[0].State().Deferred) != 0 {
		t.Fatal("loaded map record fields left deferred")
	}
	filtered, err := query.Filter(orm.Q("author__company__name", "Publisher")).All(ctx)
	if err != nil || len(filtered) != 1 {
		t.Fatal("joined lookup did not default to exact", filtered, err)
	}
	reverse, err := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(company); return r }).SelectRelated("author").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if related, ok := orm.RelatedOne(reverse, "author"); !ok || related == nil {
		t.Fatal(related, ok)
	}
	if repeated, err := query.SelectRelated("author__company__author").All(ctx); err != nil || len(repeated) != 2 {
		t.Fatal("finite repeated-schema path rejected", repeated, err)
	}
	failure := errors.New("scoping unavailable")
	if _, err := query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == company.Key() {
			return db.Predicate{}, failure
		}
		return db.Predicate{}, nil
	}).All(ctx); !errors.Is(err, failure) {
		t.Fatal("eager scope error swallowed", err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error { _, err := query.SelectForUpdateOf("self").All(ctx); return err }); err != nil {
		t.Fatal("joined root locking failed", err)
	}
	store.Backend = b
	saveMap(t, store, book, map[string]any{"author": mustValue(t, a, "id")})
	store.Backend = counted
	for _, selected := range []bool{false, true} {
		counted.queries.Store(0)
		eager := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(book); return r }).OrderBy("id").PrefetchRelated("author", "author__company")
		if selected {
			eager = eager.SelectRelated("author")
		}
		rows, err := eager.All(ctx)
		expected := int32(3)
		if selected {
			expected = 2
		}
		if err != nil || len(rows) != 3 || counted.queries.Load() != expected {
			t.Fatal("nested prefetch query count", rows, err, counted.queries.Load(), selected)
		}
		for _, i := range []int{0, 2} {
			author, loaded := orm.RelatedOne(rows[i], "author")
			if !loaded || author == nil {
				t.Fatal("author cache missing")
			}
			company, loaded := orm.RelatedOne(author, "company")
			if !loaded || company == nil || mustValue(t, company, "name") != "Publisher" {
				t.Fatal("nested cache not attached to every copy", company, loaded, selected)
			}
		}
	}
}

func TestSelectRelatedRequiredJoinsAndLocking(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	company := models.Schema{AppLabel: "tests", Name: "Company", Fields: []models.Field{models.BigAutoField("id"), models.TextField("name")}}
	author := models.Schema{AppLabel: "tests", Name: "Author", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("company", models.Relation{Target: company.Key(), OnDelete: models.Cascade})}}
	book := models.Schema{AppLabel: "tests", Name: "Book", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("author", models.Relation{Target: author.Key(), OnDelete: models.Cascade})}}
	note := models.Schema{AppLabel: "tests", Name: "Note", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("book", models.Relation{Target: book.Key(), OnDelete: models.Cascade}, models.Nullable)}}
	registry := &models.Registry{}
	ops := []migrations.Operation{}
	for _, schema := range []models.Schema{company, author, book, note} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, migrations.CreateModel(schema))
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: ops}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, registry)
	c := saveMap(t, store, company, map[string]any{"name": "Publisher"})
	a := saveMap(t, store, author, map[string]any{"company": mustValue(t, c, "id")})
	bookRecord := saveMap(t, store, book, map[string]any{"author": mustValue(t, a, "id")})
	saveMap(t, store, note, map[string]any{"book": mustValue(t, bookRecord, "id")})
	saveMap(t, store, note, map[string]any{"book": nil})
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(book); return r }).SelectRelated("author__company")
	statement, _, err := query.SQL()
	if err != nil || strings.Count(statement, "INNER JOIN") != 2 || strings.Contains(statement, "LEFT JOIN") {
		t.Fatal(statement, err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		_, err := query.SelectForUpdate(false, false).Get(ctx)
		return err
	}); err != nil {
		t.Fatal("required joined rows could not be locked", err)
	}
	outerQuery := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(note); return r }).SelectRelated("book__author__company").OrderBy("id")
	statement, _, err = outerQuery.SQL()
	if err != nil || strings.Count(statement, "LEFT JOIN") != 3 || strings.Contains(statement, "INNER JOIN") {
		t.Fatal("required descendant erased nullable ancestor", statement, err)
	}
	rows, err := outerQuery.All(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if related, loaded := orm.RelatedOne(rows[1], "book"); !loaded || related != nil {
		t.Fatal("nullable root lost", related, loaded)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error { _, err := outerQuery.SelectForUpdateOf("self").All(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	scoped := query.WithScope(func(_ context.Context, schema models.Schema) (db.Predicate, error) {
		if schema.Key() == author.Key() {
			return orm.Q("id", -1), nil
		}
		return db.Predicate{}, nil
	})
	statement, _, err = scoped.SQL()
	if err != nil || strings.Count(statement, "LEFT JOIN") != 2 {
		t.Fatal("scoped required relation became inner", statement, err)
	}
	rows, err = scoped.All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal("hidden target removed root", rows, err)
	}
	if related, loaded := orm.RelatedOne(rows[0], "author"); !loaded || related != nil {
		t.Fatal("hidden required relation leaked", related, loaded)
	}
}
