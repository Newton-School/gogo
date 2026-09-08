package migrations

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestSchemaTransitionDefaultsDoNotMutateHistory(t *testing.T) {
	probe := &transitionCapture{}
	schema := testSchema()
	schema.Fields = append(schema.Fields, models.JSONField("payload", models.WithDefault(map[string]any{"value": "original"})))
	e := Executor{Editor: probe, Migrations: []Migration{{App: "shop", Name: "0001", Operations: []Operation{CreateModel(schema)}}}}
	before, err := e.Migrations[0].Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.withHistoricalSchemas("shop.0001", false); err != nil {
		t.Fatal(err)
	}
	for _, item := range probe.after {
		field, exists := item.Field("payload")
		if exists {
			field.Default.(map[string]any)["value"] = "changed by provider"
		}
	}
	after, err := e.Migrations[0].Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("transition snapshot default mutation changed historical migration checksum")
	}
}

func TestSchemaTransitionValuesPreserveTypesAndDetachContainers(t *testing.T) {
	type namedMap map[string][]byte
	pointer := 12
	original := map[string]any{"named": namedMap{"bytes": {1, 2}}, "array": [1][]string{{"original"}}, "pointer": &pointer, "raw": json.RawMessage(`{"ok":true}`), "nil": []int(nil), "empty": []int{}}
	copy, err := detachMetadataValue(original)
	if err != nil || !reflect.DeepEqual(copy, original) {
		t.Fatal(copy, err)
	}
	detached := copy.(map[string]any)
	detached["named"].(namedMap)["bytes"][0] = 9
	detached["array"].([1][]string)[0][0] = "changed"
	*detached["pointer"].(*int) = 88
	detached["raw"].(json.RawMessage)[0] = 'x'
	if original["named"].(namedMap)["bytes"][0] != 1 || original["array"].([1][]string)[0][0] != "original" || pointer != 12 || original["raw"].(json.RawMessage)[0] != '{' {
		t.Fatal("plain metadata remained aliased", original)
	}
	field := models.JSONField("payload")
	field.Choices = []models.Choice{{Value: original, Label: "choice"}}
	field.Element = &models.Field{Default: original, Min: &pointer, Max: &pointer}
	schemas := []models.Schema{{Fields: []models.Field{field}}}
	if err := detachTransitionValues(schemas); err != nil {
		t.Fatal(err)
	}
	schemas[0].Fields[0].Choices[0].Value.(map[string]any)["new"] = "not original"
	schemas[0].Fields[0].Element.Default.(map[string]any)["other"] = "not original"
	*schemas[0].Fields[0].Element.Min.(*int) = 99
	if original["new"] != nil || original["other"] != nil || pointer != 12 {
		t.Fatal("nested field metadata remained aliased", original)
	}
}

func TestSchemaTransitionValuesRejectUnboundedContainers(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for _, value := range []any{cyclic, make([]int, 8193)} {
		if _, err := detachMetadataValue(value); err == nil {
			t.Fatal("unbounded value accepted")
		}
	}
}

func TestSchemaTransitionValuesPreserveNamedPointers(t *testing.T) {
	type namedPointer *int
	original := 7
	pointer := namedPointer(&original)
	for _, value := range []any{pointer, map[string]namedPointer{"number": pointer}, []namedPointer{pointer}} {
		copy, err := detachMetadataValue(value)
		if err != nil || !reflect.DeepEqual(copy, value) || reflect.TypeOf(copy) != reflect.TypeOf(value) {
			t.Fatal("named pointer type changed", copy, err)
		}
		switch v := copy.(type) {
		case namedPointer:
			*v = 99
		case map[string]namedPointer:
			*v["number"] = 99
		case []namedPointer:
			*v[0] = 99
		}
		if original != 7 {
			t.Fatal("named pointer remained aliased")
		}
	}
}
