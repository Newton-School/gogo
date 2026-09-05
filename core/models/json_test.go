package models

import (
	"context"
	"encoding/json"
	"testing"
)

type jsonRecord struct {
	Base
	ID      int64
	Payload json.RawMessage
}

func (*jsonRecord) Schema() Schema {
	return Schema{AppLabel: "tests", Name: "JSON", Fields: []Field{BigAutoField("id", WithStructField("ID")), JSONField("payload", WithStructField("Payload"), Nullable, Optional)}}
}

func TestJSONNullAndRawMessageAssignment(t *testing.T) {
	field := JSONField("payload", Nullable)
	for _, value := range []any{JSONNull, json.RawMessage("null"), "null"} {
		cleaned, err := field.Clean(context.Background(), value)
		if err != nil || cleaned != JSONNull {
			t.Fatal("JSON null lost its identity", cleaned, err)
		}
	}
	if cleaned, err := field.Clean(context.Background(), nil); err != nil || cleaned != nil {
		t.Fatal("SQL NULL became JSON null", cleaned, err)
	}
	model := &jsonRecord{}
	record, err := Bind(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Set("payload", map[string]any{"precise": json.Number("9007199254740993")}); err != nil {
		t.Fatal(err)
	}
	if string(model.Payload) != `{"precise":9007199254740993}` {
		t.Fatal("raw model field received debug string", string(model.Payload))
	}
	if err := FullClean(context.Background(), record, CleanOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := record.Set("payload", JSONNull); err != nil || string(model.Payload) != "null" {
		t.Fatal(string(model.Payload), err)
	}
	if err := record.Set("payload", nil); err != nil || model.Payload != nil {
		t.Fatal("SQL NULL raw message assignment", err)
	}
	if value, err := record.Get("payload"); err != nil || value != nil {
		t.Fatal("nil RawMessage was not SQL NULL", value, err)
	}
	var nilRaw json.RawMessage
	var nilPointer *json.RawMessage
	var nilNested **json.RawMessage
	for _, value := range []any{nilRaw, nilPointer, nilNested, &nilRaw} {
		if err := record.Set("payload", JSONNull); err != nil {
			t.Fatal(err)
		}
		if err := record.Set("payload", value); err != nil || model.Payload != nil {
			t.Fatal("typed nil RawMessage was not SQL NULL", err, string(model.Payload))
		}
	}
	rootNull := json.RawMessage("null")
	if err := record.Set("payload", &rootNull); err != nil || string(model.Payload) != "null" {
		t.Fatal("non-nil RawMessage pointer lost JSON null", string(model.Payload), err)
	}
}
