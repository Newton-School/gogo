package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

// This deliberately independent, test-only checker covers the emitted schema
// vocabulary, not general JSON Schema or runtime validation of custom output.
// Both documents and representations retain json.Number; no float conversion
// can make an inexact integer fixture accidentally agree with its schema.
func outputConformanceJSON(t *testing.T, document []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("JSON has a trailing value or error: %v", err)
	}
	return value
}

func outputConformanceSchema(t *testing.T, serializer *Serializer, redactable bool) map[string]any {
	t.Helper()
	document, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{Redactable: redactable})
	if err != nil {
		t.Fatal(err)
	}
	schema, ok := outputConformanceJSON(t, document).(map[string]any)
	if !ok || schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("unexpected output document: %s", document)
	}
	return schema
}

func outputConformanceRepresentation(t *testing.T, serializer *Serializer, schema map[string]any, reader ValueReader) map[string]any {
	t.Helper()
	values, err := serializer.Representation(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	value := outputConformanceJSON(t, document)
	if err := outputConformanceCheck(schema, value); err != nil {
		t.Fatalf("representation %s violates output schema: %v", document, err)
	}
	return value.(map[string]any)
}

func outputConformanceCheck(schema map[string]any, value any) error {
	nodes := 0
	var visit func(map[string]any, any, int) error
	visit = func(rule map[string]any, value any, depth int) error {
		nodes++
		if depth > 32 || nodes > 65536 {
			return errors.New("test checker budget exceeded")
		}
		for key := range rule {
			switch key {
			case "$schema", "type", "anyOf", "const", "format", "properties", "required", "additionalProperties", "items", "minItems", "maxItems", "minProperties", "maxProperties":
			default:
				return fmt.Errorf("unhandled schema keyword %q", key)
			}
		}
		if alternatives, present := rule["anyOf"]; present {
			list, ok := alternatives.([]any)
			if !ok || len(list) == 0 {
				return errors.New("invalid anyOf")
			}
			matched := false
			for _, alternative := range list {
				child, ok := alternative.(map[string]any)
				if !ok {
					return errors.New("invalid anyOf child")
				}
				if visit(child, value, depth+1) == nil {
					matched = true
				}
			}
			if !matched {
				return errors.New("no anyOf branch matches")
			}
		}
		if constant, present := rule["const"]; present && !reflect.DeepEqual(constant, value) {
			return errors.New("const differs")
		}
		if kind, present := rule["type"]; present {
			matches := func(kind string) bool {
				switch kind {
				case "null":
					return value == nil
				case "string":
					_, ok := value.(string)
					return ok
				case "boolean":
					_, ok := value.(bool)
					return ok
				case "object":
					_, ok := value.(map[string]any)
					return ok
				case "array":
					_, ok := value.([]any)
					return ok
				case "number", "integer":
					number, ok := value.(json.Number)
					if !ok {
						return false
					}
					exact, ok := new(big.Rat).SetString(string(number))
					return ok && (kind == "number" || exact.IsInt())
				}
				return false
			}
			matched := false
			switch kind := kind.(type) {
			case string:
				matched = matches(kind)
			case []any:
				for _, candidate := range kind {
					name, ok := candidate.(string)
					matched = matched || (ok && matches(name))
				}
			}
			if !matched {
				return fmt.Errorf("type %v does not accept %T", kind, value)
			}
		}
		checkLength := func(size int, minimum, maximum string) error {
			for _, bound := range []string{minimum, maximum} {
				if raw, present := rule[bound]; present {
					number, ok := raw.(json.Number)
					if !ok {
						return fmt.Errorf("invalid %s", bound)
					}
					limit, err := number.Int64()
					if err != nil || limit < 0 || (bound == minimum && int64(size) < limit) || (bound == maximum && int64(size) > limit) {
						return fmt.Errorf("size %d violates %s=%v", size, bound, raw)
					}
				}
			}
			return nil
		}
		switch value := value.(type) {
		case map[string]any:
			if err := checkLength(len(value), "minProperties", "maxProperties"); err != nil {
				return err
			}
			properties, _ := rule["properties"].(map[string]any)
			if raw, present := rule["required"]; present {
				required, ok := raw.([]any)
				if !ok {
					return errors.New("invalid required")
				}
				for _, rawName := range required {
					name, ok := rawName.(string)
					if _, present := value[name]; !ok || !present {
						return fmt.Errorf("missing required %v", rawName)
					}
				}
			}
			for name, item := range value {
				child, declared := properties[name]
				if !declared {
					child = rule["additionalProperties"]
					if child == false {
						return fmt.Errorf("extra property %q", name)
					}
					if child == nil || child == true {
						continue
					}
				}
				childRule, ok := child.(map[string]any)
				if !ok {
					return errors.New("invalid property schema")
				}
				if err := visit(childRule, item, depth+1); err != nil {
					return fmt.Errorf("property %q: %w", name, err)
				}
			}
		case []any:
			if err := checkLength(len(value), "minItems", "maxItems"); err != nil {
				return err
			}
			if raw, present := rule["items"]; present {
				child, ok := raw.(map[string]any)
				if !ok {
					return errors.New("invalid items")
				}
				for _, item := range value {
					if err := visit(child, item, depth+1); err != nil {
						return fmt.Errorf("item: %w", err)
					}
				}
			}
		}
		return nil
	}
	return visit(schema, value, 0)
}

func TestOutputSchemaConformanceCheckerRejectsFalsePositives(t *testing.T) {
	for _, test := range []struct{ schema, accepted, rejected string }{
		{`{"type":"integer"}`, `9007199254740993`, `9007199254740993.5`},
		{`{"type":"number"}`, `9007199254740993.5`, `"9007199254740993.5"`},
		{`{"type":["boolean","null"]}`, `null`, `"false"`},
		{`{"anyOf":[{"type":"integer"},{"const":""}]}`, `""`, `"1"`},
		{`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`, `{"id":1}`, `{}`},
		{`{"type":"object","properties":{},"additionalProperties":false}`, `{}`, `{"private":1}`},
		{`{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":2}`, `[1,2]`, `[]`},
		{`{"type":"array","items":{"type":"integer"},"maxItems":2}`, `[1,2]`, `[1,2,3]`},
		{`{"type":"array","items":{"type":"integer"}}`, `[1]`, `["1"]`},
		{`{"type":"object","additionalProperties":{"type":"integer"},"minProperties":1,"maxProperties":2}`, `{"a":1}`, `{}`},
		{`{"type":"object","additionalProperties":{"type":"integer"},"maxProperties":1}`, `{"a":1}`, `{"a":1,"b":2}`},
		{`{"type":"object","additionalProperties":{"type":"integer"}}`, `{"a":1}`, `{"a":"1"}`},
	} {
		schema := outputConformanceJSON(t, []byte(test.schema)).(map[string]any)
		if err := outputConformanceCheck(schema, outputConformanceJSON(t, []byte(test.accepted))); err != nil {
			t.Fatalf("checker rejected its positive fixture: %v", err)
		}
		if err := outputConformanceCheck(schema, outputConformanceJSON(t, []byte(test.rejected))); err == nil {
			t.Fatalf("checker accepted %s under %s", test.rejected, test.schema)
		}
	}
}

func TestOutputSchemaConformanceScalarsAndNil(t *testing.T) {
	for _, test := range []struct {
		field models.Field
		input any
		wire  string
		kind  string
	}{
		{models.CharField("value"), "hello", `"hello"`, "string"},
		{models.TextField("value"), "नमस्ते", `"नमस्ते"`, "string"},
		{models.SlugField("value"), "example_slug", `"example_slug"`, "string"},
		{models.EmailField("value"), "reader@example.test", `"reader@example.test"`, "string"},
		{models.URLField("value"), "https://example.test/a", `"https://example.test/a"`, "string"},
		{models.GenericIPAddressField("value"), "2001:db8::1", `"2001:db8::1"`, "string"},
		{models.UUIDField("value"), "00000000-0000-4000-8000-000000000001", `"00000000-0000-4000-8000-000000000001"`, "string"},
		{models.FilePathField("value"), "public/example.txt", `"public/example.txt"`, "string"},
		{models.FileField("value"), "public/example.txt", `"public/example.txt"`, "string"},
		{models.ImageField("value"), "public/example.png", `"public/example.png"`, "string"},
		{models.BigIntegerField("value"), json.Number("9007199254740993"), `9007199254740993`, "integer"},
		{models.BooleanField("value"), "false", `false`, "boolean"},
		{models.FloatField("value"), "1.25", `1.25`, "number"},
		{models.DecimalField("value", 22, 2), "100000000000000001.25", `"100000000000000001.25"`, "string"},
		{models.DateField("value"), "2026-09-08", `"2026-09-08"`, "string"},
		{models.TimeField("value"), "12:34:56.123456", `"12:34:56.123456"`, "string"},
		{models.DateTimeField("value"), "2026-09-08T12:34:56.123456789+05:30", `"2026-09-08T07:04:56.123456789Z"`, "string"},
		// Representation accepts typed years outside RFC3339's four digits.
		{models.DateField("value"), time.Date(10000, 9, 8, 0, 0, 0, 0, time.UTC), `"10000-09-08"`, "string"},
		{models.DateTimeField("value"), time.Date(10000, 9, 8, 12, 34, 56, 0, time.UTC), `"10000-09-08T12:34:56Z"`, "string"},
		{models.DurationField("value"), 90 * time.Second, `"1m30s"`, "string"},
		{models.BinaryField("value"), []byte{0, 255, 1}, `"AP8B"`, "string"},
	} {
		t.Run(string(test.field.Kind), func(t *testing.T) {
			serializer := mustSerializer(t, Definition{Fields: []Field{Scalar(test.field)}})
			schema := outputConformanceSchema(t, serializer, false)
			property := schema["properties"].(map[string]any)["value"].(map[string]any)
			if !reflect.DeepEqual(property["type"], []any{test.kind, "null"}) {
				t.Fatalf("wire type not precise: %v", property)
			}
			switch test.field.Kind {
			case models.Date, models.Time, models.DateTime, models.Duration:
				if _, exists := property["format"]; exists {
					t.Fatalf("derived output added an incompatible format promise: %v", property)
				}
			}
			actual := outputConformanceRepresentation(t, serializer, schema, Values{"value": test.input})
			if !reflect.DeepEqual(actual["value"], outputConformanceJSON(t, []byte(test.wire))) {
				t.Fatalf("wire normalization changed: %#v", actual)
			}
			// Input nullability is false here, but nil bypasses output codecs.
			outputConformanceRepresentation(t, serializer, schema, Values{"value": nil})
			if err := outputConformanceCheck(schema, map[string]any{}); err == nil {
				t.Fatal("required output disappeared from schema")
			}
		})
	}
	for _, kind := range []models.Kind{models.SmallInteger, models.Integer, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto} {
		serializer := mustSerializer(t, Definition{Fields: []Field{Scalar(models.NewField("value", kind))}})
		schema := outputConformanceSchema(t, serializer, false)
		outputConformanceRepresentation(t, serializer, schema, Values{"value": 7})
		property := schema["properties"].(map[string]any)["value"].(map[string]any)
		if !reflect.DeepEqual(property["type"], []any{"integer", "null"}) {
			t.Fatalf("integer kind %s weakened: %v", kind, property)
		}
	}
	serializer := mustSerializer(t, Definition{Fields: []Field{JSONField("value")}})
	schema := outputConformanceSchema(t, serializer, false)
	for _, raw := range []string{`null`, `true`, `9007199254740993`, `"text"`, `[null,{"count":9007199254740993}]`, `{"nested":{"active":true}}`} {
		input := outputConformanceJSON(t, []byte(raw))
		actual := outputConformanceRepresentation(t, serializer, schema, Values{"value": input})
		if !reflect.DeepEqual(actual["value"], input) {
			t.Fatalf("JSON changed: %#v != %#v", actual["value"], input)
		}
	}
}

func TestOutputSchemaConformanceBlankNonStrings(t *testing.T) {
	for _, kind := range []models.Kind{models.SmallInteger, models.BigInteger, models.Boolean, models.Float, models.Decimal, models.Date, models.Time, models.DateTime, models.Duration, models.Binary} {
		t.Run(string(kind), func(t *testing.T) {
			field := models.NewField("value", kind, models.Optional)
			if kind == models.Decimal {
				field.MaxDigits, field.DecimalPlaces = 10, 2
			}
			serializer := mustSerializer(t, Definition{Fields: []Field{Scalar(field)}})
			schema := outputConformanceSchema(t, serializer, false)
			actual := outputConformanceRepresentation(t, serializer, schema, Values{"value": ""})
			if actual["value"] != "" {
				t.Fatal("blank output did not preserve empty string")
			}
			outputConformanceRepresentation(t, serializer, schema, Values{"value": nil})
			outputConformanceRepresentation(t, serializer, schema, Values{})
			if kind == models.SmallInteger || kind == models.BigInteger || kind == models.Boolean || kind == models.Float {
				if err := outputConformanceCheck(schema, map[string]any{"value": "not-empty"}); err == nil {
					t.Fatal("blank exception widened to arbitrary text")
				}
			}
		})
	}
}

func TestOutputSchemaConformanceNestedRequiredAndRedaction(t *testing.T) {
	child := mustSerializer(t, Definition{Fields: []Field{IntegerField("id"), StringField("note", models.Optional)}})
	optional := NestedField("optional", child)
	optional.Required = false
	serializer := mustSerializer(t, Definition{Fields: []Field{NestedField("child", child), optional}})
	schema := outputConformanceSchema(t, serializer, false)
	for _, values := range []Values{
		{"child": Values{"id": 1}},
		{"child": Values{"id": 1, "note": "visible"}, "optional": Values{"id": 2}},
		{"child": nil, "optional": nil},
	} {
		outputConformanceRepresentation(t, serializer, schema, values)
	}
	for _, invalid := range []map[string]any{{}, {"child": map[string]any{}}, {"child": map[string]any{"id": json.Number("1"), "private": true}}} {
		if err := outputConformanceCheck(schema, invalid); err == nil {
			t.Fatalf("nested schema accepted invalid representation: %#v", invalid)
		}
	}
	redacted := outputConformanceSchema(t, serializer, true)
	if err := outputConformanceCheck(redacted, map[string]any{}); err != nil {
		t.Fatalf("root redaction was not permitted: %v", err)
	}
	if err := outputConformanceCheck(redacted, map[string]any{"child": map[string]any{}}); err == nil {
		t.Fatal("root redaction weakened nested required fields")
	}
	if _, err := serializer.Representation(context.Background(), Values{"child": Values{}}); !errors.Is(err, ErrMissingField) {
		t.Fatalf("actual nested required behavior disagreed: %v", err)
	}
}

func TestOutputSchemaConformanceCollectionLimits(t *testing.T) {
	for _, dictionary := range []bool{false, true} {
		t.Run(fmt.Sprint("dictionary=", dictionary), func(t *testing.T) {
			field := ListField("values", IntegerField("item"), 2)
			field.MinItems, field.Dictionary = 1, dictionary
			serializer := mustSerializer(t, Definition{Fields: []Field{field}})
			schema := outputConformanceSchema(t, serializer, false)
			accepted := []any{[]any{json.Number("9007199254740993")}, []any{1, nil}, nil}
			rejected := []any{[]any{}, []any{1, 2, 3}}
			wrong := any([]any{"bad"})
			if dictionary {
				accepted = []any{Values{"a": json.Number("9007199254740993")}, Values{"a": 1, "b": nil}, nil}
				rejected = []any{Values{}, Values{"a": 1, "b": 2, "c": 3}}
				wrong = Values{"a": "bad"}
			}
			for _, input := range accepted {
				outputConformanceRepresentation(t, serializer, schema, Values{"values": input})
			}
			for _, input := range rejected {
				if _, err := serializer.Representation(context.Background(), Values{"values": input}); err == nil {
					t.Fatal("runtime accepted a collection outside its length bounds")
				}
				encoded, err := json.Marshal(Values{"values": input})
				if err != nil {
					t.Fatal(err)
				}
				if err := outputConformanceCheck(schema, outputConformanceJSON(t, encoded)); err == nil {
					t.Fatal("schema lost collection length bounds")
				}
			}
			encoded, err := json.Marshal(Values{"values": wrong})
			if err != nil {
				t.Fatal(err)
			}
			if err := outputConformanceCheck(schema, outputConformanceJSON(t, encoded)); err == nil {
				t.Fatal("schema lost collection child type")
			}
		})
	}
}

type outputConformanceReader struct{ names []string }

func (reader *outputConformanceReader) Get(name string) (any, error) {
	reader.names = append(reader.names, name)
	if name == "internal_caption" {
		return "Public caption", nil
	}
	return nil, fmt.Errorf("unexpected private read %s", name)
}

func TestOutputSchemaConformancePublicNamesAndKnownComputation(t *testing.T) {
	caption := StringField("caption")
	caption.Source = "internal_caption"
	writeOnly := StringField("private_password")
	writeOnly.WriteOnly = true
	hidden := StringField("private_owner")
	hidden.Hidden = true
	hidden.Default = func(context.Context) (any, error) { panic("hidden default is input-only") }
	computed := ComputedField("count", func(context.Context, ValueReader) (any, error) { return json.Number("9007199254740993"), nil })
	computed.Required, computed.Model = true, models.BigIntegerField("count")
	serializer := mustSerializer(t, Definition{Fields: []Field{caption, writeOnly, hidden, computed}})
	schema := outputConformanceSchema(t, serializer, false)
	properties := schema["properties"].(map[string]any)
	if len(properties) != 2 || properties["caption"] == nil || properties["count"] == nil {
		t.Fatalf("public field projection changed: %v", properties)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"internal_caption", "private_password", "private_owner"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("schema disclosed private descriptor %q", private)
		}
	}
	reader := &outputConformanceReader{}
	actual := outputConformanceRepresentation(t, serializer, schema, reader)
	if !reflect.DeepEqual(reader.names, []string{"internal_caption"}) || actual["count"] != json.Number("9007199254740993") {
		t.Fatalf("private read or integer precision changed: %v %#v", reader.names, actual)
	}
}

func TestOutputSchemaConformanceExplicitCustomOutput(t *testing.T) {
	minimum, maximum := 1, 2
	field := ComputedField("summary", func(context.Context, ValueReader) (any, error) {
		return map[string]any{"ids": []any{json.Number("9007199254740993")}, "labels": map[string]any{"a": "caption"}}, nil
	})
	field.Required = true
	field.OutputSchema = &WireSchema{Type: "object", Required: []string{"ids"}, Properties: map[string]WireSchema{
		"ids":    {Type: "array", Items: &WireSchema{Type: "integer"}, MinItems: &minimum, MaxItems: &maximum},
		"labels": {Type: "object", AdditionalProperties: &WireSchema{Type: "string"}},
	}}
	serializer := mustSerializer(t, Definition{Fields: []Field{field}})
	schema := outputConformanceSchema(t, serializer, false)
	outputConformanceRepresentation(t, serializer, schema, Values{})
	for _, raw := range []string{
		`{"summary":{}}`,
		`{"summary":{"ids":[]}}`,
		`{"summary":{"ids":[1,2,3]}}`,
		`{"summary":{"ids":["1"]}}`,
		`{"summary":{"ids":[1],"labels":{"a":1}}}`,
		`{"summary":{"ids":[1],"private":true}}`,
	} {
		if err := outputConformanceCheck(schema, outputConformanceJSON(t, []byte(raw))); err == nil {
			t.Fatalf("explicit output contract lost: %s", raw)
		}
	}
	custom := StringField("value")
	custom.Represent = func(_ context.Context, value any) (any, error) { return map[string]any{"source": value}, nil }
	custom.OutputSchema = &WireSchema{Type: "any"}
	serializer = mustSerializer(t, Definition{Fields: []Field{custom}})
	schema = outputConformanceSchema(t, serializer, false)
	outputConformanceRepresentation(t, serializer, schema, Values{"value": "accepted"})
	outputConformanceRepresentation(t, serializer, schema, Values{"value": nil})
	custom.Represent = nil
	serializer = mustSerializer(t, Definition{Fields: []Field{custom}})
	if document, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{}); err == nil || len(document) != 0 {
		t.Fatal("derivable builtin was overridden by any")
	}
}

// Unlike conformance examples above, these callbacks are tripwires and must
// never execute. Generation is descriptor inspection, not sample evaluation.
func TestOutputSchemaConformanceGenerationNeverSamplesCallbacks(t *testing.T) {
	field := StringField("custom", models.WithDefaultFunc("default", func() any { panic("model default") }), models.WithValidators(func(context.Context, any) error { panic("model validator") }))
	field.Required = true
	field.Default = func(context.Context) (any, error) { panic("field default") }
	field.Validate = func(context.Context, any) (any, error) { panic("field validator") }
	field.Represent = func(context.Context, any) (any, error) { panic("representation") }
	field.OutputSchema = &WireSchema{Type: "string"}
	computed := ComputedField("computed", func(context.Context, ValueReader) (any, error) { panic("compute") })
	computed.OutputSchema = &WireSchema{Type: "boolean"}
	serializer := mustSerializer(t, Definition{Fields: []Field{field, computed}, Validate: func(context.Context, Values) error { panic("serializer validator") }})
	outputConformanceSchema(t, serializer, false)
}
