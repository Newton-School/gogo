package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type jsonContainerRow struct {
	models.Base
	ID     int64
	Scopes []string
	Counts map[string]int64
	Nested map[string]any
}

func (*jsonContainerRow) Schema() models.Schema {
	return models.Schema{AppLabel: "tests", Name: "JSONContainers", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.JSONField("scopes", models.WithStructField("Scopes")),
		models.JSONField("counts", models.WithStructField("Counts")),
		models.JSONField("nested", models.WithStructField("Nested")),
	}}
}

func TestJSONTypedContainersCleanSaveAndHydrate(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	schema := (&jsonContainerRow{}).Schema()
	if err := b.SchemaEditor().CreateModel(ctx, b, schema); err != nil {
		t.Fatal(err)
	}
	store := orm.New(b, nil)
	row := &jsonContainerRow{Scopes: []string{"shop.view_product", "shop.change_product"}, Counts: map[string]int64{"exact": 9007199254740993}, Nested: map[string]any{"exact": json.Number("9007199254740993"), "list": []any{json.Number("18446744073709551615"), nil}}}
	record, err := models.Bind(row)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := models.FullClean(ctx, record, models.CleanOptions{}, store); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(ctx, row, orm.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := orm.For(store, func() *jsonContainerRow { return &jsonContainerRow{} }).Filter(orm.Q("id", row.ID)).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Scopes, row.Scopes) || !reflect.DeepEqual(loaded.Counts, row.Counts) || !reflect.DeepEqual(loaded.Nested, row.Nested) {
		t.Fatal("JSON typed hydration changed shape or precision")
	}
	bound, _ := models.Bind(loaded)
	if err := models.FullClean(ctx, bound, models.CleanOptions{}, store); err != nil {
		t.Fatal("persisted JSON validation failed", err)
	}
	if err := bound.Set("scopes", []any{"shop.view_product"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, loaded, orm.SaveOptions{UpdateFields: []string{"scopes"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err = orm.For(store, func() *jsonContainerRow { return &jsonContainerRow{} }).Filter(orm.Q("id", row.ID)).Get(ctx)
	if err != nil || !reflect.DeepEqual(loaded.Scopes, []string{"shop.view_product"}) {
		t.Fatal("typed JSON update failed", err)
	}
	// Map records can represent nested nulls, but a []string consumer cannot.
	// Hydration must reject this representation instead of returning an empty
	// scope string and later persisting that zero value back into the JSON data.
	if _, err := b.Exec(ctx, `UPDATE "tests_jsoncontainers" SET "scopes"=$1::jsonb WHERE "id"=$2`, `[null]`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := orm.For(store, func() *jsonContainerRow { return &jsonContainerRow{} }).Filter(orm.Q("id", row.ID)).Get(ctx); err == nil {
		t.Fatal("JSON null silently became an empty Go string")
	}
	mapRow, err := orm.For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).Filter(orm.Q("id", row.ID)).Get(ctx)
	if err != nil || !reflect.DeepEqual(mustValue(t, mapRow, "scopes"), []any{nil}) {
		t.Fatal("rejected typed hydration changed stored JSON", err)
	}
}
