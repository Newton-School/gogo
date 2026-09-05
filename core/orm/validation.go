package orm

import (
	"context"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"strings"
)

func (s *Store) ValidateUnique(ctx context.Context, record models.Record, exclude []string) error {
	skip := map[string]bool{}
	for _, name := range exclude {
		skip[name] = true
	}
	validation := &models.ValidationError{}
	for _, field := range record.Schema().Fields {
		if !field.Unique || skip[field.Name] {
			continue
		}
		exists, err := s.duplicate(ctx, record, []string{field.Name})
		if err != nil {
			return err
		}
		if exists {
			validation.Add(field.Name, "unique", "An object with this value already exists.")
		}
	}
	if validation.Empty() {
		return nil
	}
	return validation
}
func (s *Store) ValidateConstraints(ctx context.Context, record models.Record, exclude []string) error {
	skip := map[string]bool{}
	for _, name := range exclude {
		skip[name] = true
	}
	validation := &models.ValidationError{}
	for _, constraint := range record.Schema().Constraints {
		if strings.EqualFold(constraint.Kind, "unique") {
			excluded := false
			for _, name := range constraint.Fields {
				excluded = excluded || skip[name]
			}
			if excluded {
				continue
			}
			exists, err := s.duplicate(ctx, record, constraint.Fields)
			if err != nil {
				return err
			}
			if exists {
				validation.Add(models.NonFieldErrors, "unique", fmt.Sprintf("Constraint %s is violated.", constraint.Name))
			}
		} else if strings.EqualFold(constraint.Kind, "check") {
			if constraint.Expression == "" {
				return fmt.Errorf("orm: empty constraint %s", constraint.Name)
			}
			if len(exclude) > 0 {
				continue
			}
			schema := record.Schema()
			columns := []string{}
			args := []any{}
			for _, field := range schema.Fields {
				if !field.IsStored() {
					continue
				}
				value, err := record.Get(field.Name)
				if err != nil {
					return err
				}
				value, err = encodeField(field, value)
				if err != nil {
					return err
				}
				column, err := s.Backend.Dialect().QuoteIdentifier(field.DBColumn())
				if err != nil {
					return err
				}
				kind, err := s.Backend.Dialect().FieldType(field)
				if err != nil {
					return err
				}
				if field.IsAuto() {
					kind = strings.Split(kind, " GENERATED")[0]
				}
				args = append(args, value)
				columns = append(columns, "CAST("+s.Backend.Dialect().Placeholder(len(args))+" AS "+kind+") AS "+column)
			}
			if strings.ContainsAny(constraint.Expression, ";\x00") {
				return fmt.Errorf("orm: invalid constraint expression")
			}
			query := "SELECT COALESCE((" + constraint.Expression + "),true) FROM (SELECT " + strings.Join(columns, ", ") + ") AS proposed"
			var valid bool
			if err := db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), query, args, &valid); err != nil {
				return err
			}
			if !valid {
				validation.Add(models.NonFieldErrors, "constraint", fmt.Sprintf("Constraint %s is violated.", constraint.Name))
			}
		}
	}
	if validation.Empty() {
		return nil
	}
	return validation
}
func (s *Store) duplicate(ctx context.Context, record models.Record, fields []string) (bool, error) {
	dialect := s.Backend.Dialect()
	table, err := dialect.QuoteIdentifier(record.Schema().DBTable())
	if err != nil {
		return false, err
	}
	parts := []string{}
	args := []any{}
	for _, name := range fields {
		field, ok := record.Schema().Field(name)
		if !ok {
			return false, fmt.Errorf("orm: unknown constraint field %s", name)
		}
		value, err := record.Get(name)
		if err != nil {
			return false, err
		}
		if value == nil {
			return false, nil
		}
		value, err = encodeField(field, value)
		if err != nil {
			return false, err
		}
		column, _ := dialect.QuoteIdentifier(field.DBColumn())
		args = append(args, value)
		parts = append(parts, column+" = "+dialect.Placeholder(len(args)))
	}
	if len(parts) == 0 {
		return false, fmt.Errorf("orm: empty uniqueness key")
	}
	if record.State().Persisted {
		pk, argsPK, err := primaryWhere(dialect, record, len(args))
		if err != nil {
			return false, err
		}
		parts = append(parts, "NOT ("+pk+")")
		args = append(args, argsPK...)
	}
	var exists bool
	err = db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+strings.Join(parts, " AND ")+")", args, &exists)
	return exists, err
}
