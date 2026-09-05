package sqlcompiler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type alternateJSONPathDialect struct{ alternateJSONDialect }

type unusedJSONPathCodec struct{}

func (unusedJSONPathCodec) Encode(any) (any, error) { panic("custom path encoding must not run") }
func (unusedJSONPathCodec) Decode(any) (any, error) { panic("custom path decoding must not run") }

func (alternateJSONPathDialect) JSONExtract(left, right string, kind db.JSONPathKind, text bool) (string, error) {
	name := "JSON_"
	if text {
		name = "TEXT_"
	}
	return name + string(kind) + "(" + left + ", " + right + ")", nil
}

func TestJSONPathsUseBoundComponentsAndResolvedScopedJoinMetadata(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "JSON", Fields: []models.Field{models.BigAutoField("id"), models.JSONField("payload"), models.TextField("name")}}
	for _, test := range []struct {
		field, lookup, sql string
		value              any
		args               []any
	}{
		{"payload__owner", "exact", `JSON_key("root"."payload", $1) = $2`, "Bob", []any{"owner", `"Bob"`}},
		{"payload__owner__name", "exact", `JSON_path("root"."payload", $1) = $2`, "Bob", []any{[]string{"owner", "name"}, `"Bob"`}},
		{"related__payload__owner", "exact", `JSON_key("joined"."payload", $1) = $2`, nil, []any{"owner", "null"}},
		{"related__payload__owner", "isnull", `JSON_key("joined"."payload", $1) IS NULL`, true, []any{"owner"}},
		{"payload__owner", "iexact", `LOWER(TEXT_key("root"."payload", $1)) = LOWER($2)`, "bob", []any{"owner", "bob"}},
		{"payload__0", "exact", `JSON_index("root"."payload", $1) = $2`, 1, []any{int32(0), "1"}},
		{"payload__-1", "exact", `JSON_index("root"."payload", $1) = $2`, 3, []any{int32(-1), "3"}},
		{"payload__number", "gte", `JSON_key("root"."payload", $1) >= $2`, 9, []any{"number", "9"}},
	} {
		t.Run(test.field+"/"+test.lookup, func(t *testing.T) {
			compiler := Compiler{Dialect: alternateJSONPathDialect{}, Schema: schema, Alias: "root", Joins: map[string]db.Join{"related": {Alias: "joined", Schema: schema}}}
			statement, err := compiler.Predicate(db.Predicate{Field: test.field, Lookup: test.lookup, Value: test.value})
			if err != nil || statement != test.sql || !reflect.DeepEqual(compiler.Args, test.args) {
				t.Fatal(statement, compiler.Args, err)
			}
		})
	}
	key := `not__syntax') --`
	expression := db.Expression{Kind: "json_path", Args: []db.Expression{{Kind: "field", Name: "payload"}}, Value: []string{key}}
	compiler := Compiler{Dialect: alternateJSONPathDialect{}, Schema: schema}
	statement, err := compiler.Predicate(db.Predicate{Expression: &expression, Value: true})
	if err != nil || strings.Contains(statement, key) || !reflect.DeepEqual(compiler.Args, []any{key, "true"}) {
		t.Fatal("literal key was parsed or interpolated", statement, compiler.Args, err)
	}
	for _, field := range []string{"unknown__payload__key", "name__key", "payload__2147483648", "payload__bad\x00key", "payload__" + strings.Repeat("k__", 65)} {
		compiler := Compiler{Dialect: alternateJSONPathDialect{}, Schema: schema}
		if _, err := compiler.Predicate(db.Predicate{Field: field, Value: 1}); err == nil || len(compiler.Args) != 0 {
			t.Fatal("invalid/unresolved path accepted or bound", err, compiler.Args)
		}
	}
	compiler = Compiler{Dialect: alternateJSONDialect{}, Schema: schema}
	if _, err := compiler.Predicate(db.Predicate{Field: "payload__owner", Value: "Bob"}); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 0 {
		t.Fatal("missing path dialect did not fail closed", err)
	}
	schema.Fields[1].Codec = unusedJSONPathCodec{}
	compiler = Compiler{Dialect: alternateJSONPathDialect{}, Schema: schema}
	if _, err := compiler.Predicate(db.Predicate{Field: "payload__owner", Value: "Bob"}); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 0 {
		t.Fatal("custom codec was silently treated as standard JSON", err)
	}
	if _, err := OutputField(schema, "payload__owner"); !db.IsCode(err, db.UnsupportedFeature) {
		t.Fatal("custom codec JSON projection was silently retyped", err)
	}
}
