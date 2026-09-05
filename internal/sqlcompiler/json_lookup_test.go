package sqlcompiler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type alternateJSONDialect struct{ numberedDialect }

func (alternateJSONDialect) JSONLookup(operation, left, right string) (string, error) {
	return "CUSTOM_" + operation + "(" + left + ", " + right + ")", nil
}

func TestJSONContainmentDispatchesThroughPublicDialect(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "JSON", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload"), models.TextField("text")}}
	key := `x') OR TRUE --`
	for _, test := range []struct {
		lookup string
		value  any
		bound  any
	}{
		{"contains", map[string]any{"large": json.Number("9007199254740993")}, `{"large":9007199254740993}`},
		{"contained_by", json.RawMessage(`{"large":9007199254740993}`), `{"large":9007199254740993}`},
		{"contains", "null", `"null"`},
		{"contains", models.JSONNull, "null"},
		{"has_key", key, key},
		{"has_keys", [2]string{"a", key}, []string{"a", key}},
		{"has_any_keys", []any{"a", key}, []string{"a", key}},
		{"has_keys", []string(nil), []string{}},
	} {
		for _, field := range []string{"payload", "related__payload"} {
			t.Run(test.lookup+"/"+field, func(t *testing.T) {
				compiler := Compiler{Dialect: alternateJSONDialect{}, Schema: schema, Alias: "root", Joins: map[string]db.Join{"related": {Alias: "joined", Schema: schema}}, Args: []any{99}}
				statement, err := compiler.Predicate(db.Predicate{Field: field, Lookup: test.lookup, Value: test.value})
				alias := "root"
				if field != "payload" {
					alias = "joined"
				}
				want := `CUSTOM_` + test.lookup + `("` + alias + `"."payload", $2)`
				if err != nil || statement != want || !reflect.DeepEqual(compiler.Args, []any{99, test.bound}) || strings.Contains(statement, key) {
					t.Fatal(statement, compiler.Args, err)
				}
			})
		}
	}
	compiler := Compiler{Dialect: numberedDialect{}, Schema: schema}
	if _, err := compiler.Predicate(db.Predicate{Field: "payload", Lookup: "contains", Value: map[string]any{}}); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 0 {
		t.Fatal("missing JSON capability did not fail before binding", compiler.Args, err)
	}
	compiler = Compiler{Dialect: alternateJSONDialect{}, Schema: schema}
	if statement, err := compiler.Predicate(db.Predicate{Field: "text", Lookup: "contains", Value: "%_"}); err != nil || strings.Contains(statement, "CUSTOM_") || !strings.Contains(statement, " LIKE ") {
		t.Fatal("text containment was overridden", statement, err)
	}
	for _, predicate := range []db.Predicate{
		{Field: "payload", Lookup: "contains", Value: nil},
		{Field: "payload", Lookup: "contained_by", Value: json.RawMessage(`{"invalid":`)},
		{Field: "payload", Lookup: "has_key", Value: 1},
		{Field: "payload", Lookup: "has_key", Value: "bad\x00key"},
		{Field: "payload", Lookup: "has_keys", Value: "not a list"},
		{Field: "payload", Lookup: "has_keys", Value: []any{"valid", 1}},
		{Field: "payload", Lookup: "has_keys", Value: make([]string, 1025)},
		{Field: "text", Lookup: "has_key", Value: "key"},
	} {
		compiler := Compiler{Dialect: alternateJSONDialect{}, Schema: schema}
		if _, err := compiler.Predicate(predicate); err == nil || len(compiler.Args) != 0 {
			t.Fatal("invalid JSON lookup accepted or partly bound", predicate.Lookup, err)
		}
	}
}
