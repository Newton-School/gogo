package postgres_test

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestScopedTypedAggregatesPrecisionEmptyDefaultsAndFilters(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Sale", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.IntegerField("quantity"), models.DecimalField("amount", 30, 9, models.Nullable)}}
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, values := range []map[string]any{
		{"tenant": 1, "quantity": 1, "amount": "9007199254740993.100000000"},
		{"tenant": 1, "quantity": 2, "amount": "0.200000000"},
		{"tenant": 1, "quantity": 2, "amount": nil},
		{"tenant": 2, "quantity": 99, "amount": "100000000000000000000.000000000"},
	} {
		saveMap(t, store, schema, values)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	integer := models.BigIntegerField("result")
	decimal := models.DecimalField("result", 50, 25, models.Nullable)
	float := models.FloatField("result", models.Nullable)
	outputs := map[string]orm.ResultExpression{
		"count":          orm.Typed(orm.Count(orm.F("*")), integer),
		"count_distinct": orm.Typed(orm.DistinctAggregate(orm.Count(orm.F("quantity"))), integer),
		"total":          orm.Typed(orm.Sum(orm.F("amount")), decimal),
		"average":        orm.Typed(orm.Avg(orm.F("amount")), decimal),
		"minimum":        orm.Typed(orm.Min(orm.F("quantity")), integer),
		"maximum":        orm.Typed(orm.Max(orm.F("quantity")), integer),
		"filtered":       orm.Typed(orm.FilteredAggregate(orm.Sum(orm.F("quantity")), orm.Q("quantity__gte", 2)), integer),
		"deviation":      orm.Typed(orm.StdDev(orm.F("quantity"), true), float),
		"variance":       orm.Typed(orm.Variance(orm.F("quantity"), false), float),
	}
	result, err := query.OrderBy("id").Aggregate(ctx, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if counted.queries.Load() != 1 || result["count"] != int64(3) || result["count_distinct"] != int64(2) || result["minimum"] != int64(1) || result["maximum"] != int64(2) || result["filtered"] != int64(4) {
		t.Fatal(result, counted.queries.Load())
	}
	for name, expected := range map[string]string{"total": "9007199254740993.3", "average": "4503599627370496.65"} {
		got, ok := new(big.Rat).SetString(result[name].(string))
		want, _ := new(big.Rat).SetString(expected)
		if !ok || got.Cmp(want) != 0 {
			t.Fatal("aggregate decimal rounded", name, result[name])
		}
	}
	if math.Abs(result["variance"].(float64)-2.0/9.0) > 1e-12 || math.Abs(result["deviation"].(float64)-math.Sqrt(1.0/3.0)) > 1e-12 {
		t.Fatal("population/sample semantics", result)
	}
	empty := query.Filter(orm.Q("quantity", -1))
	result, err = empty.Aggregate(ctx, map[string]orm.ResultExpression{
		"count":   orm.Typed(orm.Count(orm.F("*")), integer),
		"sum":     orm.Typed(orm.Sum(orm.F("amount")), decimal),
		"default": orm.Typed(orm.Func("COALESCE", orm.Sum(orm.F("quantity")), orm.Value(7)), integer),
	})
	if err != nil || result["count"] != int64(0) || result["sum"] != nil || result["default"] != int64(7) {
		t.Fatal("empty aggregate contract", result, err)
	}
	if err := db.Atomic(ctx, b, db.AtomicOptions{}, func(ctx context.Context) error {
		_, err := query.Aggregate(ctx, outputs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("scope unavailable")
	if _, err := query.WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return db.Predicate{}, failure }).Aggregate(ctx, outputs); !errors.Is(err, failure) {
		t.Fatal("scope failure ignored", err)
	}
	for _, invalid := range []orm.Query[*models.MapRecord]{query.Limit(1), query.Offset(0), query.Distinct(), query.SelectForUpdateOf("self")} {
		counted.queries.Store(0)
		if _, err := invalid.Aggregate(ctx, outputs); !db.IsCode(err, db.UnsupportedFeature) || counted.queries.Load() != 0 {
			t.Fatal("unsupported aggregate shape silently changed semantics", err)
		}
	}
	for _, expression := range []db.Expression{orm.F("amount"), orm.Sum(orm.Sum(orm.F("quantity"))), orm.DistinctAggregate(orm.Count(orm.F("*"))), orm.FilteredAggregate(orm.Func("ABS", orm.F("quantity")), orm.Q("tenant", 1))} {
		counted.queries.Store(0)
		if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"invalid": orm.Typed(expression, integer)}); err == nil || counted.queries.Load() != 0 {
			t.Fatal("invalid aggregate expression reached SQL", err)
		}
	}
	if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"small": orm.Typed(orm.Sum(orm.F("amount")), models.SmallIntegerField("result"))}); err == nil {
		t.Fatal("declared output overflow was swallowed")
	}
	if _, err := empty.Aggregate(ctx, map[string]orm.ResultExpression{"required": orm.Typed(orm.Sum(orm.F("quantity")), integer)}); err == nil {
		t.Fatal("nonnullable empty output was silently accepted")
	}
}
