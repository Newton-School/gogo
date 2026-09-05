package models

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type jsonContainers struct {
	Base
	ID      int64
	Strings []string
	Counts  map[string]int64
	Nested  map[string]any
	Pointer *[]string
}

func (*jsonContainers) Schema() Schema {
	fields := []Field{BigAutoField("id", WithStructField("ID"))}
	for _, name := range []string{"Strings", "Counts", "Nested", "Pointer"} {
		fields = append(fields, JSONField(strings.ToLower(name), WithStructField(name), Nullable, Optional))
	}
	return Schema{AppLabel: "tests", Name: "JSONContainers", Fields: fields}
}

func TestJSONTypedContainersCleanAndAssignment(t *testing.T) {
	ctx := context.Background()
	pointer := []string{"pointer"}
	model := &jsonContainers{Strings: []string{"shop.view_product"}, Counts: map[string]int64{"precise": 9007199254740993}, Nested: map[string]any{"precise": json.Number("9007199254740993")}, Pointer: &pointer}
	record, err := Bind(model)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := FullClean(ctx, record, CleanOptions{}, nil); err != nil {
			t.Fatal("typed JSON cannot survive model cleaning", err)
		}
	}
	if !reflect.DeepEqual(model.Strings, []string{"shop.view_product"}) || model.Counts["precise"] != 9007199254740993 || model.Nested["precise"] != json.Number("9007199254740993") || model.Pointer == nil || !reflect.DeepEqual(*model.Pointer, pointer) {
		t.Fatal("typed containers changed", model)
	}
	if err := record.Set("strings", []any{"next"}); err != nil || !reflect.DeepEqual(model.Strings, []string{"next"}) {
		t.Fatal(err, model.Strings)
	}
	if err := record.Set("counts", json.RawMessage(`{"maximum":9223372036854775807}`)); err != nil || model.Counts["maximum"] != 9223372036854775807 {
		t.Fatal(err, model.Counts)
	}
	for _, test := range []struct {
		name  string
		value any
	}{{"strings", []any{"partial", json.Number("1")}}, {"counts", map[string]any{"maximum": json.Number("9223372036854775808")}}, {"pointer", []any{"partial", true}}} {
		before, _ := record.Get(test.name)
		encoded, _ := json.Marshal(before)
		if err := record.Set(test.name, test.value); err == nil {
			t.Fatal("invalid JSON container accepted", test.name)
		}
		after, _ := record.Get(test.name)
		got, _ := json.Marshal(after)
		if string(got) != string(encoded) {
			t.Fatal("failed assignment partially mutated model", test.name)
		}
	}
	if err := record.Set("strings", JSONNull); err != nil || model.Strings != nil {
		t.Fatal("JSON null container", err)
	}
	if err := record.Set("pointer", nil); err != nil || model.Pointer != nil {
		t.Fatal("SQL null pointer", err)
	}
}
