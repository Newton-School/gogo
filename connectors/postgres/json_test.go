package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type jsonRow struct {
	models.Base
	ID      int64
	Payload json.RawMessage
}

func (*jsonRow) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "JSON", Fields: []models.Field{models.BigAutoField("id", models.WithStructField("ID")), models.JSONField("payload", models.WithStructField("Payload"), models.Nullable, models.Optional)}}
}

func TestJSONExactNumbersRawModelsAndDistinctNullRoundTrips(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&jsonRow{}).Schema()
	engine := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(schema)}}}}
	if err := engine.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	precise := saveMap(t, store, schema, map[string]any{"payload": json.RawMessage(`{"integer":9007199254740993,"decimal":12345678901234567890.123456789,"nested":[18446744073709551615,null]}`)})
	sqlNull := saveMap(t, store, schema, map[string]any{"payload": nil})
	jsonNull := saveMap(t, store, schema, map[string]any{"payload": models.JSONNull})
	query := orm.For(store, func() *models.MapRecord { r, _ := models.NewRecord(schema); return r }).OrderBy("id")
	rows, err := query.All(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	payload := mustValue(t, rows[0], "payload").(map[string]any)
	if payload["integer"] != json.Number("9007199254740993") || payload["decimal"] != json.Number("12345678901234567890.123456789") || payload["nested"].([]any)[0] != json.Number("18446744073709551615") {
		t.Fatal("JSON number precision lost", payload)
	}
	if mustValue(t, rows[1], "payload") != nil || mustValue(t, rows[2], "payload") != models.JSONNull {
		t.Fatal("SQL and JSON null collapsed")
	}
	values, err := query.Values(ctx, "payload")
	if err != nil || len(values) != 3 || values[0]["payload"].(map[string]any)["integer"] != json.Number("9007199254740993") || values[1]["payload"] != nil || values[2]["payload"] != models.JSONNull {
		t.Fatal("Values skipped JSON field decoding", values, err)
	}
	for _, row := range rows {
		if err := store.Save(ctx, row, orm.SaveOptions{UpdateFields: []string{"payload"}}); err != nil {
			t.Fatal(err)
		}
	}
	var isSQLNull, isJSONNull bool
	if err := db.QueryRow(ctx, b, "SELECT payload IS NULL FROM tests_json WHERE id=$1", []any{mustValue(t, sqlNull, "id")}, &isSQLNull); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, b, "SELECT payload='null'::jsonb FROM tests_json WHERE id=$1", []any{mustValue(t, jsonNull, "id")}, &isJSONNull); err != nil {
		t.Fatal(err)
	}
	if !isSQLNull || !isJSONNull {
		t.Fatal("null states changed after save", isSQLNull, isJSONNull)
	}
	typed, err := orm.For(store, func() *jsonRow { return &jsonRow{} }).Filter(orm.Q("id", mustValue(t, precise, "id"))).Get(ctx)
	if err != nil || !json.Valid(typed.Payload) {
		t.Fatal("typed RawMessage loaded invalid JSON", typed, err)
	}
	if err := store.Save(ctx, typed, orm.SaveOptions{UpdateFields: []string{"payload"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshFromDB(ctx, typed, "payload"); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(typed.Payload) {
		t.Fatal("typed raw payload changed on refresh", string(typed.Payload))
	}
	typedRows, err := orm.For(store, func() *jsonRow { return &jsonRow{} }).OrderBy("id").All(ctx)
	if err != nil || len(typedRows) != 3 || typedRows[1].Payload != nil || string(typedRows[2].Payload) != "null" {
		t.Fatal("raw-message null representations collapsed", typedRows, err)
	}
	for _, row := range typedRows {
		if err := store.Save(ctx, row, orm.SaveOptions{UpdateFields: []string{"payload"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(ctx, b, "SELECT payload IS NULL FROM tests_json WHERE id=$1", []any{mustValue(t, sqlNull, "id")}, &isSQLNull); err != nil || !isSQLNull {
		t.Fatal("typed raw-message save changed SQL NULL", isSQLNull, err)
	}
}
