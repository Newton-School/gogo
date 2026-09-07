package migrations

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func cloneConstraint(value models.Constraint) models.Constraint {
	value.Fields = slices.Clone(value.Fields)
	if value.NullsDistinct != nil {
		copy := *value.NullsDistinct
		value.NullsDistinct = &copy
	}
	return value
}

func constraintEquivalent(a, b models.Constraint) bool {
	if !slices.Equal(a.Fields, b.Fields) {
		return false
	}
	a.Fields, b.Fields = nil, nil
	return reflect.DeepEqual(a, b)
}

func constraintNames(values []models.Constraint) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.Name
	}
	return result
}

func detectConstraints(before, after models.Schema) (remove, add []Operation, order *Operation, err error) {
	old, next := map[string]models.Constraint{}, map[string]models.Constraint{}
	for _, value := range before.Constraints {
		if _, duplicate := old[value.Name]; duplicate {
			return nil, nil, nil, errors.New("migrations: historical constraint names are ambiguous")
		}
		old[value.Name] = value
	}
	for _, value := range after.Constraints {
		next[value.Name] = value
	}
	result := []models.Constraint{}
	for _, value := range before.Constraints {
		other, exists := next[value.Name]
		if exists && constraintEquivalent(value, other) {
			result = append(result, value)
		} else {
			remove = append(remove, RemoveConstraint(before, value))
		}
	}
	for _, value := range after.Constraints {
		other, exists := old[value.Name]
		if exists && constraintEquivalent(value, other) {
			continue
		}
		add = append(add, AddConstraint(after, value))
		result = append(result, value)
	}
	current, desired := constraintNames(result), constraintNames(after.Constraints)
	if !slices.Equal(current, desired) {
		value := Operation{Kind: "constraint_order", Schema: after.Clone(), OldConstraintOrder: current, ConstraintOrder: desired}
		order = &value
	}
	return remove, add, order, nil
}

func applyConstraintState(schema models.Schema, operation Operation) (models.Schema, error) {
	schema = schema.Clone()
	if operation.Kind == "constraint_order" {
		if !slices.Equal(constraintNames(schema.Constraints), operation.OldConstraintOrder) || len(operation.ConstraintOrder) != len(schema.Constraints) {
			return schema, errors.New("migrations: constraint order differs from historical state")
		}
		byName := map[string]models.Constraint{}
		for _, value := range schema.Constraints {
			byName[value.Name] = value
		}
		ordered := make([]models.Constraint, len(operation.ConstraintOrder))
		for i, name := range operation.ConstraintOrder {
			value, exists := byName[name]
			if !exists {
				return schema, errors.New("migrations: constraint order must name every constraint exactly once")
			}
			ordered[i] = value
			delete(byName, name)
		}
		schema.Constraints = ordered
		return schema, schema.Validate()
	}
	if operation.Constraint == nil {
		return schema, errors.New("migrations: constraint operation requires immutable descriptor")
	}
	position := -1
	for i, value := range schema.Constraints {
		if value.Name == operation.Constraint.Name {
			position = i
			break
		}
	}
	switch operation.Kind {
	case "add_constraint":
		if position >= 0 {
			return schema, errors.New("migrations: constraint already exists in historical state")
		}
		schema.Constraints = append(schema.Constraints, cloneConstraint(*operation.Constraint))
	case "remove_constraint":
		if position < 0 || !constraintEquivalent(schema.Constraints[position], *operation.Constraint) {
			return schema, errors.New("migrations: removed constraint differs from historical state")
		}
		schema.Constraints = append(schema.Constraints[:position], schema.Constraints[position+1:]...)
		if len(schema.Constraints) == 0 {
			schema.Constraints = nil
		}
	}
	return schema, schema.Validate()
}

func (e *Executor) validateConstraintOperations(migration Migration) error {
	for _, operation := range migration.Operations {
		if operation.Kind != "add_constraint" && operation.Kind != "remove_constraint" {
			continue
		}
		if operation.Constraint == nil {
			return errors.New("migrations: constraint operation requires immutable descriptor")
		}
		if _, ok := e.Editor.(db.ConstraintLifecycleEditor); !ok {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Constraint operations require a definition-aware schema editor"}
		}
		if migration.NonAtomic {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Constraint lifecycle requires an atomic migration"}
		}
	}
	return nil
}

func (e *Executor) runConstraint(ctx context.Context, executor db.Executor, operation Operation, remove bool) error {
	if operation.Constraint == nil {
		return errors.New("migrations: constraint operation requires immutable descriptor")
	}
	editor, ok := e.Editor.(db.ConstraintLifecycleEditor)
	if !ok {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Constraint operations require a definition-aware schema editor"}
	}
	if remove {
		return editor.RemoveConstraint(ctx, executor, operation.Schema, *operation.Constraint)
	}
	return editor.AddConstraint(ctx, executor, operation.Schema, *operation.Constraint)
}
