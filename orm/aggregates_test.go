package orm

import (
	"testing"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestAggregateSQLGeneration(t *testing.T) {
	dialect := postgres.New()
	cases := []struct {
		name string
		agg  AggregateExpression
		want string
	}{
		{"count_all", CountAll().As("total"), `COUNT(*) AS "total"`},
		{"count_distinct_filter", Count(F("id")).Distinct().Filter(Filter("active", LookupExact, true)).As("active_count"), `COUNT(DISTINCT "id") FILTER (WHERE "active" = $1) AS "active_count"`},
		{"sum", Sum(F("amount")).As("amount_sum"), `SUM("amount") AS "amount_sum"`},
		{"avg", Avg(F("rating")).As("rating_avg"), `AVG("rating") AS "rating_avg"`},
		{"min", Min(F("created_at")).As("first_created"), `MIN("created_at") AS "first_created"`},
		{"max", Max(F("created_at")).As("last_created"), `MAX("created_at") AS "last_created"`},
		{"stddev", StdDev(F("score")).As("score_stddev"), `STDDEV("score") AS "score_stddev"`},
		{"variance", Variance(F("score")).As("score_variance"), `VARIANCE("score") AS "score_variance"`},
	}
	for _, tc := range cases {
		fragment, err := tc.agg.SelectionSQL(dialect, 1)
		if err != nil {
			t.Fatalf("%s SelectionSQL() error = %v", tc.name, err)
		}
		if fragment.SQL != tc.want {
			t.Fatalf("%s SQL = %q, want %q", tc.name, fragment.SQL, tc.want)
		}
	}
}

func TestAggregateWindowAndAnnotationIntegration(t *testing.T) {
	dialect := postgres.New()
	windowed, err := CompileExpression(dialect, Over(Avg(F("score")), Window{PartitionBy: []Expression{F("team_id")}}), 1)
	if err != nil {
		t.Fatalf("CompileExpression() error = %v", err)
	}
	if windowed.SQL != `AVG("score") OVER (PARTITION BY "team_id")` {
		t.Fatalf("windowed aggregate SQL = %q", windowed.SQL)
	}

	query, err := NewQuery(models.Metadata{AppLabel: "sales", ModelName: "Invoice"}).
		AnnotateAggregate(Sum(F("amount")).As("amount_sum"), dialect)
	if err != nil {
		t.Fatalf("AnnotateAggregate() error = %v", err)
	}
	if query.Annotations["amount_sum"].SQL != `SUM("amount")` {
		t.Fatalf("annotation = %#v", query.Annotations["amount_sum"])
	}
}

func TestAggregateResultTypedAccess(t *testing.T) {
	result := AggregateResult{Values: map[string]any{
		"count": int64(3),
		"sum":   12.5,
		"name":  "ignored",
	}}
	if got, ok := result.Int64("count"); !ok || got != 3 {
		t.Fatalf("Int64(count) = (%d, %v)", got, ok)
	}
	if got, ok := result.Float64("sum"); !ok || got != 12.5 {
		t.Fatalf("Float64(sum) = (%f, %v)", got, ok)
	}
	if _, ok := result.Float64("name"); ok {
		t.Fatalf("Float64(name) ok = true, want false")
	}
	if got, ok := result.Get("missing"); ok || got != nil {
		t.Fatalf("Get(missing) = (%#v, %v)", got, ok)
	}
}
