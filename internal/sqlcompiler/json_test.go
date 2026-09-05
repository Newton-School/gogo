package sqlcompiler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestJSONLookupUsesRootAndJoinedFieldMetadata(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "JSON", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload", models.Nullable), models.TextField("text", models.Nullable)}}
	for _, test := range []struct {
		name      string
		predicate db.Predicate
		fragment  string
		args      []any
	}{
		{"root_null", db.Predicate{Field: "payload", Value: nil}, `"root"."payload" = $1`, []any{"null"}},
		{"joined_null", db.Predicate{Field: "relation__payload", Value: nil}, `"related"."payload" = $1`, []any{"null"}},
		{"text_null", db.Predicate{Field: "text", Value: nil}, `"root"."text" IS NULL`, nil},
		{"json_string", db.Predicate{Field: "payload", Value: "null"}, `"root"."payload" = $1`, []any{`"null"`}},
		{"explicit_null", db.Predicate{Field: "payload", Value: models.JSONNull}, `"root"."payload" = $1`, []any{"null"}},
		{"raw", db.Predicate{Field: "payload", Value: json.RawMessage(`{"integer":9007199254740993}`)}, `"root"."payload" = $1`, []any{`{"integer":9007199254740993}`}},
		{"sql_null", db.Predicate{Field: "payload", Lookup: "isnull", Value: true}, `"root"."payload" IS NULL`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiler := Compiler{Dialect: numberedDialect{}, Schema: schema, Alias: "root", Joins: map[string]db.Join{"relation": {Alias: "related", Schema: schema}}}
			statement, err := compiler.Predicate(test.predicate)
			if err != nil || !strings.Contains(statement, test.fragment) || !reflect.DeepEqual(compiler.Args, test.args) {
				t.Fatal(statement, compiler.Args, err)
			}
		})
	}
}
