package sqlcompiler

import (
	"errors"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type Assignment struct {
	Field      string
	Expression db.Expression
}

// Update compiles a single-table SQL mutation. No relation is implicitly joined
// and assignments are expressions over the old row, not sequential Go writes.
func Update(dialect db.Dialect, schema models.Schema, where db.Predicate, assignments []Assignment) (string, []any, error) {
	if len(assignments) == 0 {
		return "", nil, errors.New("orm: Update requires assignments")
	}
	table, err := dialect.QuoteIdentifier(schema.DBTable())
	if err != nil {
		return "", nil, err
	}
	c := &Compiler{Dialect: dialect, Schema: schema}
	parts := make([]string, 0, len(assignments))
	seen := map[string]bool{}
	for _, assignment := range assignments {
		field, ok := schema.Field(assignment.Field)
		if !ok || !field.IsStored() || field.Kind == models.Generated || field.GeneratedExpression != "" || seen[field.Name] {
			return "", nil, errors.New("orm: invalid or duplicate update assignment")
		}
		seen[field.Name] = true
		if err := ValidateUpdateExpression(assignment.Expression); err != nil {
			return "", nil, err
		}
		column, err := dialect.QuoteIdentifier(field.DBColumn())
		if err != nil {
			return "", nil, err
		}
		value, err := c.Expression(assignment.Expression)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, column+" = "+value)
	}
	if err := validateUpdatePredicate(where, 0); err != nil {
		return "", nil, err
	}
	filter, err := c.Predicate(where)
	if err != nil {
		return "", nil, err
	}
	statement := "UPDATE " + table + " SET " + strings.Join(parts, ", ")
	if filter != "" {
		statement += " WHERE " + filter
	}
	return statement, c.Args, nil
}

// ValidateUpdateExpression rejects aggregate/window operations before a write.
// Conditional predicates and membership candidates are part of that tree too.
func ValidateUpdateExpression(expression db.Expression) error {
	return validateUpdateExpression(expression, 0)
}

// ValidateRowExpression limits a per-row projection to scalar expressions.
// Aggregate and window expressions need a different grouping/execution owner.
func ValidateRowExpression(expression db.Expression) error {
	return validateUpdateExpression(expression, 0)
}

func validateUpdateExpression(expression db.Expression, depth int) error {
	if depth > 64 {
		return errors.New("orm: row expression exceeds nesting limit")
	}
	if expression.Filter != nil || expression.Distinct {
		return errors.New("orm: row expression cannot contain aggregate filters or distinct expressions")
	}
	if expression.Kind == "field" && expression.Name == "*" {
		return errors.New("orm: row expression requires concrete field references")
	}
	if expression.Kind == "function" {
		name := strings.ToUpper(expression.Name)
		if IsAggregateFunction(name) {
			return errors.New("orm: row expression cannot contain aggregates")
		}
		switch name {
		case "ROW_NUMBER", "RANK", "DENSE_RANK", "PERCENT_RANK", "CUME_DIST", "NTILE", "LAG", "LEAD", "FIRST_VALUE", "LAST_VALUE", "NTH_VALUE":
			return errors.New("orm: row expression cannot contain window functions")
		}
	}
	for _, arg := range expression.Args {
		if err := validateUpdateExpression(arg, depth+1); err != nil {
			return err
		}
	}
	for _, branch := range expression.Branches {
		if err := validateUpdatePredicate(branch.Condition, depth+1); err != nil {
			return err
		}
		if err := validateUpdateExpression(branch.Then, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateUpdatePredicate(predicate db.Predicate, depth int) error {
	if depth > 64 {
		return errors.New("orm: row predicate exceeds nesting limit")
	}
	if predicate.Expression != nil {
		if err := validateUpdateExpression(*predicate.Expression, depth+1); err != nil {
			return err
		}
	}
	if expression, ok := predicate.Value.(db.Expression); ok {
		if err := validateUpdateExpression(expression, depth+1); err != nil {
			return err
		}
	} else if predicate.Lookup == "in" || predicate.Lookup == "range" {
		value := reflect.ValueOf(predicate.Value)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			for i := 0; i < value.Len(); i++ {
				if expression, ok := value.Index(i).Interface().(db.Expression); ok {
					if err := validateUpdateExpression(expression, depth+1); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, child := range predicate.Children {
		if err := validateUpdatePredicate(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
