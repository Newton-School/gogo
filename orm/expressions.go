package orm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Newton-School/gogo/orm/dialects"
)

var (
	ErrInvalidExpression = errors.New("invalid expression")
	ErrUnsafeRawSQL      = errors.New("unsafe raw sql")
)

var safeSQLNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var safeCastTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ ]*$`)

// Expression compiles into parameterized SQL.
type Expression interface {
	Compile(ExpressionContext) (SQLFragment, error)
}

// ExpressionContext carries dialect and placeholder state.
type ExpressionContext struct {
	Dialect dialects.Dialect
	Start   int
}

func (c ExpressionContext) placeholder(offset int) string {
	start := c.Start
	if start <= 0 {
		start = 1
	}
	return c.Dialect.Placeholder(start + offset)
}

// CompileExpression compiles one expression using a dialect and placeholder offset.
func CompileExpression(dialect dialects.Dialect, expression Expression, start int) (SQLFragment, error) {
	if dialect == nil {
		return SQLFragment{}, fmt.Errorf("%w: dialect is required", ErrInvalidExpression)
	}
	if expression == nil {
		return SQLFragment{}, fmt.Errorf("%w: expression is required", ErrInvalidExpression)
	}
	return expression.Compile(ExpressionContext{Dialect: dialect, Start: start})
}

// FieldExpression references a model field.
type FieldExpression struct {
	Name string
}

// F creates a field reference expression.
func F(name string) FieldExpression {
	return FieldExpression{Name: name}
}

func (e FieldExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if e.Name == "" {
		return SQLFragment{}, fmt.Errorf("%w: field name is required", ErrInvalidExpression)
	}
	return SQLFragment{SQL: ctx.Dialect.QuoteIdent(e.Name)}, nil
}

// ValueExpression represents one bound value.
type ValueExpression struct {
	V any
}

// Value creates a bound value expression.
func Value(value any) ValueExpression {
	return ValueExpression{V: value}
}

func (e ValueExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	return SQLFragment{SQL: ctx.placeholder(0), Args: []any{e.V}}, nil
}

// FilterExpression compiles a field lookup predicate.
type FilterExpression struct {
	Field  string
	Lookup Lookup
	Value  any
}

// Filter creates a lookup predicate expression.
func Filter(field string, lookup Lookup, value any) FilterExpression {
	return FilterExpression{Field: field, Lookup: lookup, Value: value}
}

func (e FilterExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	return NewLookupRegistry().Compile(LookupContext{
		Dialect: ctx.Dialect,
		Column:  e.Field,
		Lookup:  e.Lookup,
		Value:   e.Value,
		Start:   ctx.Start,
	})
}

// QExpression combines predicates with AND/OR and NOT.
type QExpression struct {
	Connector string
	Children  []Expression
	Negated   bool
}

// Q creates an AND-connected predicate group.
func Q(children ...Expression) QExpression {
	return QExpression{Connector: "AND", Children: append([]Expression(nil), children...)}
}

// And combines expressions with AND.
func (q QExpression) And(expression Expression) QExpression {
	if q.Connector == "AND" && !q.Negated {
		q.Children = append(q.Children, expression)
		return q
	}
	return QExpression{Connector: "AND", Children: []Expression{q, expression}}
}

// Or combines expressions with OR.
func (q QExpression) Or(expression Expression) QExpression {
	if q.Connector == "OR" && !q.Negated {
		q.Children = append(q.Children, expression)
		return q
	}
	return QExpression{Connector: "OR", Children: []Expression{q, expression}}
}

// Not negates an expression.
func Not(expression Expression) QExpression {
	return QExpression{Connector: "AND", Children: []Expression{expression}, Negated: true}
}

func (q QExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if len(q.Children) == 0 {
		return SQLFragment{}, fmt.Errorf("%w: Q requires children", ErrInvalidExpression)
	}
	parts := make([]string, 0, len(q.Children))
	args := make([]any, 0)
	for _, child := range q.Children {
		sql, nextArgs, err := compileChild(ctx, child, args)
		if err != nil {
			return SQLFragment{}, err
		}
		parts = append(parts, sql)
		args = nextArgs
	}
	sql := strings.Join(parts, " "+q.Connector+" ")
	if len(parts) > 1 {
		sql = "(" + sql + ")"
	}
	if q.Negated {
		sql = "NOT (" + sql + ")"
	}
	return SQLFragment{SQL: sql, Args: args}, nil
}

// FunctionExpression renders SQL function calls.
type FunctionExpression struct {
	Name string
	Args []Expression
}

// Func creates a SQL function expression.
func Func(name string, args ...Expression) FunctionExpression {
	return FunctionExpression{Name: name, Args: append([]Expression(nil), args...)}
}

func (e FunctionExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if !safeSQLNamePattern.MatchString(e.Name) {
		return SQLFragment{}, fmt.Errorf("%w: unsafe function name %q", ErrInvalidExpression, e.Name)
	}
	parts := make([]string, 0, len(e.Args))
	args := make([]any, 0)
	for _, expression := range e.Args {
		sql, nextArgs, err := compileChild(ctx, expression, args)
		if err != nil {
			return SQLFragment{}, err
		}
		parts = append(parts, sql)
		args = nextArgs
	}
	return SQLFragment{SQL: e.Name + "(" + strings.Join(parts, ", ") + ")", Args: args}, nil
}

// Coalesce creates a COALESCE expression.
func Coalesce(args ...Expression) FunctionExpression {
	return Func("COALESCE", args...)
}

// CastExpression renders CAST(expression AS type).
type CastExpression struct {
	Expression Expression
	DBType     string
}

// Cast creates a cast expression.
func Cast(expression Expression, dbType string) CastExpression {
	return CastExpression{Expression: expression, DBType: dbType}
}

func (e CastExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if !safeCastTypePattern.MatchString(e.DBType) {
		return SQLFragment{}, fmt.Errorf("%w: unsafe cast type %q", ErrInvalidExpression, e.DBType)
	}
	fragment, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{SQL: "CAST(" + fragment.SQL + " AS " + e.DBType + ")", Args: fragment.Args}, nil
}

// WhenExpression stores one CASE WHEN branch.
type WhenExpression struct {
	Condition Expression
	Result    Expression
}

// When creates a CASE branch.
func When(condition, result Expression) WhenExpression {
	return WhenExpression{Condition: condition, Result: result}
}

// CaseExpression renders CASE WHEN ... THEN ... ELSE ... END.
type CaseExpression struct {
	Whens       []WhenExpression
	DefaultExpr Expression
}

// Case creates a CASE expression.
func Case(whens ...WhenExpression) CaseExpression {
	return CaseExpression{Whens: append([]WhenExpression(nil), whens...)}
}

// Default sets the default CASE expression.
func (e CaseExpression) Default(expression Expression) CaseExpression {
	e.DefaultExpr = expression
	return e
}

func (e CaseExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if len(e.Whens) == 0 {
		return SQLFragment{}, fmt.Errorf("%w: CASE requires WHEN branches", ErrInvalidExpression)
	}
	parts := []string{"CASE"}
	args := make([]any, 0)
	for _, when := range e.Whens {
		conditionSQL, nextArgs, err := compileChild(ctx, when.Condition, args)
		if err != nil {
			return SQLFragment{}, err
		}
		args = nextArgs
		resultSQL, nextArgs, err := compileChild(ctx, when.Result, args)
		if err != nil {
			return SQLFragment{}, err
		}
		args = nextArgs
		parts = append(parts, "WHEN "+conditionSQL+" THEN "+resultSQL)
	}
	if e.DefaultExpr != nil {
		defaultSQL, nextArgs, err := compileChild(ctx, e.DefaultExpr, args)
		if err != nil {
			return SQLFragment{}, err
		}
		args = nextArgs
		parts = append(parts, "ELSE "+defaultSQL)
	}
	parts = append(parts, "END")
	return SQLFragment{SQL: strings.Join(parts, " "), Args: args}, nil
}

// RawExpression stores raw SQL fragments.
type RawExpression struct {
	SQLText string
	Args    []any
	Unsafe  bool
}

// RawSQL creates raw SQL that must be explicitly marked unsafe before compilation.
func RawSQL(sql string, args ...any) RawExpression {
	return RawExpression{SQLText: sql, Args: append([]any(nil), args...)}
}

// UnsafeRawSQL creates an explicitly unsafe raw SQL expression.
func UnsafeRawSQL(sql string, args ...any) RawExpression {
	return RawExpression{SQLText: sql, Args: append([]any(nil), args...), Unsafe: true}
}

func (e RawExpression) Compile(ExpressionContext) (SQLFragment, error) {
	if !e.Unsafe {
		return SQLFragment{}, ErrUnsafeRawSQL
	}
	return SQLFragment{SQL: e.SQLText, Args: append([]any(nil), e.Args...)}, nil
}

// SubqueryExpression stores a raw subquery fragment for later compiler integration.
type SubqueryExpression struct {
	SQLText string
	Args    []any
}

// Subquery creates a subquery expression.
func Subquery(sql string, args ...any) SubqueryExpression {
	return SubqueryExpression{SQLText: sql, Args: append([]any(nil), args...)}
}

func (e SubqueryExpression) Compile(ExpressionContext) (SQLFragment, error) {
	return SQLFragment{SQL: "(" + e.SQLText + ")", Args: append([]any(nil), e.Args...)}, nil
}

// OuterRefExpression references an outer query field.
type OuterRefExpression struct {
	Name string
}

// OuterRef creates an outer query reference.
func OuterRef(name string) OuterRefExpression {
	return OuterRefExpression{Name: name}
}

// SQL renders the outer reference for use in subquery strings.
func (e OuterRefExpression) SQL() string {
	return "OUTER." + dialects.QuoteIdent(e.Name)
}

func (e OuterRefExpression) Compile(ExpressionContext) (SQLFragment, error) {
	return SQLFragment{SQL: e.SQL()}, nil
}

// ExistsExpression renders EXISTS(subquery).
type ExistsExpression struct {
	Subquery Expression
}

// Exists creates an EXISTS expression.
func Exists(subquery Expression) ExistsExpression {
	return ExistsExpression{Subquery: subquery}
}

func (e ExistsExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	fragment, err := e.Subquery.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{SQL: "EXISTS " + fragment.SQL, Args: fragment.Args}, nil
}

// BinaryExpression renders arithmetic and comparison expressions.
type BinaryExpression struct {
	Left     Expression
	Operator string
	Right    Expression
}

// Compare creates a comparison expression.
func Compare(left Expression, operator string, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: operator, Right: right}
}

// Add creates an addition/concatenation expression.
func Add(left, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: "+", Right: right}
}

func Subtract(left, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: "-", Right: right}
}

func Multiply(left, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: "*", Right: right}
}

func Divide(left, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: "/", Right: right}
}

func Modulo(left, right Expression) BinaryExpression {
	return BinaryExpression{Left: left, Operator: "%", Right: right}
}

func (e BinaryExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	if !validOperator(e.Operator) {
		return SQLFragment{}, fmt.Errorf("%w: unsupported operator %q", ErrInvalidExpression, e.Operator)
	}
	left, err := e.Left.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	right, err := e.Right.Compile(ExpressionContext{Dialect: ctx.Dialect, Start: ctx.Start + len(left.Args)})
	if err != nil {
		return SQLFragment{}, err
	}
	args := append([]any(nil), left.Args...)
	args = append(args, right.Args...)
	return SQLFragment{SQL: left.SQL + " " + e.Operator + " " + right.SQL, Args: args}, nil
}

// Window stores OVER clause metadata.
type Window struct {
	PartitionBy []Expression
	OrderBy     []string
	Frame       Frame
}

// WindowExpression renders expression OVER (...).
type WindowExpression struct {
	Expression Expression
	Window     Window
}

// Over creates a window expression.
func Over(expression Expression, window Window) WindowExpression {
	return WindowExpression{Expression: expression, Window: window}
}

func (e WindowExpression) Compile(ctx ExpressionContext) (SQLFragment, error) {
	base, err := e.Expression.Compile(ctx)
	if err != nil {
		return SQLFragment{}, err
	}
	args := append([]any(nil), base.Args...)
	clauses := make([]string, 0, 3)
	if len(e.Window.PartitionBy) > 0 {
		parts := make([]string, 0, len(e.Window.PartitionBy))
		for _, expression := range e.Window.PartitionBy {
			sql, nextArgs, err := compileChild(ctx, expression, args)
			if err != nil {
				return SQLFragment{}, err
			}
			parts = append(parts, sql)
			args = nextArgs
		}
		clauses = append(clauses, "PARTITION BY "+strings.Join(parts, ", "))
	}
	if len(e.Window.OrderBy) > 0 {
		clauses = append(clauses, "ORDER BY "+compileOrdering(ctx.Dialect, e.Window.OrderBy))
	}
	if e.Window.Frame.Mode != "" {
		clauses = append(clauses, e.Window.Frame.SQL())
	}
	return SQLFragment{SQL: base.SQL + " OVER (" + strings.Join(clauses, " ") + ")", Args: args}, nil
}

// Frame stores SQL window frame metadata.
type Frame struct {
	Mode  string
	Start FrameBound
	End   FrameBound
}

// SQL renders the frame clause.
func (f Frame) SQL() string {
	if f.Mode == "" {
		return ""
	}
	return f.Mode + " BETWEEN " + string(f.Start) + " AND " + string(f.End)
}

// FrameBound stores a SQL frame bound.
type FrameBound string

const (
	FrameUnboundedPreceding FrameBound = "UNBOUNDED PRECEDING"
	FrameCurrentRow         FrameBound = "CURRENT ROW"
	FrameUnboundedFollowing FrameBound = "UNBOUNDED FOLLOWING"
)

// FramePreceding creates an N PRECEDING bound.
func FramePreceding(value int) FrameBound {
	return FrameBound(fmt.Sprintf("%d PRECEDING", value))
}

// FrameFollowing creates an N FOLLOWING bound.
func FrameFollowing(value int) FrameBound {
	return FrameBound(fmt.Sprintf("%d FOLLOWING", value))
}

// RowsBetween creates a ROWS frame.
func RowsBetween(start, end FrameBound) Frame {
	return Frame{Mode: "ROWS", Start: start, End: end}
}

// RangeBetween creates a RANGE frame.
func RangeBetween(start, end FrameBound) Frame {
	return Frame{Mode: "RANGE", Start: start, End: end}
}

func compileChild(ctx ExpressionContext, expression Expression, existingArgs []any) (string, []any, error) {
	fragment, err := expression.Compile(ExpressionContext{Dialect: ctx.Dialect, Start: ctx.Start + len(existingArgs)})
	if err != nil {
		return "", nil, err
	}
	args := append(existingArgs, fragment.Args...)
	return fragment.SQL, args, nil
}

func compileOrdering(dialect dialects.Dialect, values []string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		direction := "ASC"
		field := value
		if strings.HasPrefix(value, "-") {
			direction = "DESC"
			field = strings.TrimPrefix(value, "-")
		}
		parts[i] = dialect.QuoteIdent(field) + " " + direction
	}
	return strings.Join(parts, ", ")
}

func validOperator(operator string) bool {
	switch operator {
	case "=", "!=", "<>", ">", ">=", "<", "<=", "+", "-", "*", "/", "%":
		return true
	default:
		return false
	}
}
