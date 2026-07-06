package orm

import (
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestPrefetchPlannerBuildsBatchedPlans(t *testing.T) {
	compiler := NewCompiler(postgres.New())
	parent := NewQuerySet(testCompilerModel(), compiler)
	comments := NewQuerySet(testCompilerModel(), compiler).Filter(Predicate{Field: "active", Lookup: LookupExact, Value: true})

	plans, err := PlanPrefetches(parent, Prefetch("comments", comments, "prefetched_comments"), Prefetch("tags", NewQuerySet(testCompilerModel(), compiler), ""))
	if err != nil {
		t.Fatalf("PlanPrefetches() error = %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %#v", plans)
	}
	if !plans[0].Batched || plans[0].ToAttr != "prefetched_comments" || plans[0].Path != "comments" {
		t.Fatalf("first prefetch plan = %#v", plans[0])
	}
	if plans[0].Query.SQL == "" || plans[1].Query.SQL == "" {
		t.Fatalf("prefetch queries were not compiled: %#v", plans)
	}
}
