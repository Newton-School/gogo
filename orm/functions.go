package orm

import (
	"errors"
	"fmt"

	"github.com/Newton-School/gogo/orm/dialects"
)

var ErrUnsupportedFunction = errors.New("unsupported database function")

// CollateExpression renders expression COLLATE collation.
type CollateExpression struct {
	Expression Expression
	Collation  string
}

func Collate(expression Expression, collation string) CollateExpression {
	return CollateExpression{Expression: expression, Collation: collation}
}

func (e CollateExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	fragment, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{SQL: fragment.SQL + " COLLATE " + ctx.Dialect.QuoteIdent(e.Collation), Args: fragment.Args}, nil
}

func Greatest(args ...Expression) FunctionExpression { return Func("GREATEST", args...) }
func Least(args ...Expression) FunctionExpression    { return Func("LEAST", args...) }
func NullIf(a, b Expression) FunctionExpression      { return Func("NULLIF", a, b) }
func Now() FunctionExpression                        { return Func("NOW") }
func Lower(expression Expression) FunctionExpression { return Func("LOWER", expression) }
func Upper(expression Expression) FunctionExpression { return Func("UPPER", expression) }
func Length(expression Expression) FunctionExpression {
	return Func("LENGTH", expression)
}
func Substr(expression, start, length Expression) FunctionExpression {
	return Func("SUBSTR", expression, start, length)
}
func Replace(expression, old, replacement Expression) FunctionExpression {
	return Func("REPLACE", expression, old, replacement)
}
func Concat(args ...Expression) FunctionExpression { return Func("CONCAT", args...) }
func MD5(expression Expression) FunctionExpression { return Func("MD5", expression) }
func Round(expression Expression, precision ...Expression) FunctionExpression {
	args := append([]Expression{expression}, precision...)
	return Func("ROUND", args...)
}
func Ceil(expression Expression) FunctionExpression  { return Func("CEIL", expression) }
func Floor(expression Expression) FunctionExpression { return Func("FLOOR", expression) }
func Abs(expression Expression) FunctionExpression   { return Func("ABS", expression) }
func Mod(left, right Expression) FunctionExpression  { return Func("MOD", left, right) }
func Power(left, right Expression) FunctionExpression {
	return Func("POWER", left, right)
}
func Random() FunctionExpression { return Func("RANDOM") }

// ExtractExpression renders backend date extraction.
type ExtractExpression struct {
	Part       string
	Expression Expression
}

func Extract(part string, expression Expression) ExtractExpression {
	return ExtractExpression{Part: part, Expression: expression}
}

func (e ExtractExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	fragment, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	sql, err := ctx.Dialect.DateExtract(e.Part, fragment.SQL)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{SQL: sql, Args: fragment.Args}, nil
}

// TruncExpression renders date/time truncation.
type TruncExpression struct {
	Part       string
	Expression Expression
}

func Trunc(part string, expression Expression) TruncExpression {
	return TruncExpression{Part: part, Expression: expression}
}

func (e TruncExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	fragment, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	switch ctx.Dialect.Name() {
	case "postgres":
		args := append([]any{e.Part}, fragment.Args...)
		return SQLFragment{SQL: "DATE_TRUNC(" + ctx.placeholder(0) + ", " + fragment.SQL + ")", Args: args}, nil
	default:
		return SQLFragment{}, fmt.Errorf("%w: trunc is not supported by %s", ErrUnsupportedFunction, ctx.Dialect.Name())
	}
}

// HashExpression renders SHA-family hash functions where supported.
type HashExpression struct {
	Algorithm  string
	Expression Expression
}

func SHA256(expression Expression) HashExpression {
	return HashExpression{Algorithm: "sha256", Expression: expression}
}

func SHA512(expression Expression) HashExpression {
	return HashExpression{Algorithm: "sha512", Expression: expression}
}

func (e HashExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if ctx.Dialect.Name() != "postgres" {
		return SQLFragment{}, fmt.Errorf("%w: %s hash requires PostgreSQL pgcrypto", ErrUnsupportedFunction, e.Algorithm)
	}
	fragment, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	algorithmPlaceholder := ctx.Dialect.Placeholder(ctx.Start + len(fragment.Args))
	encodingPlaceholder := ctx.Dialect.Placeholder(ctx.Start + len(fragment.Args) + 1)
	args := append([]any(nil), fragment.Args...)
	args = append(args, e.Algorithm, "hex")
	return SQLFragment{SQL: "ENCODE(DIGEST(" + fragment.SQL + ", " + algorithmPlaceholder + "), " + encodingPlaceholder + ")", Args: args}, nil
}

// JSONExpression renders JSON object and array builders.
type JSONExpression struct {
	Object bool
	Items  []any
}

func JSONObject(items ...any) JSONExpression {
	return JSONExpression{Object: true, Items: append([]any(nil), items...)}
}

func JSONArray(items ...Expression) JSONExpression {
	values := make([]any, len(items))
	for i, item := range items {
		values[i] = item
	}
	return JSONExpression{Items: values}
}

func (e JSONExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	name := jsonFunctionName(ctx.Dialect.Name(), e.Object)
	if e.Object && len(e.Items)%2 != 0 {
		return SQLFragment{}, fmt.Errorf("%w: JSONObject requires key/value pairs", ErrInvalidExpression)
	}
	parts := make([]string, 0, len(e.Items))
	args := make([]any, 0)
	for i, item := range e.Items {
		var expression Expression
		if e.Object && i%2 == 0 {
			key, ok := item.(string)
			if !ok {
				return SQLFragment{}, fmt.Errorf("%w: JSONObject keys must be strings", ErrInvalidExpression)
			}
			expression = Value(key)
		} else {
			var ok bool
			expression, ok = item.(Expression)
			if !ok {
				return SQLFragment{}, fmt.Errorf("%w: JSON values must be expressions", ErrInvalidExpression)
			}
		}
		sql, nextArgs, err := compileChild(ctx, expression, args)
		if err != nil {
			return SQLFragment{}, err
		}
		parts = append(parts, sql)
		args = nextArgs
	}
	return SQLFragment{SQL: name + "(" + joinSQL(parts, ", ") + ")", Args: args}, nil
}

// AnnotateExpression compiles an expression and stores it as query annotation metadata.
func (q Query) AnnotateExpression(alias string, expression Expression, dialect dialects.Dialect) (Query, error) {
	fragment, err := CompileExpression(dialect, expression, 1)
	if err != nil {
		return Query{}, err
	}
	return q.Annotate(alias, ExpressionRef{SQL: fragment.SQL, Args: fragment.Args}), nil
}

func jsonFunctionName(dialectName string, object bool) string {
	switch dialectName {
	case "postgres":
		if object {
			return "jsonb_build_object"
		}
		return "jsonb_build_array"
	default:
		if object {
			return "json_object"
		}
		return "json_array"
	}
}
