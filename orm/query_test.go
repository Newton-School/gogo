package orm

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/models"
)

func TestQueryBuilderMethodsDoNotMutateOriginal(t *testing.T) {
	base := NewQuery(models.Metadata{AppLabel: "blog", ModelName: "Post"})
	filtered := base.AddFilter(Predicate{Field: "title", Lookup: LookupExact, Value: "hello"})
	ordered := filtered.Order("title")

	if len(base.Filters) != 0 || len(base.Ordering) != 0 {
		t.Fatalf("base query was mutated: %#v", base)
	}
	if len(filtered.Filters) != 1 || len(filtered.Ordering) != 0 {
		t.Fatalf("filtered query = %#v", filtered)
	}
	if len(ordered.Filters) != 1 || len(ordered.Ordering) != 1 {
		t.Fatalf("ordered query = %#v", ordered)
	}
}

func TestQueryCloneCopiesEveryStateBucket(t *testing.T) {
	query := NewQuery(models.Metadata{AppLabel: "blog", ModelName: "Post"}).
		Select("id", "title").
		AddFilter(Predicate{Field: "title", Lookup: LookupIContains, Value: "go"}).
		AddExclude(Predicate{Field: "deleted_at", Lookup: LookupIsNull, Value: false}).
		AddJoin(Join{Path: "author", Type: JoinInner, Target: "auth.User"}).
		Order("-created_at").
		Group("author_id").
		AddHaving(Predicate{Field: "count", Lookup: LookupGT, Value: 1}).
		LimitTo(20).
		OffsetBy(10).
		SetDistinct(true, "author_id").
		Annotate("post_count", ExpressionRef{SQL: "COUNT(*)"}).
		SelectRelated("author").
		PrefetchRelated("tags").
		WithLock(LockState{ForUpdate: true, SkipLocked: true}).
		AddSetOperation(SetOperation{Type: SetUnion, Query: NewQuery(models.Metadata{AppLabel: "blog", ModelName: "ArchivedPost"})}).
		AddWindow("rank", WindowState{Expression: "RANK()", PartitionBy: []string{"author_id"}, OrderBy: []string{"-created_at"}})

	cloned := query.Clone()
	cloned.SelectedColumns[0] = "changed"
	cloned.Filters[0].Field = "changed"
	cloned.Excludes[0].Field = "changed"
	cloned.Joins[0].Path = "changed"
	cloned.Ordering[0] = "changed"
	cloned.Grouping[0] = "changed"
	cloned.Having[0].Field = "changed"
	*cloned.Limit = 1
	*cloned.Offset = 2
	cloned.DistinctFields[0] = "changed"
	cloned.Annotations["post_count"] = ExpressionRef{SQL: "changed"}
	cloned.Related.SelectRelated[0] = "changed"
	cloned.Related.PrefetchRelated[0] = "changed"
	cloned.Locking.SkipLocked = false
	cloned.SetOperations[0].Query.Model.ModelName = "Changed"
	cloned.Windows["rank"] = WindowState{Expression: "changed"}

	if query.SelectedColumns[0] != "id" || query.Filters[0].Field != "title" || query.Excludes[0].Field != "deleted_at" {
		t.Fatalf("query slices share backing storage: %#v", query)
	}
	if query.Joins[0].Path != "author" || query.Ordering[0] != "-created_at" || query.Grouping[0] != "author_id" {
		t.Fatalf("join/order/group state was mutated: %#v", query)
	}
	if query.Having[0].Field != "count" || *query.Limit != 20 || *query.Offset != 10 || query.DistinctFields[0] != "author_id" {
		t.Fatalf("limit/distinct/having state was mutated: %#v", query)
	}
	if query.Annotations["post_count"].SQL != "COUNT(*)" {
		t.Fatalf("annotations were mutated: %#v", query.Annotations)
	}
	if !reflect.DeepEqual(query.Related.SelectRelated, []string{"author"}) || !reflect.DeepEqual(query.Related.PrefetchRelated, []string{"tags"}) {
		t.Fatalf("related loading was mutated: %#v", query.Related)
	}
	if !query.Locking.SkipLocked || query.SetOperations[0].Query.Model.ModelName != "ArchivedPost" || query.Windows["rank"].Expression != "RANK()" {
		t.Fatalf("lock/set/window state was mutated: %#v", query)
	}
}

func TestQueryNoneAndReverse(t *testing.T) {
	query := NewQuery(models.Metadata{AppLabel: "blog", ModelName: "Post"}).Order("created_at").None().Reverse()
	if !query.Empty {
		t.Fatalf("Empty = false, want true")
	}
	if !reflect.DeepEqual(query.Ordering, []string{"-created_at"}) {
		t.Fatalf("Ordering = %#v", query.Ordering)
	}
}
