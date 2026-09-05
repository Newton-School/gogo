package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func TestJSONEqualityNullAndLiteralValues(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&jsonRow{}).Schema()
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	values := []any{nil, models.JSONNull, "null", map[string]any{"integer": json.Number("9007199254740993"), "nested": []any{nil, true}}, json.Number("9007199254740993"), true}
	ids := make([]any, len(values))
	for i, value := range values {
		row := saveMap(t, store, schema, map[string]any{"payload": value})
		ids[i] = mustValue(t, row, "id")
	}
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).OrderBy("id")
	for _, test := range []struct {
		name  string
		where db.Predicate
		want  []any
	}{
		{"nil_means_json_null", orm.Q("payload", nil), []any{ids[1]}},
		{"explicit_json_null", orm.Q("payload", models.JSONNull), []any{ids[1]}},
		{"raw_json_null", orm.Q("payload", json.RawMessage(`null`)), []any{ids[1]}},
		{"string_null", orm.Q("payload", "null"), []any{ids[2]}},
		{"sql_null", orm.Q("payload__isnull", true), []any{ids[0]}},
		{"not_sql_null", orm.Q("payload__isnull", false), ids[1:]},
		{"object", orm.Q("payload", values[3]), []any{ids[3]}},
		{"number", orm.Q("payload", values[4]), []any{ids[4]}},
		{"boolean", orm.Q("payload", true), []any{ids[5]}},
		{"field_expression_null", db.Predicate{Expression: expressionPointer(orm.F("payload")), Value: nil}, []any{ids[1]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := query.Filter(test.where).All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]any, len(rows))
			for i, row := range rows {
				got[i] = mustValue(t, row, "id")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatal("incorrect JSON lookup identity", got, test.want)
			}
		})
	}
	counted := &countedBackend{Backend: b}
	store.Backend = counted
	if _, err := query.Filter(orm.Q("payload", json.RawMessage(`{"broken":`))).All(ctx); err == nil || counted.queries.Load() != 0 {
		t.Fatal("invalid JSON lookup reached SQL", err, counted.queries.Load())
	}
}

func expressionPointer(expression db.Expression) *db.Expression { return &expression }
