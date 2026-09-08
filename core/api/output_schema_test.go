package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

func outputSchemaDocument(t *testing.T, serializer *Serializer, options OutputSchemaOptions) map[string]any {
	t.Helper()
	encoded, err := serializer.OutputSchema(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOutputSchemaDirectionsRequiredAndActualWireTypes(t *testing.T) {
	private := StringField("password")
	private.WriteOnly = true
	hidden := StringField("owner")
	hidden.Hidden = true
	hidden.Default = func(context.Context) (any, error) { panic("hidden default must not run") }
	readonly := IntegerField("id")
	readonly.ReadOnly = true
	optional := StringField("optional")
	optional.Required = false
	alias := StringField("name")
	alias.Source = "private_column"
	serializer := mustSerializer(t, Definition{Fields: []Field{
		readonly, private, hidden, alias, optional,
		DecimalField("price", 24, 8), DateField("day"), TimeField("clock"), DateTimeField("instant"), DurationField("elapsed"),
		BooleanField("enabled"), FloatField("score"), JSONField("json"), Scalar(models.NewField("bytes", models.Binary)),
	}})
	document := outputSchemaDocument(t, serializer, OutputSchemaOptions{})
	if document["$schema"] != outputSchemaDialect || document["type"] != "object" || document["additionalProperties"] != false {
		t.Fatal("invalid document root", document)
	}
	properties := document["properties"].(map[string]any)
	for _, name := range []string{"password", "owner", "private_column"} {
		if _, found := properties[name]; found {
			t.Fatal("private field/source leaked", name)
		}
	}
	for name, kind := range map[string]string{"id": "integer", "name": "string", "optional": "string", "price": "string", "day": "string", "clock": "string", "instant": "string", "elapsed": "string", "enabled": "boolean", "score": "number", "bytes": "string"} {
		property := properties[name].(map[string]any)
		if !reflect.DeepEqual(property["type"], []any{kind, "null"}) || property["format"] != nil {
			t.Fatal("wire type/nullability/format mismatch", name, property)
		}
	}
	if !reflect.DeepEqual(properties["json"], map[string]any{}) {
		t.Fatal("declared JSON field did not permit any JSON value")
	}
	required := document["required"].([]any)
	if !reflect.DeepEqual(required, []any{"bytes", "clock", "day", "elapsed", "enabled", "id", "instant", "json", "name", "price", "score"}) {
		t.Fatal("required fields confused with direction or optional metadata", required)
	}
	if _, found := outputSchemaDocument(t, serializer, OutputSchemaOptions{Redactable: true})["required"]; found {
		t.Fatal("redactable root retained required fields")
	}
}

func TestOutputSchemaNestedCollectionsAndBlankStrings(t *testing.T) {
	child := mustSerializer(t, Definition{Fields: []Field{StringField("name")}})
	array := ListField("items", NestedField("item", child), 3)
	array.MinItems = 1
	dictionary := DictField("lookup", IntegerField("item"), 4)
	dictionary.MinItems = 2
	blank := IntegerField("blank", models.Optional)
	serializer := mustSerializer(t, Definition{Fields: []Field{array, dictionary, blank}})
	document := outputSchemaDocument(t, serializer, OutputSchemaOptions{Redactable: true})
	properties := document["properties"].(map[string]any)
	items := properties["items"].(map[string]any)
	if items["minItems"] != float64(1) || items["maxItems"] != float64(3) || !reflect.DeepEqual(items["type"], []any{"array", "null"}) {
		t.Fatal("list shape or bounds", items)
	}
	item := items["items"].(map[string]any)
	if !reflect.DeepEqual(item["type"], []any{"object", "null"}) || !reflect.DeepEqual(item["required"], []any{"name"}) || item["additionalProperties"] != false {
		t.Fatal("root redactability widened nested serializer", item)
	}
	lookup := properties["lookup"].(map[string]any)
	if lookup["minProperties"] != float64(2) || lookup["maxProperties"] != float64(4) || lookup["minItems"] != nil || lookup["maxItems"] != nil {
		t.Fatal("dictionary length described as array size", lookup)
	}
	if !reflect.DeepEqual(lookup["additionalProperties"].(map[string]any)["type"], []any{"integer", "null"}) {
		t.Fatal("dictionary values lost nullability")
	}
	if !reflect.DeepEqual(properties["blank"], map[string]any{"anyOf": []any{map[string]any{"type": []any{"integer", "null"}}, map[string]any{"const": ""}}}) {
		t.Fatal("legacy blank scalar output absent", properties["blank"])
	}
}

type outputSchemaOpaque struct{}

func (outputSchemaOpaque) String() string               { panic("String must not run") }
func (outputSchemaOpaque) MarshalJSON() ([]byte, error) { panic("MarshalJSON must not run") }
func (outputSchemaOpaque) Encode(any) (any, error)      { panic("Encode must not run") }
func (outputSchemaOpaque) Decode(any) (any, error)      { panic("Decode must not run") }

func TestOutputSchemaDoesNotExecuteRuntimeMetadata(t *testing.T) {
	tripwire := func(context.Context, any) (any, error) { panic("runtime callback must not run") }
	field := IntegerField("value")
	field.Model.Default, field.Model.Min, field.Model.Max = outputSchemaOpaque{}, outputSchemaOpaque{}, outputSchemaOpaque{}
	field.Model.Choices = []models.Choice{{Value: outputSchemaOpaque{}, Label: "private choice label"}}
	field.Model.DefaultFunc = func() any { panic("default must not run") }
	field.Model.Validators = []models.Validator{func(context.Context, any) error { panic("validator must not run") }}
	field.Validate = tripwire
	field.Compute = func(context.Context, ValueReader) (any, error) { panic("compute must not run") }
	field.ReadOnly = true
	field.Label, field.HelpText = "private label", "private help"
	custom := ComputedField("custom", func(context.Context, ValueReader) (any, error) { panic("custom compute must not run") })
	custom.Represent = tripwire
	custom.OutputSchema = &WireSchema{Type: "string", Format: "uuid"}
	serializer := mustSerializer(t, Definition{Fields: []Field{field, custom}, Validate: func(context.Context, Values) error { panic("serializer validator must not run") }})
	encoded, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil || bytes.Contains(encoded, []byte("private")) {
		t.Fatal("runtime metadata executed or leaked", string(encoded), err)
	}
}

func TestOutputSchemaCustomAssertionsAndMissingMetadata(t *testing.T) {
	compute := func(context.Context, ValueReader) (any, error) { panic("not called for schema generation") }
	unknown := []Field{
		ComputedField("value", compute),
		{Name: "value", Model: models.NewField("value", models.Array)},
		{Name: "value", Model: models.Field{Name: "value", Kind: models.Custom, Codec: outputSchemaOpaque{}}},
		{Name: "value", Model: models.NewField("value", models.HStore)},
		{Name: "value", Validate: func(context.Context, any) (any, error) { panic("not called") }},
	}
	for _, field := range unknown {
		serializer := mustSerializer(t, Definition{Fields: []Field{field}})
		if value, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
			t.Fatal("unknown output silently inferred", field.Model.Kind, string(value), err)
		}
		field.OutputSchema = &WireSchema{Type: "any"}
		serializer = mustSerializer(t, Definition{Fields: []Field{field}})
		if property := outputSchemaDocument(t, serializer, OutputSchemaOptions{})["properties"].(map[string]any)["value"]; !reflect.DeepEqual(property, map[string]any{}) {
			t.Fatal("explicit any not compiled to empty schema", property)
		}
	}
	known := StringField("value")
	known.OutputSchema = &WireSchema{Type: "integer"}
	serializer := mustSerializer(t, Definition{Fields: []Field{known}})
	if value, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
		t.Fatal("known wire output was replaced by contradictory assertion", value, err)
	}
	known.Represent = func(context.Context, any) (any, error) { panic("not called") }
	serializer = mustSerializer(t, Definition{Fields: []Field{known}})
	property := outputSchemaDocument(t, serializer, OutputSchemaOptions{})["properties"].(map[string]any)["value"].(map[string]any)
	if !reflect.DeepEqual(property["type"], []any{"integer", "null"}) {
		t.Fatal("Represent precedence or mandatory outer null union lost", property)
	}
}

func TestOutputSchemaConstructorFreezesCompleteCustomGraph(t *testing.T) {
	minimum, maximum := 0, 3
	metadata := &WireSchema{Type: "object", Required: []string{"value"}, Properties: map[string]WireSchema{
		"value": {Type: "array", Items: &WireSchema{Type: "string", Nullable: true}, MinItems: &minimum, MaxItems: &maximum},
	}, AdditionalProperties: &WireSchema{Type: "any"}}
	field := ComputedField("custom", func(context.Context, ValueReader) (any, error) { panic("not called") })
	field.OutputSchema = metadata
	serializer := mustSerializer(t, Definition{Fields: []Field{field}})
	want, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil {
		t.Fatal(err)
	}
	metadata.Required[0] = "other"
	metadata.Properties["value"].Items.Type = "integer"
	metadata.Properties["new"] = WireSchema{Type: "integer"}
	metadata.AdditionalProperties.Type = "number"
	minimum, maximum = 2, 10
	got, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("caller metadata changed frozen schema", string(got), err)
	}
	got[0] = 'x'
	again, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil || !bytes.Equal(again, want) {
		t.Fatal("returned schema bytes share storage", err)
	}
}

func TestOutputSchemaInvalidCustomMetadataFailsAtConstruction(t *testing.T) {
	negative := -1
	minimum, maximum := 2, 1
	cycle := &WireSchema{Type: "array"}
	cycle.Items = cycle
	for _, metadata := range []*WireSchema{
		{}, {Type: "unknown"}, {Type: "string", Properties: map[string]WireSchema{}},
		{Type: "object", Required: []string{"absent"}}, {Type: "object", Properties: map[string]WireSchema{"key": {Type: "string"}}, Required: []string{"key", "key"}},
		{Type: "object", Properties: map[string]WireSchema{"bad\x00key": {Type: "string"}}},
		{Type: "object", Properties: map[string]WireSchema{strings.Repeat("x", outputSchemaMaxName+1): {Type: "string"}}},
		{Type: "object", Properties: map[string]WireSchema{"invalid\xff": {Type: "string"}}},
		{Type: "array"}, {Type: "array", Items: &WireSchema{Type: "any"}, MinItems: &negative},
		{Type: "array", Items: &WireSchema{Type: "any"}, MinItems: &minimum, MaxItems: &maximum},
		{Type: "number", Format: "uuid"}, {Type: "string", Format: "custom-extension"},
		{Type: "any", Items: &WireSchema{Type: "string"}}, cycle,
	} {
		field := ComputedField("value", func(context.Context, ValueReader) (any, error) { panic("not called") })
		field.OutputSchema = metadata
		if serializer, err := New(Definition{Fields: []Field{field}}); serializer != nil || err != ErrOutputSchema {
			t.Fatal("invalid custom metadata survived New", metadata.Type, err)
		}
	}
}

type outputSchemaContext struct {
	context.Context
	err func() error
}

func (ctx *outputSchemaContext) Err() error { return ctx.err() }

func TestOutputSchemaContextBoundariesAndEntrySnapshot(t *testing.T) {
	serializer := mustSerializer(t, Definition{Fields: []Field{StringField("value")}})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Time{})
	defer stop()
	var typedNil *outputSchemaContext
	for _, test := range []struct {
		ctx  context.Context
		want error
	}{
		{nil, ErrOutputSchema}, {typedNil, ErrOutputSchema}, {canceled, context.Canceled}, {expired, context.DeadlineExceeded},
		{&outputSchemaContext{Context: context.Background(), err: func() error { panic("private context panic") }}, ErrOutputSchema},
		{&outputSchemaContext{Context: context.Background(), err: func() error { return errors.New("private context error") }}, ErrOutputSchema},
	} {
		if value, err := serializer.OutputSchema(test.ctx, OutputSchemaOptions{}); value != nil || err != test.want {
			t.Fatal("invalid context returned schema or unsafe error", string(value), err)
		}
	}
	calls := 0
	finalCancel := &outputSchemaContext{Context: context.Background(), err: func() error {
		calls++
		if calls == 2 {
			return context.Canceled
		}
		return nil
	}}
	if value, err := serializer.OutputSchema(finalCancel, OutputSchemaOptions{}); value != nil || err != context.Canceled || calls != 2 {
		t.Fatal("final cancellation returned encoded output", value, err, calls)
	}
	child := mustSerializer(t, Definition{Fields: []Field{StringField("original")}})
	parent := mustSerializer(t, Definition{Fields: []Field{NestedField("child", child)}})
	replacement := mustSerializer(t, Definition{Fields: []Field{StringField("replacement")}})
	initial, err := parent.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mutate := &outputSchemaContext{Context: context.Background(), err: func() error {
		*child = *replacement
		*parent = *replacement
		return nil
	}}
	value, err := parent.OutputSchema(mutate, OutputSchemaOptions{})
	if err != nil || !bytes.Equal(value, initial) {
		t.Fatal("context callback retargeted captured declaration graph", string(value), err)
	}
	if later := outputSchemaDocument(t, parent, OutputSchemaOptions{}); later["properties"].(map[string]any)["replacement"] == nil {
		t.Fatal("next operation did not observe deliberate handle replacement")
	}
}

func TestOutputSchemaBoundsAndDeterministicConcurrency(t *testing.T) {
	leaf := StringField("value")
	for range outputSchemaMaxDepth - 1 {
		leaf = ListField("value", leaf, 1)
	}
	serializer := mustSerializer(t, Definition{Fields: []Field{leaf}})
	if _, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{}); err != nil {
		t.Fatal("supported depth rejected", err)
	}
	tooDeep := mustSerializer(t, Definition{Fields: []Field{ListField("value", leaf, 1)}})
	if value, err := tooDeep.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
		t.Fatal("overdeep output shape accepted", value, err)
	}
	cycle := &Serializer{}
	cycle.fields = []Field{NestedField("self", cycle)}
	if value, err := cycle.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
		t.Fatal("recursive serializer emitted a partial schema", value, err)
	}
	large := make([]Field, outputSchemaMaxNodes)
	for i := range large {
		large[i] = StringField(fmt.Sprintf("field_%d", i))
	}
	tooWide := mustSerializer(t, Definition{Fields: large})
	if value, err := tooWide.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
		t.Fatal("overwide output shape accepted", len(value), err)
	}
	metadata := &WireSchema{Type: "object", Properties: map[string]WireSchema{}}
	for i := range 1024 {
		name := fmt.Sprintf("%04d", i) + strings.Repeat("\x01", 240)
		metadata.Properties[name] = WireSchema{Type: "string"}
	}
	custom := ComputedField("custom", func(context.Context, ValueReader) (any, error) { panic("not called") })
	custom.OutputSchema = metadata
	tooEncoded := mustSerializer(t, Definition{Fields: []Field{custom}})
	if value, err := tooEncoded.OutputSchema(context.Background(), OutputSchemaOptions{}); value != nil || err != ErrOutputSchema {
		t.Fatal("JSON escaping bypassed encoded output bound", len(value), err)
	}
	serializer = mustSerializer(t, Definition{Fields: []Field{StringField("z"), StringField("a")}})
	want, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 8 {
				got, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
				if err != nil || !bytes.Equal(got, want) {
					t.Error("concurrent output not deterministic", string(got), err)
				}
			}
		})
	}
	group.Wait()
}

func TestOutputSchemaHiddenFieldsConsumeSharedTraversalBudget(t *testing.T) {
	fields := []Field{StringField("visible")}
	for i := range 100 {
		field := StringField(fmt.Sprintf("private_%d", i))
		field.Hidden = true
		field.Default = func(context.Context) (any, error) { panic("private metadata must not run") }
		fields = append(fields, field)
	}
	child := mustSerializer(t, Definition{Fields: fields})
	for _, count := range []int{80, 200} {
		parents := make([]Field, count)
		for i := range parents {
			parents[i] = NestedField(fmt.Sprintf("entry_%d", i), child)
		}
		serializer := mustSerializer(t, Definition{Fields: parents})
		value, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
		if count == 80 {
			if err != nil || bytes.Contains(value, []byte("private_")) {
				t.Fatal("bounded nested projection exposed hidden metadata or failed", err)
			}
		} else if value != nil || err != ErrOutputSchema {
			t.Fatal("repeated hidden fields bypassed shared traversal bound", len(value), err)
		}
	}
}

func TestOutputSchemaConstructorSharesOnlyOptionalMetadataBudget(t *testing.T) {
	metadata := &WireSchema{Type: "object", Properties: map[string]WireSchema{}}
	for i := range 128 {
		metadata.Properties[fmt.Sprintf("field_%d", i)] = WireSchema{Type: "string"}
	}
	// Each reused graph requires 257 node/name visits. Reuse must not cause
	// unbounded copying; every occurrence owns an independent frozen graph.
	for _, count := range []int{1, 63, 64} {
		fields := make([]Field, count)
		for i := range fields {
			fields[i] = ComputedField(fmt.Sprintf("value_%d", i), func(context.Context, ValueReader) (any, error) { panic("not called") })
			fields[i].OutputSchema = metadata
		}
		serializer, err := New(Definition{Fields: fields})
		if count < 64 {
			if err != nil || serializer == nil {
				t.Fatal("bounded metadata rejected at construction", count, err)
			}
		} else if serializer != nil || err != ErrOutputSchema {
			t.Fatal("many metadata copies bypassed one constructor budget", err)
		}
	}
	fields := make([]Field, outputSchemaMaxNodes+1)
	for i := range fields {
		fields[i] = StringField(fmt.Sprintf("value_%d", i))
	}
	if _, err := New(Definition{Fields: fields}); err != nil {
		t.Fatal("optional metadata budget restricted existing no-metadata constructor", err)
	}
	// The same budget also crosses existing Element recursion.
	item := StringField("item")
	item.Represent = func(context.Context, any) (any, error) { panic("not called") }
	item.OutputSchema = metadata
	fields = make([]Field, 64)
	for i := range fields {
		fields[i] = ListField(fmt.Sprintf("list_%d", i), item, 1)
	}
	if serializer, err := New(Definition{Fields: fields}); serializer != nil || err != ErrOutputSchema {
		t.Fatal("element recursion reset optional metadata budget", err)
	}
}

func FuzzOutputSchemaMetadata(f *testing.F) {
	for _, input := range []string{
		`{"Type":"any"}`, `{"Type":"string","Nullable":true,"Format":"uuid"}`,
		`{"Type":"array","Items":{"Type":"integer"},"MinItems":0,"MaxItems":2}`,
		`{"Type":"object","Properties":{"value":{"Type":"string"}},"Required":["value"]}`,
		`{"Type":"object","Required":["absent"]}`, `{"Type":"unknown"}`,
	} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			return
		}
		var metadata WireSchema
		if json.Unmarshal([]byte(input), &metadata) != nil {
			return
		}
		field := StringField("value")
		field.Represent = func(context.Context, any) (any, error) { panic("schema generation executed a callback") }
		field.OutputSchema = &metadata
		serializer, err := New(Definition{Fields: []Field{field}})
		if err != nil {
			if serializer != nil || err != ErrOutputSchema {
				t.Fatal("metadata construction returned unexpected partial state", err)
			}
			return
		}
		document, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
		if err != nil {
			if document != nil || err != ErrOutputSchema {
				t.Fatal("metadata generation returned unexpected partial output", err)
			}
			return
		}
		if !json.Valid(document) || len(document) > outputSchemaMaxBytes {
			t.Fatal("schema document is invalid or unbounded")
		}
		again, err := serializer.OutputSchema(context.Background(), OutputSchemaOptions{})
		if err != nil || !bytes.Equal(document, again) {
			t.Fatal("schema generation is not deterministic", err)
		}
	})
}
