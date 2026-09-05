package postgres_test

import (
	"context"
	"encoding/json"
	"math/big"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestScopedCastsJSONPrecisionNullsAndBackendErrors(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := models.Schema{AppLabel: "tests", Name: "Casts", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.JSONField("payload", models.Nullable)}}
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	for _, values := range []map[string]any{
		{"tenant": 1, "payload": map[string]any{"amount": json.Number("9007199254740993.1"), "name": "Alpha", "encoded": `"null"`}},
		{"tenant": 1, "payload": map[string]any{"amount": json.Number("0.2"), "name": "Beta", "encoded": "null"}},
		{"tenant": 1, "payload": map[string]any{"amount": nil}},
		{"tenant": 1, "payload": map[string]any{}},
		{"tenant": 2, "payload": map[string]any{"amount": "not a number"}},
	} {
		saveMap(t, store, schema, values)
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	query := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).WithScope(func(context.Context, models.Schema) (db.Predicate, error) { return orm.Q("tenant", 1), nil })
	decimal := models.DecimalField("value", 30, 9, models.Nullable)
	amount := orm.Cast(orm.JSONTextPath("payload", "amount"), decimal)
	result, err := query.Aggregate(ctx, map[string]orm.ResultExpression{
		"sum":            orm.Typed(orm.Sum(amount), decimal),
		"count":          orm.Typed(orm.Count(amount), models.BigIntegerField("value")),
		"cast_after_sum": orm.Typed(orm.Cast(orm.Sum(amount), models.DecimalField("value", 30, 1)), decimal),
	})
	if err != nil || counted.queries.Load() != 1 || result["count"] != int64(2) {
		t.Fatal(result, err, counted.queries.Load())
	}
	for _, name := range []string{"sum", "cast_after_sum"} {
		got, ok := new(big.Rat).SetString(result[name].(string))
		want, _ := new(big.Rat).SetString("9007199254740993.3")
		if !ok || got.Cmp(want) != 0 {
			t.Fatal("cast rounded precise decimal", name, result[name])
		}
	}
	for _, test := range []struct {
		expression db.Expression
		lookup     string
		value      any
		want       int64
	}{
		{amount, "gt", "9007199254740993", 1},
		{amount, "isnull", true, 2},
		{orm.Cast(orm.JSONTextPath("payload", "name"), models.CharField("value", models.WithMaxLength(3))), "exact", "Alp", 1},
		{orm.Cast(orm.JSONTextPath("payload", "encoded"), models.JSONField("value")), "exact", "null", 1},
		{orm.Cast(orm.JSONTextPath("payload", "encoded"), models.JSONField("value")), "exact", nil, 1},
		{orm.Cast(orm.Value(7), models.TextField("value")), "exact", "7", 4},
		{orm.Cast(orm.Value(true), models.TextField("value")), "exact", "true", 4},
		{orm.Cast(orm.Value(1.5), models.TextField("value")), "exact", "1.5", 4},
	} {
		got, err := query.Filter(db.Predicate{Expression: &test.expression, Lookup: test.lookup, Value: test.value}).Count(ctx)
		if err != nil || got != test.want {
			t.Fatal("cast predicate output metadata", got, err)
		}
	}
	// Query construction owns its complete expression/output tree.
	predicate := db.Predicate{Expression: &amount, Lookup: "gt", Value: "9007199254740993"}
	snapshotted := query.Filter(predicate)
	before, beforeArgs, err := snapshotted.SQLContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	amount.Output.MaxDigits = 1
	amount.Args[0].Value.([]string)[0] = "changed"
	after, afterArgs, err := snapshotted.SQLContext(ctx)
	if err != nil || before != after || !reflect.DeepEqual(beforeArgs, afterArgs) {
		t.Fatal("cast query retained caller metadata", err)
	}
	for _, test := range []struct {
		value any
		field models.Field
	}{
		{"not a number", models.BigIntegerField("value")},
		{"9223372036854775808", models.BigIntegerField("value")},
		{"999.99", models.DecimalField("value", 3, 2)},
	} {
		cast := orm.Cast(orm.Value(test.value), test.field)
		if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"invalid": orm.Typed(orm.Max(cast), test.field)}); err == nil {
			t.Fatal("database conversion/overflow silently accepted")
		}
	}
	counted.queries.Store(0)
	invalid := orm.Cast(orm.Value(1), models.BigAutoField("id"))
	if _, err := query.Aggregate(ctx, map[string]orm.ResultExpression{"invalid": orm.Typed(orm.Max(invalid), models.BigIntegerField("value"))}); err == nil || counted.queries.Load() != 0 {
		t.Fatal("identity DDL reached CAST SQL", err)
	}
}
