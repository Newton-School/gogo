package sqlcompiler

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type castDialect struct{ alternateJSONPathDialect }

func (d castDialect) CastExpression(operand string, output models.Field) (string, error) {
	typeName, err := d.FieldType(output)
	if err != nil {
		return "", err
	}
	return "CONVERT(" + operand + " AS " + typeName + ")", nil
}

func (castDialect) FieldType(field models.Field) (string, error) {
	switch field.Kind {
	case models.BigInteger:
		return "integer64", nil
	case models.Decimal:
		return fmt.Sprintf("decimal(%d,%d)", field.MaxDigits, field.DecimalPlaces), nil
	case models.JSON:
		return "json", nil
	case models.Text:
		return "text", nil
	}
	return "", &db.Error{Code: db.UnsupportedFeature, Message: "Unsupported output"}
}

func TestCastUsesDialectTypeAndDeclaredJSONMetadata(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Cast", Fields: []models.Field{models.JSONField("payload")}}
	for _, test := range []struct {
		output  models.Field
		value   any
		wantSQL string
		args    []any
	}{
		{models.BigIntegerField("ignored"), int64(9), `CONVERT(TEXT_key("payload", $1) AS integer64) = $2`, []any{"amount", int64(9)}},
		{models.JSONField("ignored"), "null", `CONVERT(TEXT_key("payload", $1) AS json) = $2`, []any{"amount", `"null"`}},
		{models.TextField("ignored"), nil, `CONVERT(TEXT_key("payload", $1) AS text) IS NULL`, []any{"amount"}},
	} {
		expression := db.Expression{Kind: "cast", Output: &test.output, Args: []db.Expression{{Kind: "json_text_path", Args: []db.Expression{{Kind: "field", Name: "payload"}}, Value: []string{"amount"}}}}
		compiler := Compiler{Dialect: castDialect{}, Schema: schema}
		statement, err := compiler.Predicate(db.Predicate{Expression: &expression, Value: test.value})
		if err != nil || statement != test.wantSQL || !reflect.DeepEqual(compiler.Args, test.args) {
			t.Fatal(statement, compiler.Args, err)
		}
	}
}

func TestCastRejectsDDLTypesAndCyclesBeforeBinding(t *testing.T) {
	cycle := models.Field{Kind: models.Array}
	cycle.Element = &cycle
	for _, output := range []models.Field{models.BigAutoField("id"), {Kind: models.Generated, GeneratedExpression: "ignored"}, {Kind: models.ManyToMany}, {Kind: models.ForeignKey}, {Kind: models.Text, Relation: &models.Relation{}}, {Kind: models.Array, Element: &models.Field{Kind: models.Auto}}, cycle} {
		compiler := Compiler{Dialect: castDialect{}}
		expression := db.Expression{Kind: "cast", Output: &output, Args: []db.Expression{{Kind: "value", Value: "not bound"}}}
		if _, err := compiler.Expression(expression); err == nil || len(compiler.Args) != 0 {
			t.Fatal("invalid cast bound a value", err)
		}
	}
	field := models.Field{Kind: models.Custom}
	compiler := Compiler{Dialect: castDialect{}}
	if _, err := compiler.Expression(db.Expression{Kind: "cast", Output: &field, Args: []db.Expression{{Kind: "value", Value: 1}}}); !db.IsCode(err, db.UnsupportedFeature) || len(compiler.Args) != 0 {
		t.Fatal("unsupported output reached SQL", err)
	}
	field = models.TextField("ignored")
	unsupported := Compiler{Dialect: numberedDialect{}, Args: []any{"prior"}}
	if _, err := unsupported.Expression(db.Expression{Kind: "cast", Output: &field, Args: []db.Expression{{Kind: "value", Value: "no fallback"}}}); !db.IsCode(err, db.UnsupportedFeature) || !reflect.DeepEqual(unsupported.Args, []any{"prior"}) {
		t.Fatal("storage type support was treated as cast support", err)
	}
	for _, expression := range []db.Expression{{Kind: "cast"}, {Kind: "cast", Output: &field}, {Kind: "cast", Output: &field, Args: []db.Expression{{Kind: "value"}, {Kind: "value"}}}, {Kind: "cast", Output: &field, Distinct: true, Args: []db.Expression{{Kind: "value"}}}} {
		if _, err := compiler.Expression(expression); err == nil {
			t.Fatal("invalid cast arity accepted")
		}
	}
}

func TestCastPreservesLiteralSourceTypeBeforeConversion(t *testing.T) {
	text := models.TextField("value")
	compiler := Compiler{Dialect: castDialect{}}
	statement, err := compiler.Expression(db.Expression{Kind: "cast", Output: &text, Args: []db.Expression{{Kind: "value", Value: 7}}})
	if err != nil || statement != `CONVERT(CONVERT($1 AS integer64) AS text)` || !reflect.DeepEqual(compiler.Args, []any{7}) {
		t.Fatal("integer literal was bound as text", statement, compiler.Args, err)
	}
}
