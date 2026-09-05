package sqlcompiler

import (
	"errors"
	"reflect"

	"github.com/Newton-School/gogo/core/db"
)

// ContainsAggregate inspects a bounded expression tree without SQL compilation
// or callbacks. Query builders inspect every declared alias, so alias references
// do not need resolution here; terminal grouped validation resolves them and
// rejects nested aggregates, invalid field uses and dependency cycles.
func ContainsAggregate(expression db.Expression) (bool, error) {
	nodes, found := 0, false
	enter := func(depth int) error {
		nodes++
		if depth > 64 || nodes > 8192 {
			return errors.New("orm: aggregate detection exceeds depth or work bound")
		}
		return nil
	}
	var visitExpression func(db.Expression, int) error
	var visitPredicate func(db.Predicate, int) error
	visitExpression = func(expression db.Expression, depth int) error {
		if err := enter(depth); err != nil {
			return err
		}
		if expression.Kind == "invalid_tree" {
			return errors.New("orm: invalid expression tree")
		}
		if expression.Kind == "function" && IsAggregateFunction(expression.Name) {
			found = true
		}
		if expression.Filter != nil {
			if err := visitPredicate(*expression.Filter, depth+1); err != nil {
				return err
			}
		}
		for _, arg := range expression.Args {
			if err := visitExpression(arg, depth+1); err != nil {
				return err
			}
		}
		for _, branch := range expression.Branches {
			if err := visitPredicate(branch.Condition, depth+1); err != nil {
				return err
			}
			if err := visitExpression(branch.Then, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	visitPredicate = func(predicate db.Predicate, depth int) error {
		if err := enter(depth); err != nil {
			return err
		}
		if predicate.Expression != nil {
			if err := visitExpression(*predicate.Expression, depth+1); err != nil {
				return err
			}
		}
		values := []any{predicate.Value}
		if predicate.Lookup == "in" || predicate.Lookup == "range" {
			value := reflect.ValueOf(predicate.Value)
			if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
				if value.Len() > 8192 {
					return errors.New("orm: aggregate detection membership exceeds work bound")
				}
				values = make([]any, value.Len())
				for i := range values {
					values[i] = value.Index(i).Interface()
				}
			}
		}
		for _, value := range values {
			if expression, ok := value.(db.Expression); ok {
				if err := visitExpression(expression, depth+1); err != nil {
					return err
				}
			}
		}
		for _, child := range predicate.Children {
			if err := visitPredicate(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visitExpression(expression, 0); err != nil {
		return false, err
	}
	return found, nil
}
