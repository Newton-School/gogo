package orm

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/Newton-School/gogo/orm/dialects"
)

var ErrInvalidLookup = errors.New("invalid lookup")

// SQLFragment stores parameterized SQL and its bound args.
type SQLFragment struct {
	SQL  string
	Args []any
}

// JSONPathValue stores a JSON path lookup value.
type JSONPathValue struct {
	Path  []string
	Value any
}

// LookupContext is passed to lookup renderers.
type LookupContext struct {
	Dialect dialects.Dialect
	Column  string
	Lookup  Lookup
	Value   any
	Start   int
}

// Placeholder returns the placeholder at a zero-based offset from Start.
func (c LookupContext) Placeholder(offset int) string {
	start := c.Start
	if start <= 0 {
		start = 1
	}
	return c.Dialect.Placeholder(start + offset)
}

// QuotedColumn returns the dialect-quoted column identifier.
func (c LookupContext) QuotedColumn() string {
	return c.Dialect.QuoteIdent(c.Column)
}

// LookupRenderer compiles one lookup into SQL.
type LookupRenderer func(LookupContext) (SQLFragment, error)

// LookupRegistry stores built-in and custom lookup renderers.
type LookupRegistry struct {
	renderers map[Lookup]LookupRenderer
}

// NewLookupRegistry creates a registry with all built-in lookups.
func NewLookupRegistry() *LookupRegistry {
	registry := &LookupRegistry{renderers: make(map[Lookup]LookupRenderer)}
	registry.mustRegisterBuiltins()
	return registry
}

// Register adds or replaces a lookup renderer.
func (r *LookupRegistry) Register(lookup Lookup, renderer LookupRenderer) error {
	if lookup == "" || renderer == nil {
		return fmt.Errorf("%w: lookup name and renderer are required", ErrInvalidLookup)
	}
	r.renderers[lookup] = renderer
	return nil
}

// Compile compiles a lookup using the registered renderer.
func (r *LookupRegistry) Compile(ctx LookupContext) (SQLFragment, error) {
	if ctx.Dialect == nil {
		return SQLFragment{}, fmt.Errorf("%w: dialect is required", ErrInvalidLookup)
	}
	renderer, ok := r.renderers[ctx.Lookup]
	if !ok {
		return SQLFragment{}, fmt.Errorf("%w: unsupported lookup %q", ErrInvalidLookup, ctx.Lookup)
	}
	return renderer(ctx)
}

func (r *LookupRegistry) mustRegisterBuiltins() {
	builtins := map[Lookup]LookupRenderer{
		LookupExact:     comparison("="),
		LookupIExact:    lowerComparison("="),
		LookupContains:  patternLookup("LIKE", containsPattern, false),
		LookupIContains: patternLookup("LIKE", containsPattern, true),
		LookupIn:        inLookup,
		LookupGT:        comparison(">"),
		LookupGTE:       comparison(">="),
		LookupLT:        comparison("<"),
		LookupLTE:       comparison("<="),
		LookupRange:     rangeLookup,
		LookupStarts:    patternLookup("LIKE", startsPattern, false),
		LookupIStarts:   patternLookup("LIKE", startsPattern, true),
		LookupEnds:      patternLookup("LIKE", endsPattern, false),
		LookupIEnds:     patternLookup("LIKE", endsPattern, true),
		LookupDate:      dateLookup,
		LookupYear:      extractedLookup("year"),
		LookupMonth:     extractedLookup("month"),
		LookupDay:       extractedLookup("day"),
		LookupWeek:      extractedLookup("week"),
		LookupWeekDay:   extractedLookup("weekday"),
		LookupQuarter:   extractedLookup("quarter"),
		LookupTime:      timeLookup,
		LookupHour:      extractedLookup("hour"),
		LookupMinute:    extractedLookup("minute"),
		LookupSecond:    extractedLookup("second"),
		LookupIsNull:    isNullLookup,
		LookupRegex:     regexLookup(false),
		LookupIRegex:    regexLookup(true),
		LookupJSONPath:  jsonPathLookup,
	}
	for lookup, renderer := range builtins {
		r.renderers[lookup] = renderer
	}
}

func comparison(operator string) LookupRenderer {
	return func(ctx LookupContext) (SQLFragment, error) {
		return SQLFragment{
			SQL:  ctx.QuotedColumn() + " " + operator + " " + ctx.Placeholder(0),
			Args: []any{ctx.Value},
		}, nil
	}
}

func lowerComparison(operator string) LookupRenderer {
	return func(ctx LookupContext) (SQLFragment, error) {
		placeholder := ctx.Placeholder(0)
		return SQLFragment{
			SQL:  "LOWER(" + ctx.QuotedColumn() + ") " + operator + " LOWER(" + placeholder + ")",
			Args: []any{ctx.Value},
		}, nil
	}
}

func patternLookup(operator string, pattern func(any) any, insensitive bool) LookupRenderer {
	return func(ctx LookupContext) (SQLFragment, error) {
		column := ctx.QuotedColumn()
		placeholder := ctx.Placeholder(0)
		if insensitive {
			column = "LOWER(" + column + ")"
			placeholder = "LOWER(" + placeholder + ")"
		}
		return SQLFragment{
			SQL:  column + " " + operator + " " + placeholder,
			Args: []any{pattern(ctx.Value)},
		}, nil
	}
}

func containsPattern(value any) any {
	return "%" + fmt.Sprint(value) + "%"
}

func startsPattern(value any) any {
	return fmt.Sprint(value) + "%"
}

func endsPattern(value any) any {
	return "%" + fmt.Sprint(value)
}

func inLookup(ctx LookupContext) (SQLFragment, error) {
	values, err := listValues(ctx.Value)
	if err != nil {
		return SQLFragment{}, err
	}
	placeholders := make([]string, len(values))
	for i := range values {
		placeholders[i] = ctx.Placeholder(i)
	}
	return SQLFragment{
		SQL:  ctx.QuotedColumn() + " IN (" + joinSQL(placeholders, ", ") + ")",
		Args: values,
	}, nil
}

func rangeLookup(ctx LookupContext) (SQLFragment, error) {
	values, err := listValues(ctx.Value)
	if err != nil {
		return SQLFragment{}, err
	}
	if len(values) != 2 {
		return SQLFragment{}, fmt.Errorf("%w: range lookup requires exactly two values", ErrInvalidLookup)
	}
	return SQLFragment{
		SQL:  ctx.QuotedColumn() + " BETWEEN " + ctx.Placeholder(0) + " AND " + ctx.Placeholder(1),
		Args: values,
	}, nil
}

func dateLookup(ctx LookupContext) (SQLFragment, error) {
	return SQLFragment{
		SQL:  "DATE(" + ctx.QuotedColumn() + ") = " + ctx.Placeholder(0),
		Args: []any{ctx.Value},
	}, nil
}

func timeLookup(ctx LookupContext) (SQLFragment, error) {
	return SQLFragment{
		SQL:  "CAST(" + ctx.QuotedColumn() + " AS time) = " + ctx.Placeholder(0),
		Args: []any{ctx.Value},
	}, nil
}

func extractedLookup(part string) LookupRenderer {
	return func(ctx LookupContext) (SQLFragment, error) {
		sql, err := ctx.Dialect.DateExtract(part, ctx.QuotedColumn())
		if err != nil {
			return SQLFragment{}, err
		}
		return SQLFragment{
			SQL:  sql + " = " + ctx.Placeholder(0),
			Args: []any{ctx.Value},
		}, nil
	}
}

func isNullLookup(ctx LookupContext) (SQLFragment, error) {
	isNull, ok := ctx.Value.(bool)
	if !ok {
		return SQLFragment{}, fmt.Errorf("%w: isnull lookup requires a bool", ErrInvalidLookup)
	}
	operator := "IS NOT NULL"
	if isNull {
		operator = "IS NULL"
	}
	return SQLFragment{SQL: ctx.QuotedColumn() + " " + operator}, nil
}

func regexLookup(insensitive bool) LookupRenderer {
	return func(ctx LookupContext) (SQLFragment, error) {
		operator := "~"
		if insensitive {
			operator = "~*"
		}
		if ctx.Dialect.Name() == "sqlite" {
			column := ctx.QuotedColumn()
			placeholder := ctx.Placeholder(0)
			if insensitive {
				column = "LOWER(" + column + ")"
				placeholder = "LOWER(" + placeholder + ")"
			}
			return SQLFragment{SQL: column + " REGEXP " + placeholder, Args: []any{ctx.Value}}, nil
		}
		return SQLFragment{SQL: ctx.QuotedColumn() + " " + operator + " " + ctx.Placeholder(0), Args: []any{ctx.Value}}, nil
	}
}

func jsonPathLookup(ctx LookupContext) (SQLFragment, error) {
	value, ok := ctx.Value.(JSONPathValue)
	if !ok {
		return SQLFragment{}, fmt.Errorf("%w: json path lookup requires JSONPathValue", ErrInvalidLookup)
	}
	sql, err := ctx.Dialect.JSONLookup(ctx.QuotedColumn(), value.Path)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{SQL: sql + " = " + ctx.Placeholder(0), Args: []any{value.Value}}, nil
}

func listValues(value any) ([]any, error) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, fmt.Errorf("%w: lookup value must be a slice or array", ErrInvalidLookup)
	}
	values := make([]any, reflected.Len())
	for i := 0; i < reflected.Len(); i++ {
		values[i] = reflected.Index(i).Interface()
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: lookup value cannot be empty", ErrInvalidLookup)
	}
	return values, nil
}

func joinSQL(values []string, separator string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += separator
		}
		result += value
	}
	return result
}
