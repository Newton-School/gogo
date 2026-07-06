package orm

import (
	"fmt"

	"github.com/Newton-School/gogo/orm/dialects"
)

// AggregateExpression renders SQL aggregate functions.
type AggregateExpression struct {
	Function         string
	Expression       Expression
	All              bool
	DistinctFlag     bool
	FilterExpression Expression
	Alias            string
	WindowCompatible bool
}

func Count(expression Expression) AggregateExpression {
	return aggregate("COUNT", expression)
}

func CountAll() AggregateExpression {
	aggregate := aggregate("COUNT", nil)
	aggregate.All = true
	return aggregate
}

func Sum(expression Expression) AggregateExpression      { return aggregate("SUM", expression) }
func Avg(expression Expression) AggregateExpression      { return aggregate("AVG", expression) }
func Min(expression Expression) AggregateExpression      { return aggregate("MIN", expression) }
func Max(expression Expression) AggregateExpression      { return aggregate("MAX", expression) }
func StdDev(expression Expression) AggregateExpression   { return aggregate("STDDEV", expression) }
func Variance(expression Expression) AggregateExpression { return aggregate("VARIANCE", expression) }

func aggregate(function string, expression Expression) AggregateExpression {
	return AggregateExpression{Function: function, Expression: expression, WindowCompatible: true}
}

// Distinct marks the aggregate as DISTINCT.
func (a AggregateExpression) Distinct() AggregateExpression {
	a.DistinctFlag = true
	return a
}

// Filter adds an aggregate FILTER clause.
func (a AggregateExpression) Filter(expression Expression) AggregateExpression {
	a.FilterExpression = expression
	return a
}

// As sets the aggregate alias.
func (a AggregateExpression) As(alias string) AggregateExpression {
	a.Alias = alias
	return a
}

func (a AggregateExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if !safeSQLNamePattern.MatchString(a.Function) {
		return SQLFragment{}, fmt.Errorf("%w: unsafe aggregate function %q", ErrInvalidExpression, a.Function)
	}

	args := make([]any, 0)
	inner := "*"
	if !a.All {
		if a.Expression == nil {
			return SQLFragment{}, fmt.Errorf("%w: aggregate expression is required", ErrInvalidExpression)
		}
		fragment, err := a.Expression.Compile(ctx)
		if err != nil {
			return SQLFragment{}, err
		}
		inner = fragment.SQL
		args = append(args, fragment.Args...)
		if a.DistinctFlag {
			inner = "DISTINCT " + inner
		}
	}

	sql := a.Function + "(" + inner + ")"
	if a.FilterExpression != nil {
		filter, err := a.FilterExpression.Compile(ExpressionContext{Dialect: ctx.Dialect, Start: ctx.Start + len(args)})
		if err != nil {
			return SQLFragment{}, err
		}
		args = append(args, filter.Args...)
		if ctx.Dialect.Name() == "postgres" {
			sql += " FILTER (WHERE " + filter.SQL + ")"
		} else {
			return SQLFragment{}, fmt.Errorf("%w: aggregate filters are not supported by %s", ErrUnsupportedFunction, ctx.Dialect.Name())
		}
	}
	return SQLFragment{SQL: sql, Args: args}, nil
}

// SelectionSQL renders an aggregate with its alias for SELECT lists.
func (a AggregateExpression) SelectionSQL(dialect dialects.Dialect, start int) (SQLFragment, error) {
	fragment, err := CompileExpression(dialect, a, start)
	if err != nil {
		return SQLFragment{}, err
	}
	if a.Alias != "" {
		fragment.SQL += " AS " + dialect.QuoteIdent(a.Alias)
	}
	return fragment, nil
}

// AnnotateAggregate compiles an aggregate and stores it as query annotation metadata.
func (q Query) AnnotateAggregate(aggregate AggregateExpression, dialect dialects.Dialect) (Query, error) {
	if aggregate.Alias == "" {
		return Query{}, fmt.Errorf("%w: aggregate alias is required", ErrInvalidExpression)
	}
	fragment, err := CompileExpression(dialect, aggregate, 1)
	if err != nil {
		return Query{}, err
	}
	return q.Annotate(aggregate.Alias, ExpressionRef{SQL: fragment.SQL, Args: fragment.Args}), nil
}

// AggregateResult stores scanned aggregate output.
type AggregateResult struct {
	Values map[string]any
}

// Get returns one aggregate value.
func (r AggregateResult) Get(alias string) (any, bool) {
	value, ok := r.Values[alias]
	return value, ok
}

// Int64 returns one aggregate value as int64 when possible.
func (r AggregateResult) Int64(alias string) (int64, bool) {
	value, ok := r.Get(alias)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// Float64 returns one aggregate value as float64 when possible.
func (r AggregateResult) Float64(alias string) (float64, bool) {
	value, ok := r.Get(alias)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
