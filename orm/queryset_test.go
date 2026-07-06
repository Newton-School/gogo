package orm

import (
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestQuerySetLazyOperationsAreImmutable(t *testing.T) {
	base := NewQuerySet(testCompilerModel(), NewCompiler(postgres.New()))
	derived := base.
		All().
		Filter(Predicate{Field: "active", Lookup: LookupExact, Value: true}).
		Exclude(Predicate{Field: "title", Lookup: LookupExact, Value: "draft"}).
		OrderBy("created_at").
		Reverse().
		Distinct("id").
		Values("id", "title").
		ValuesList("id").
		Dates("created_at", "month", "ASC").
		DateTimes("created_at", "hour", "UTC", "DESC").
		Only("id", "title").
		Defer("views").
		SelectRelated("author").
		PrefetchRelated("tags").
		Annotate("lower_title", ExpressionRef{SQL: `LOWER("title")`}).
		Alias("visible", ExpressionRef{SQL: `"active" = true`}).
		Using("replica").
		SelectForUpdate(LockState{ForUpdate: true, SkipLocked: true}).
		ComplexFilter(Filter("views", LookupGT, 10))

	if len(base.Query().Filters) != 0 || base.UsingAlias() != DefaultDatabase {
		t.Fatalf("base queryset was mutated: %#v", base)
	}
	state := derived.State()
	if state.Mode != QueryModeValuesList || state.DateField != "created_at" || state.DateTimeZone != "UTC" {
		t.Fatalf("queryset state = %#v", state)
	}
	if derived.UsingAlias() != "replica" || len(derived.Query().Filters) != 2 || len(derived.Query().Excludes) != 1 {
		t.Fatalf("derived queryset = %#v", derived)
	}
	if !derived.Query().Locking.ForUpdate || !derived.Query().Locking.SkipLocked {
		t.Fatalf("lock state = %#v", derived.Query().Locking)
	}
}

func TestQuerySetSetOperationsAndOrderingHelpers(t *testing.T) {
	compiler := NewCompiler(postgres.New())
	base := NewQuerySet(testCompilerModel(), compiler).Values("id")
	other := NewQuerySet(testCompilerModel(), compiler).Values("id").Filter(Predicate{Field: "active", Lookup: LookupExact, Value: false})

	union := base.Union(other)
	intersection := base.Intersection(other)
	difference := base.Difference(other)
	if union.Query().SetOperations[0].Type != SetUnion || intersection.Query().SetOperations[0].Type != SetIntersection || difference.Query().SetOperations[0].Type != SetDifference {
		t.Fatalf("set operation types = %#v %#v %#v", union.Query().SetOperations, intersection.Query().SetOperations, difference.Query().SetOperations)
	}

	if *base.First().Query().Limit != 1 || *base.Last().Query().Limit != 1 {
		t.Fatalf("first/last did not limit query")
	}
	if base.Latest("created_at").Query().Ordering[0] != "-created_at" || base.Earliest("created_at").Query().Ordering[0] != "created_at" {
		t.Fatalf("latest/earliest ordering mismatch")
	}
	got, err := base.Get(Predicate{Field: "id", Lookup: LookupExact, Value: int64(1)})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if *got.Query().Limit != 2 {
		t.Fatalf("Get() limit = %d, want 2", *got.Query().Limit)
	}
	inBulk := base.InBulk("id", []any{int64(1), int64(2)})
	if inBulk.Query().Filters[0].Lookup != LookupIn {
		t.Fatalf("InBulk() filters = %#v", inBulk.Query().Filters)
	}
}

func TestQuerySetCompilesOperations(t *testing.T) {
	qs := NewQuerySet(testCompilerModel(), NewCompiler(postgres.New()))
	filtered := qs.Filter(Predicate{Field: "id", Lookup: LookupExact, Value: int64(1)})

	for name, fn := range map[string]func() (CompiledSQL, error){
		"iterator": filtered.Iterator,
		"count":    filtered.Count,
		"exists":   filtered.Exists,
		"delete":   filtered.Delete,
		"explain":  filtered.Explain,
	} {
		if compiled, err := fn(); err != nil || compiled.SQL == "" {
			t.Fatalf("%s compiled = %#v, err = %v", name, compiled, err)
		}
	}

	created, err := qs.Create(map[string]any{"title": "Go", "active": true})
	if err != nil || created.SQL == "" {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	updated, err := filtered.Update(map[string]any{"title": "Updated"})
	if err != nil || updated.SQL == "" {
		t.Fatalf("Update() = %#v, %v", updated, err)
	}
	contains, err := qs.Contains("id", int64(1))
	if err != nil || contains.SQL == "" {
		t.Fatalf("Contains() = %#v, %v", contains, err)
	}
	aggregate, err := filtered.Aggregate(CountAll().As("total"))
	if err != nil || aggregate.SQL == "" {
		t.Fatalf("Aggregate() = %#v, %v", aggregate, err)
	}
	raw := qs.Raw("SELECT * FROM blog_post WHERE id = $1", int64(1))
	if raw.SQL != "SELECT * FROM blog_post WHERE id = $1" || raw.Args[0] != int64(1) {
		t.Fatalf("Raw() = %#v", raw)
	}
}

func TestQuerySetCreateOrUpdatePlansAndBulkOperations(t *testing.T) {
	qs := NewQuerySet(testCompilerModel(), NewCompiler(postgres.New()))
	getOrCreate, err := qs.GetOrCreate(map[string]any{"title": "Go"}, map[string]any{"active": true})
	if err != nil || getOrCreate.Get.SQL == "" || getOrCreate.Create.SQL == "" {
		t.Fatalf("GetOrCreate() = %#v, %v", getOrCreate, err)
	}
	updateOrCreate, err := qs.UpdateOrCreate(map[string]any{"title": "Go"}, map[string]any{"active": true})
	if err != nil || updateOrCreate.Get.SQL == "" || updateOrCreate.Update.SQL == "" || updateOrCreate.Create.SQL == "" {
		t.Fatalf("UpdateOrCreate() = %#v, %v", updateOrCreate, err)
	}
	bulkCreate, err := qs.BulkCreate([]map[string]any{{"title": "A"}, {"title": "B"}})
	if err != nil || len(bulkCreate) != 2 {
		t.Fatalf("BulkCreate() = %#v, %v", bulkCreate, err)
	}
	bulkUpdate, err := qs.BulkUpdate("id", []map[string]any{{"id": int64(1), "title": "A"}, {"id": int64(2), "title": "B"}})
	if err != nil || len(bulkUpdate) != 2 {
		t.Fatalf("BulkUpdate() = %#v, %v", bulkUpdate, err)
	}
}
