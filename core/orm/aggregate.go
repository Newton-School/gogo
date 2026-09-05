package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/internal/sqlcompiler"
)

// ResultExpression declares the result type, including nullability and decimal
// precision. It avoids inferring an application Go type from a driver's value.
type ResultExpression struct {
	Expression db.Expression
	Output     models.Field
}

func Typed(expression db.Expression, output models.Field) ResultExpression {
	return ResultExpression{Expression: expression, Output: output}
}
func Sum(expression db.Expression) db.Expression   { return Func("SUM", expression) }
func Avg(expression db.Expression) db.Expression   { return Func("AVG", expression) }
func Count(expression db.Expression) db.Expression { return Func("COUNT", expression) }
func Min(expression db.Expression) db.Expression   { return Func("MIN", expression) }
func Max(expression db.Expression) db.Expression   { return Func("MAX", expression) }
func StdDev(expression db.Expression, sample bool) db.Expression {
	if sample {
		return Func("STDDEV_SAMP", expression)
	}
	return Func("STDDEV_POP", expression)
}
func Variance(expression db.Expression, sample bool) db.Expression {
	if sample {
		return Func("VAR_SAMP", expression)
	}
	return Func("VAR_POP", expression)
}
func DistinctAggregate(expression db.Expression) db.Expression {
	expression.Distinct = true
	return expression
}
func FilteredAggregate(expression db.Expression, predicate db.Predicate) db.Expression {
	expression.Filter = &predicate
	return expression
}

// Aggregate executes one scoped aggregate statement and returns named typed
// values. Use Func("COALESCE", Sum(...), Value(...)) for an explicit empty-set
// default. A nullable Output returns nil when its aggregate has no value.
// Grouped, annotated, sliced, DISTINCT and locked source queries require a
// separate subquery execution path and are rejected, never silently rewritten.
func (q Query[T]) Aggregate(ctx context.Context, expressions map[string]ResultExpression) (map[string]any, error) {
	if q.err != nil {
		return nil, q.err
	}
	if len(expressions) == 0 || len(expressions) > 128 {
		return nil, errors.New("orm: aggregate requires between one and 128 named outputs")
	}
	if q.selectAST.Limit != nil || q.selectAST.Offset != nil || q.selectAST.Distinct || len(q.selectAST.DistinctOn) > 0 || q.selectAST.ForUpdate || len(q.selectAST.GroupBy) > 0 || len(q.selectAST.Projections) > 0 || len(q.selectAST.Aliases) > 0 {
		return nil, &db.Error{Code: db.UnsupportedFeature, Message: "Aggregate over sliced, distinct, grouped, annotated or locked queries requires an explicit subquery"}
	}
	aliases := make([]string, 0, len(expressions))
	for alias := range expressions {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	outputs := make([]models.Field, len(aliases))
	projections := make([]db.Projection, len(aliases))
	for i, alias := range aliases {
		if !models.ValidIdentifier(alias) {
			return nil, errors.New("orm: invalid aggregate alias")
		}
		spec := expressions[alias]
		found, err := validateAggregateExpression(spec.Expression, false, 0)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("orm: aggregate output must contain an aggregate")
		}
		output := (models.Schema{Fields: []models.Field{spec.Output}}).Clone().Fields[0]
		if output.Relation != nil || !output.IsStored() {
			return nil, errors.New("orm: scalar aggregate output field required")
		}
		if _, err := q.store.Backend.Dialect().FieldType(output); err != nil {
			return nil, err
		}
		outputs[i] = output
		projections[i] = db.Projection{Expression: cloneExpression(spec.Expression), Alias: alias}
	}
	q = q.clone()
	q.prefetches = nil
	q, err := q.prepareRelated(ctx)
	if err != nil {
		return nil, err
	}
	q.selectAST.Fields = nil
	q.selectAST.Projections = projections
	q.selectAST.Order = nil
	statement, args, err := sqlcompiler.Select(q.store.Backend.Dialect(), q.schema, q.selectAST)
	if err != nil {
		return nil, err
	}
	values := make([]any, len(aliases))
	dest := make([]any, len(aliases))
	for i := range dest {
		dest[i] = &values[i]
	}
	if err := db.QueryRow(ctx, db.ExecutorFor(ctx, q.store.Backend), statement, args, dest...); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(aliases))
	for i, alias := range aliases {
		value, err := q.store.decodeField(outputs[i], values[i])
		if err == nil {
			value, err = outputs[i].Clean(ctx, value)
		}
		if err != nil {
			return nil, fmt.Errorf("orm: aggregate output %s: %w", alias, err)
		}
		result[alias] = value
	}
	return result, nil
}

func validateAggregateExpression(expression db.Expression, inside bool, depth int) (bool, error) {
	if depth > 64 {
		return false, errors.New("orm: aggregate expression exceeds depth bound")
	}
	aggregate := expression.Kind == "function" && sqlcompiler.IsAggregateFunction(expression.Name)
	if aggregate && inside {
		return false, errors.New("orm: nested aggregate requires an explicit subquery")
	}
	if expression.Kind == "field" && !inside {
		return false, errors.New("orm: ungrouped field outside aggregate")
	}
	if aggregate && len(expression.Args) != 1 {
		return false, errors.New("orm: aggregate requires one argument")
	}
	if aggregate && expression.Args[0].Kind == "field" && expression.Args[0].Name == "*" && (strings.ToUpper(expression.Name) != "COUNT" || expression.Distinct) {
		return false, errors.New("orm: star is supported only by non-distinct COUNT")
	}
	if expression.Filter != nil {
		if !aggregate {
			return false, errors.New("orm: FILTER requires an aggregate")
		}
		if err := validateAggregateFilter(*expression.Filter, depth+1); err != nil {
			return false, err
		}
	}
	found := aggregate
	for _, branch := range expression.Branches {
		condition, err := validateAggregateCondition(branch.Condition, inside || aggregate, depth+1)
		if err != nil {
			return false, err
		}
		then, err := validateAggregateExpression(branch.Then, inside || aggregate, depth+1)
		if err != nil {
			return false, err
		}
		found = found || condition || then
	}
	for _, argument := range expression.Args {
		nested, err := validateAggregateExpression(argument, inside || aggregate, depth+1)
		if err != nil {
			return false, err
		}
		found = found || nested
	}
	return found, nil
}

func validateAggregateFilter(predicate db.Predicate, depth int) error {
	_, err := validateAggregateCondition(predicate, true, depth)
	return err
}

func validateAggregateCondition(predicate db.Predicate, inside bool, depth int) (bool, error) {
	if depth > 64 {
		return false, errors.New("orm: aggregate condition exceeds depth bound")
	}
	if predicate.Field != "" && !inside {
		return false, errors.New("orm: ungrouped conditional field outside aggregate")
	}
	found := false
	if predicate.Expression != nil {
		nested, err := validateAggregateExpression(*predicate.Expression, inside, depth+1)
		if err != nil {
			return false, err
		}
		found = found || nested
	}
	values := []any{predicate.Value}
	if predicate.Lookup == "in" || predicate.Lookup == "range" {
		value := reflect.ValueOf(predicate.Value)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			values = make([]any, value.Len())
			for i := range values {
				values[i] = value.Index(i).Interface()
			}
		}
	}
	for _, value := range values {
		if expression, ok := value.(db.Expression); ok {
			nested, err := validateAggregateExpression(expression, inside, depth+1)
			if err != nil {
				return false, err
			}
			found = found || nested
		}
	}
	for _, child := range predicate.Children {
		nested, err := validateAggregateCondition(child, inside, depth+1)
		if err != nil {
			return false, err
		}
		found = found || nested
	}
	return found, nil
}
