package sqlcompiler

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestJSONMembershipEncodesLiteralsAndDropsUnusedPathArguments(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "JSON", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload")}}
	for _, test := range []struct {
		name, lookup, sql string
		value             any
		args              []any
	}{
		{"empty", "in", "FALSE", []any{}, []any{99}},
		{"nil_only", "in", "FALSE", []any{nil}, []any{99}},
		{"native_strings", "in", `JSON_key("payload", $2) IN ($3, $4)`, []any{"null", nil, models.JSONNull}, []any{99, "value", `"null"`, "null"}},
		{"range", "range", `JSON_key("payload", $2) BETWEEN $3 AND $4`, []string{"null", "null"}, []any{99, "value", `"null"`, `"null"`}},
		{"precise", "in", `JSON_key("payload", $2) IN ($3)`, []any{json.Number("9007199254740993")}, []any{99, "value", "9007199254740993"}},
		{"expression", "in", `JSON_key("payload", $2) IN (JSON_key("payload", $3))`, []any{db.Expression{Kind: "field", Name: "payload__value"}}, []any{99, "value", "value"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiler := Compiler{Dialect: alternateJSONPathDialect{}, Schema: schema, Args: []any{99}}
			statement, err := compiler.Predicate(db.Predicate{Field: "payload__value", Lookup: test.lookup, Value: test.value})
			if err != nil || statement != test.sql || !reflect.DeepEqual(compiler.Args, test.args) {
				t.Fatal(statement, compiler.Args, err)
			}
		})
	}
}
