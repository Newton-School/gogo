package sqlcompiler

import (
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// AggregateFieldReferences returns only fields used inside aggregate arguments
// and row FILTER clauses. It resolves alias dependencies without evaluating
// values, providers or SQL. Scalar row annotations never invent collection joins.
func AggregateFieldReferences(schema models.Schema, projections, aliases []db.Projection) ([]string, error) {
	declared := make(map[string]db.Projection, len(aliases))
	for _, alias := range aliases {
		declared[alias.Alias] = alias
	}
	fields, nodes := map[string]bool{}, 0
	enter := func(depth int) error {
		nodes++
		if depth > 64 || nodes > 8192 {
			return errors.New("orm: aggregate relation expression exceeds depth or work bound")
		}
		return nil
	}
	var expression func(db.Expression, bool, int) error
	var predicate func(db.Predicate, bool, int) error
	expression = func(value db.Expression, inside bool, depth int) error {
		if err := enter(depth); err != nil {
			return err
		}
		if value.Kind == "invalid_tree" {
			return errors.New("orm: invalid aggregate relation expression")
		}
		// Window aggregates preserve rows; their arguments use only already
		// resolved scalar/to-one paths, never inferred collection joins.
		if value.Kind == "window" {
			return nil
		}
		if value.Kind == "field" {
			if _, stored := schema.Field(value.Name); !stored {
				prefix, _, _ := strings.Cut(value.Name, "__")
				if alias, ok := declared[prefix]; ok {
					return expression(alias.Expression, inside, depth+1)
				}
			}
			if inside && strings.Contains(value.Name, "__") {
				fields[value.Name] = true
			}
			return nil
		}
		inside = inside || value.Kind == "function" && IsAggregateFunction(value.Name)
		if value.Filter != nil {
			if err := predicate(*value.Filter, inside, depth+1); err != nil {
				return err
			}
		}
		for _, arg := range value.Args {
			if err := expression(arg, inside, depth+1); err != nil {
				return err
			}
		}
		for _, branch := range value.Branches {
			if err := predicate(branch.Condition, inside, depth+1); err != nil {
				return err
			}
			if err := expression(branch.Then, inside, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	predicate = func(value db.Predicate, inside bool, depth int) error {
		if err := enter(depth); err != nil {
			return err
		}
		if value.Expression != nil {
			if err := expression(*value.Expression, inside, depth+1); err != nil {
				return err
			}
		} else if value.Field != "" {
			if err := expression(db.Expression{Kind: "field", Name: value.Field}, inside, depth+1); err != nil {
				return err
			}
		}
		values := []any{value.Value}
		if value.Lookup == "in" || value.Lookup == "range" {
			list := reflect.ValueOf(value.Value)
			if list.IsValid() && (list.Kind() == reflect.Slice || list.Kind() == reflect.Array) {
				if list.Len() > 8192 {
					return errors.New("orm: aggregate relation membership exceeds work bound")
				}
				values = make([]any, list.Len())
				for i := range values {
					values[i] = list.Index(i).Interface()
				}
			}
		}
		for _, value := range values {
			if e, ok := value.(db.Expression); ok {
				if err := expression(e, inside, depth+1); err != nil {
					return err
				}
			}
		}
		for _, child := range value.Children {
			if err := predicate(child, inside, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, projection := range projections {
		if err := expression(projection.Expression, false, 0); err != nil {
			return nil, err
		}
	}
	for _, alias := range aliases {
		if err := expression(alias.Expression, false, 0); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
