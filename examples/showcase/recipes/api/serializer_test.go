// These tests execute serializer binding and JSON representation without a
// database. Nested input is not nested persistence: Save needs a client policy.
package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/models"
)

func serializer(t *testing.T, fields ...api.Field) *api.Serializer {
	t.Helper()
	value, err := api.New(api.Definition{Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestEveryScalarSerializerConstructor(t *testing.T) {
	cases := []struct {
		name      string
		field     api.Field
		good, bad any
	}{
		{"Scalar", api.Scalar(models.BinaryField("value")), []byte{0, 1, 255}, "not-bytes"},
		{"StringField", api.StringField("value", models.WithMinLength(2), models.WithMaxLength(8)), "example", "x"},
		{"IntegerField", api.IntegerField("value"), json.Number("9007199254740993"), "1.5"},
		{"BooleanField", api.BooleanField("value"), false, "perhaps"},
		{"DecimalField", api.DecimalField("value", 20, 2), "100000000000000001.25", "1.001"},
		{"JSONField", api.JSONField("value"), map[string]any{"number": json.Number("9007199254740993")}, make(chan int)},
		{"FloatField", api.FloatField("value"), 1.25, "NaN"},
		{"UUIDField", api.UUIDField("value"), "12345678-1234-1234-1234-123456789abc", "invalid"},
		{"URLField", api.URLField("value"), "https://example.test", "javascript:alert(1)"},
		{"EmailField", api.EmailField("value"), "developer@example.test", "Developer <developer@example.test>"},
		{"SlugField", api.SlugField("value"), "sample-value", "has spaces"},
		{"IPAddressField", api.IPAddressField("value"), "2001:db8::1", "999.1.1.1"},
		{"DateField", api.DateField("value"), "2026-01-02", "2026-02-30"},
		{"DateTimeField", api.DateTimeField("value"), "2026-01-02T03:04:05Z", "not-a-date"},
		{"TimeField", api.TimeField("value"), "03:04:05.123456", "25:00:00"},
		{"DurationField", api.DurationField("value"), "1h2m", "forever"},
		{"ChoiceField", api.ChoiceField(api.StringField("value"), models.Choice{Value: "draft", Label: "Draft"}), "draft", "published"},
	}
	for _, example := range cases {
		t.Run(example.name, func(t *testing.T) {
			s := serializer(t, example.field)
			values, err := s.Validate(t.Context(), api.Values{"value": example.good}, api.BindOptions{})
			if err != nil {
				t.Fatalf("valid input: %v", err)
			}
			output, err := s.Representation(t.Context(), values)
			if err != nil {
				t.Fatalf("public representation: %v", err)
			}
			if _, err := json.Marshal(output); err != nil {
				t.Fatal("representation is not JSON-safe")
			}
			if _, err := s.Validate(t.Context(), api.Values{"value": example.bad}, api.BindOptions{}); err == nil {
				t.Fatal("invalid input accepted")
			}
			if example.name == "IntegerField" && values["value"] != int64(9007199254740993) {
				t.Fatal("integer lost precision")
			}
			if example.name == "DecimalField" && output["value"] != example.good {
				t.Fatal("decimal must remain exact base-10 text")
			}
		})
	}
}

func TestListDictNestedAndComputedConstructors(t *testing.T) {
	child := serializer(t, api.IntegerField("count"))
	computed := api.ComputedField("label", func(_ context.Context, source api.ValueReader) (any, error) {
		return source.Get("title")
	})
	computed.OutputSchema = &api.WireSchema{Type: "string"}
	s := serializer(t,
		api.ListField("items", api.NestedField("item", child), 2),
		api.DictField("scores", api.IntegerField("score"), 2),
		api.NestedField("summary", child), api.StringField("title"), computed,
	)
	input := api.Values{
		"items": []any{api.Values{"count": 1}}, "scores": api.Values{"first": 2},
		"summary": api.Values{"count": 3}, "title": "Sample", "label": "untrusted",
	}
	values, err := s.Validate(t.Context(), input, api.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := values["label"]; exists {
		t.Fatal("computed input became writable")
	}
	output, err := s.Representation(t.Context(), values)
	if err != nil || output["label"] != "Sample" {
		t.Fatalf("computed output: %v", err)
	}
	input["items"] = []any{api.Values{"count": "bad"}}
	input["scores"] = api.Values{"first": "bad"}
	input["summary"] = api.Values{"count": 3, "unknown": true}
	_, err = s.Validate(t.Context(), input, api.BindOptions{})
	var invalid *api.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatal("expected indexed validation errors")
	}
	for _, path := range []string{"items.0.count", "scores.first", "summary.unknown"} {
		if len(invalid.Fields[path]) == 0 {
			t.Errorf("missing nested error path %s", path)
		}
	}
	input["items"] = []any{api.Values{"count": 1}, api.Values{"count": 2}, api.Values{"count": 3}}
	input["scores"] = api.Values{"first": 2}
	input["summary"] = api.Values{"count": 3}
	_, err = s.Validate(t.Context(), input, api.BindOptions{})
	if !errors.As(err, &invalid) || len(invalid.Fields["items"]) != 1 || invalid.Fields["items"][0].Code != "length" {
		t.Fatal("bounded list overflow needs a collection-length error")
	}
}

func TestReadWriteHiddenSourceNullAndPartialRules(t *testing.T) {
	id := api.IntegerField("id")
	id.ReadOnly = true
	inputOnly := api.StringField("input_only")
	inputOnly.WriteOnly = true
	owner := api.StringField("owner")
	owner.Hidden = true
	defaults := 0
	owner.Default = func(context.Context) (any, error) { defaults++; return "trusted-demo-owner", nil }
	name := api.StringField("display_name")
	name.Source = "name"
	note := api.StringField("note", models.Nullable, models.Optional)
	s := serializer(t, id, inputOnly, owner, name, note)
	values, err := s.Validate(t.Context(), api.Values{
		"id": "forged", "input_only": "fictional input", "owner": "forged", "display_name": "Example",
	}, api.BindOptions{})
	if err != nil || values["owner"] != "trusted-demo-owner" || values["name"] != "Example" || defaults != 1 {
		t.Fatalf("field directions/default/source: %v", err)
	}
	if _, exists := values["id"]; exists {
		t.Fatal("read-only input leaked into writable values")
	}
	values["id"] = 42 // Trusted application output, not accepted input.
	output, err := s.Representation(t.Context(), values)
	if err != nil || output["display_name"] != "Example" {
		t.Fatalf("projection: %v", err)
	}
	for _, hidden := range []string{"input_only", "owner", "name"} {
		if _, exists := output[hidden]; exists {
			t.Fatalf("non-public source %s leaked", hidden)
		}
	}
	partial, err := s.Validate(t.Context(), api.Values{"note": nil}, api.BindOptions{Partial: true})
	if err != nil || len(partial) != 1 || defaults != 1 {
		t.Fatalf("partial must preserve omission and skip defaults: %v", err)
	}
	if value, exists := partial["note"]; !exists || value != nil {
		t.Fatal("explicit null collapsed into omission")
	}
	if _, err := s.Validate(t.Context(), api.Values{"is_admin": true}, api.BindOptions{Partial: true}); err == nil {
		t.Fatal("unknown input must fail closed")
	}
}

func TestModelSerializerNeedsProjectionAndRelationPolicies(t *testing.T) {
	schema := models.Schema{AppLabel: "demo", Name: "Record", Fields: []models.Field{
		models.BigAutoField("id"), models.CharField("title"), models.CharField("private_note"),
		models.ForeignKeyField("category_id", models.Relation{Target: "demo.Category", OnDelete: models.Protect}),
	}}
	if _, err := api.FromModel(schema, api.ModelOptions{}); err == nil {
		t.Fatal("implicit public model fields accepted")
	}
	if _, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"category_id"}}); err == nil {
		t.Fatal("relation accepted without explicit input/output policy")
	}
	// ID 1 is a fixed public educational category, not a customer record. Real
	// resolvers must query current actor-scoped rows and recheck before writing.
	s, err := api.FromModel(schema, api.ModelOptions{
		Fields: []string{"id", "title", "category_id"},
		ResolveRelation: func(_ context.Context, _ models.Field, value any) (any, error) {
			if value != int64(1) {
				return nil, models.Invalid("invalid_choice", "Select a public demo category.")
			}
			return value, nil
		},
		RepresentRelation: func(_ context.Context, _ models.Field, value any) (any, error) {
			if value != int64(1) {
				return nil, errors.New("demo category is not public")
			}
			return value, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := s.Validate(t.Context(), api.Values{"id": 100, "title": "Sample", "category_id": int64(1)}, api.BindOptions{})
	if err != nil || len(values) != 2 {
		t.Fatalf("model input: %v", err)
	}
	output, err := s.Representation(t.Context(), api.Values{"id": 42, "title": "Sample", "category_id": int64(1), "private_note": "not public"})
	if err != nil || len(output) != 3 {
		t.Fatalf("model output: %v", err)
	}
	if _, err := s.Validate(t.Context(), api.Values{"title": "Sample", "category_id": int64(999)}, api.BindOptions{}); err == nil {
		t.Fatal("unknown relation accepted")
	}
}

func TestCustomValidationRepresentationAndOutputMetadata(t *testing.T) {
	field := api.StringField("code")
	field.Validate = func(_ context.Context, value any) (any, error) {
		if !strings.HasPrefix(value.(string), "DEMO-") {
			return nil, models.Invalid("prefix", "Use a DEMO- code.")
		}
		return value, nil
	}
	field.Represent = func(_ context.Context, value any) (any, error) { return strings.ToLower(value.(string)), nil }
	field.OutputSchema = &api.WireSchema{Type: "string"}
	s := serializer(t, field)
	values, err := s.Validate(t.Context(), api.Values{"code": "DEMO-42"}, api.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := s.Representation(t.Context(), values); err != nil || output["code"] != "demo-42" {
		t.Fatalf("custom representation: %v", err)
	}
	if _, err := s.Validate(t.Context(), api.Values{"code": "WRONG"}, api.BindOptions{}); err == nil {
		t.Fatal("custom validator ignored")
	}
	document, err := s.OutputSchema(t.Context(), api.OutputSchemaOptions{})
	if err != nil || !json.Valid(document) {
		t.Fatalf("explicit custom wire metadata: %v", err)
	}
	// WireSchema is metadata, not an additional runtime validator or input schema.
	// Unknown computed outputs must declare their wire shape before projection.
	computed := api.ComputedField("value", func(context.Context, api.ValueReader) (any, error) { return "example", nil })
	if _, err := serializer(t, computed).OutputSchema(t.Context(), api.OutputSchemaOptions{}); !errors.Is(err, api.ErrOutputSchema) {
		t.Fatal("unspecified computed wire shape was invented")
	}
}

func TestSavePolicyBoundaryIsExplicitAndValidationDoesNotWrite(t *testing.T) {
	s := serializer(t, api.IntegerField("count"))
	writes := 0
	// This is an in-memory transaction boundary probe, NOT database atomicity.
	policy := api.SavePolicy{
		Atomic: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		Write:  func(_ context.Context, values api.Values) (api.ValueReader, error) { writes++; return values, nil },
	}
	if _, err := s.Save(t.Context(), api.Values{"count": "bad"}, api.BindOptions{}, policy); err == nil || writes != 0 {
		t.Fatal("invalid values reached write policy")
	}
	if _, err := s.Save(t.Context(), api.Values{"count": 1}, api.BindOptions{}, policy); err != nil || writes != 1 {
		t.Fatalf("explicit write policy: %v", err)
	}
	boundaryFailure := errors.New("sample boundary failed")
	policy.Atomic = func(context.Context, func(context.Context) error) error { return boundaryFailure }
	if result, err := s.Save(t.Context(), api.Values{"count": 2}, api.BindOptions{}, policy); result != nil || !errors.Is(err, boundaryFailure) || writes != 1 {
		t.Fatal("failed boundary claimed success")
	}
}
