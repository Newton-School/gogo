package orm

import (
	"context"
	"fmt"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"regexp"
	"strings"
	"time"
)

func (s *Store) ValidateUnique(ctx context.Context, record models.Record, exclude []string) error {
	if e := s.refuseRouting(); e != nil {
		return e
	}
	skip := excludedFields(exclude)
	validation := &models.ValidationError{}
	for _, field := range record.Schema().Fields {
		if skip[field.Name] {
			continue
		}
		if field.Unique || field.PrimaryKey {
			value, err := record.Get(field.Name)
			if err != nil {
				return err
			}
			if !(record.State().Adding() && field.IsAuto() && models.IsEmptyValue(value)) && !(record.State().Persisted && field.PrimaryKey) {
				exists, err := s.duplicate(ctx, record, []string{field.Name}, duplicateOptions{})
				if err != nil {
					return err
				}
				if exists {
					validation.Add(field.Name, "unique", "An object with this value already exists.")
				}
			}
		}
		for _, period := range []struct{ field, part string }{{field.UniqueForDate, "date"}, {field.UniqueForMonth, "month"}, {field.UniqueForYear, "year"}} {
			if period.field == "" || skip[period.field] {
				continue
			}
			exists, err := s.duplicate(ctx, record, []string{field.Name}, duplicateOptions{DateField: period.field, DatePart: period.part})
			if err != nil {
				return err
			}
			if exists {
				validation.Add(field.Name, "unique_for_date", "An object with this value already exists for this date period.")
			}
		}
	}
	if keys := record.Schema().PKFields(); len(keys) > 1 && record.State().Adding() {
		names := []string{}
		skipKey := false
		for _, key := range keys {
			names = append(names, key.Name)
			skipKey = skipKey || skip[key.Name]
		}
		if !skipKey {
			exists, err := s.duplicate(ctx, record, names, duplicateOptions{})
			if err != nil {
				return err
			}
			if exists {
				validation.Add(models.NonFieldErrors, "unique_together", "An object with this primary key already exists.")
			}
		}
	}
	if validation.Empty() {
		return nil
	}
	return validation
}
func excludedFields(exclude []string) map[string]bool {
	skip := map[string]bool{}
	for _, name := range exclude {
		skip[name] = true
	}
	return skip
}

func (s *Store) ValidateConstraints(ctx context.Context, record models.Record, exclude []string) error {
	if e := s.refuseRouting(); e != nil {
		return e
	}
	skip := excludedFields(exclude)
	validation := &models.ValidationError{}
	for _, constraint := range record.Schema().Constraints {
		excluded := false
		for _, name := range constraint.Fields {
			excluded = excluded || skip[name]
		}
		for _, name := range expressionFields(record.Schema(), constraint.Condition+" "+constraint.Expression) {
			excluded = excluded || skip[name]
		}
		if excluded {
			continue
		}
		switch strings.ToLower(constraint.Kind) {
		case "unique":
			if constraint.Condition != "" {
				applies, err := s.evaluate(ctx, record, constraint.Condition, exclude, false)
				if err != nil {
					return err
				}
				if !applies {
					continue
				}
			}
			nullsEqual := constraint.NullsDistinct != nil && !*constraint.NullsDistinct
			exists, err := s.duplicate(ctx, record, constraint.Fields, duplicateOptions{NullsEqual: nullsEqual, Condition: constraint.Condition})
			if err != nil {
				return err
			}
			if exists {
				validation.Add(models.NonFieldErrors, "unique", fmt.Sprintf("Constraint %s is violated.", constraint.Name))
			}
		case "check":
			valid, err := s.evaluate(ctx, record, constraint.Expression, exclude, true)
			if err != nil {
				return err
			}
			if !valid {
				validation.Add(models.NonFieldErrors, "constraint", fmt.Sprintf("Constraint %s is violated.", constraint.Name))
			}
		default:
			return fmt.Errorf("orm: unsupported constraint kind %s", constraint.Kind)
		}
	}
	if validation.Empty() {
		return nil
	}
	return validation
}

// Expressions are trusted schema source, never request input. Token scanning
// identifies local field dependencies; SQL evaluation is owned by the backend.
var expressionTokens = regexp.MustCompile(`'([^']|'')*'|"([^"]|"")*"|[A-Za-z_][A-Za-z0-9_]*`)

func expressionFields(schema models.Schema, expression string) []string {
	words := map[string]bool{}
	for _, token := range expressionTokens.FindAllString(expression, -1) {
		if strings.HasPrefix(token, "'") {
			continue
		}
		words[strings.Trim(token, "\"")] = true
	}
	result := []string{}
	for _, field := range schema.Fields {
		if words[field.DBColumn()] || words[field.Name] {
			result = append(result, field.Name)
		}
	}
	return result
}
func (s *Store) evaluate(ctx context.Context, record models.Record, expression string, exclude []string, nullResult bool) (bool, error) {
	if expression == "" || strings.ContainsAny(expression, ";\x00") {
		return false, fmt.Errorf("orm: invalid constraint expression")
	}
	skip := excludedFields(exclude)
	columns := []string{}
	args := []any{}
	for _, field := range record.Schema().Fields {
		if !field.IsStored() {
			continue
		}
		var value any
		var err error
		if !skip[field.Name] {
			value, err = record.Get(field.Name)
			if err != nil {
				return false, err
			}
			value, err = encodeField(field, value)
			if err != nil {
				return false, err
			}
		}
		column, err := s.Backend.Dialect().QuoteIdentifier(field.DBColumn())
		if err != nil {
			return false, err
		}
		typeField := field
		if field.Relation != nil && s.Registry != nil {
			if target, ok := s.Registry.Get(field.Relation.Target); ok {
				keys := field.Relation.TargetFields
				if len(keys) == 0 {
					for _, pk := range target.PKFields() {
						keys = append(keys, pk.Name)
					}
				}
				if len(keys) == 1 {
					if found, ok := target.Field(keys[0]); ok {
						typeField = found
					}
				}
			}
		}
		kind, err := s.Backend.Dialect().FieldType(typeField)
		if err != nil {
			return false, err
		}
		kind = strings.Split(kind, " GENERATED")[0]
		args = append(args, value)
		columns = append(columns, "CAST("+s.Backend.Dialect().Placeholder(len(args))+" AS "+kind+") AS "+column)
	}
	defaultValue := "false"
	if nullResult {
		defaultValue = "true"
	}
	query := "SELECT COALESCE((" + expression + ")," + defaultValue + ") FROM (SELECT " + strings.Join(columns, ", ") + ") AS proposed"
	var valid bool
	err := db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), query, args, &valid)
	return valid, err
}

type duplicateOptions struct {
	NullsEqual                     bool
	Condition, DateField, DatePart string
}

func (s *Store) duplicate(ctx context.Context, record models.Record, fields []string, options duplicateOptions) (bool, error) {
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
		column, _ := dialect.QuoteIdentifier(field.DBColumn())
		if value == nil {
			if !options.NullsEqual {
				return false, nil
			}
			parts = append(parts, column+" IS NULL")
			continue
		}
		value, err = encodeField(field, value)
		if err != nil {
			return false, err
		}
		args = append(args, value)
		parts = append(parts, column+" = "+dialect.Placeholder(len(args)))
	}
	if len(parts) == 0 {
		return false, fmt.Errorf("orm: empty uniqueness key")
	}
	if options.Condition != "" {
		if strings.ContainsAny(options.Condition, ";\x00") {
			return false, fmt.Errorf("orm: invalid uniqueness condition")
		}
		parts = append(parts, "("+options.Condition+")")
	}
	if options.DateField != "" {
		field, ok := record.Schema().Field(options.DateField)
		if !ok || (field.Kind != models.Date && field.Kind != models.DateTime) {
			return false, fmt.Errorf("orm: date uniqueness requires Date or DateTime field")
		}
		value, err := record.Get(field.Name)
		if err != nil {
			return false, err
		}
		if value == nil {
			return false, nil
		}
		date, ok := value.(time.Time)
		if !ok {
			return false, fmt.Errorf("orm: date uniqueness requires time.Time")
		}
		location := time.UTC
		if s.Timezone != nil {
			if current := s.Timezone(ctx); current != nil {
				location = current
			}
		}
		column, _ := dialect.QuoteIdentifier(field.DBColumn())
		if field.Kind == models.DateTime {
			date = date.In(location)
			args = append(args, location.String())
			column = "(" + column + " AT TIME ZONE " + dialect.Placeholder(len(args)) + ")"
		}
		periods := []string{options.DatePart}
		if options.DatePart == "date" {
			periods = []string{"year", "month", "day"}
		}
		for _, period := range periods {
			var n int
			switch period {
			case "year":
				n = date.Year()
			case "month":
				n = int(date.Month())
			case "day":
				n = date.Day()
			default:
				return false, fmt.Errorf("orm: invalid date uniqueness period")
			}
			args = append(args, n)
			parts = append(parts, "EXTRACT("+period+" FROM "+column+")="+dialect.Placeholder(len(args)))
		}
	}
	if record.State().Persisted {
		pk, values, err := primaryWhere(dialect, record, len(args))
		if err != nil {
			return false, err
		}
		parts = append(parts, "NOT ("+pk+")")
		args = append(args, values...)
	}
	var exists bool
	err = db.QueryRow(ctx, db.ExecutorFor(ctx, s.Backend), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+strings.Join(parts, " AND ")+")", args, &exists)
	return exists, err
}
