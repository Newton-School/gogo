package testing

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/Newton-School/gogo/orm/dialects"
)

type Fixture struct {
	Model      string
	PK         any
	Fields     map[string]any
	NaturalKey []any
}

type Sequence func(int) any

type Factory struct {
	model            string
	defaults         map[string]any
	naturalKeyFields []string
	sequence         int
}

func Seq(fn func(int) any) Sequence {
	return Sequence(fn)
}

func NewFactory(model string, defaults map[string]any) *Factory {
	return &Factory{model: model, defaults: cloneAnyMap(defaults)}
}

func (f *Factory) WithNaturalKey(fields ...string) *Factory {
	f.naturalKeyFields = append([]string(nil), fields...)
	return f
}

func (f *Factory) Build(overrides map[string]any) Fixture {
	f.sequence++
	values := cloneAnyMap(f.defaults)
	for key, value := range overrides {
		values[key] = value
	}
	fields := make(map[string]any, len(values))
	var pk any
	for key, value := range values {
		resolved := resolveValue(value, f.sequence)
		if key == "id" || key == "pk" {
			pk = resolved
			continue
		}
		fields[key] = resolved
	}
	fixture := Fixture{Model: f.model, PK: pk, Fields: fields}
	for _, field := range f.naturalKeyFields {
		fixture.NaturalKey = append(fixture.NaturalKey, fields[field])
	}
	return fixture
}

func (f *Factory) BuildMany(count int) []Fixture {
	fixtures := make([]Fixture, count)
	for i := 0; i < count; i++ {
		fixtures[i] = f.Build(nil)
	}
	return fixtures
}

func LoadFixtures(ctx context.Context, database *TestDatabase, fixtures []Fixture) error {
	if database == nil {
		return fmt.Errorf("test database is required")
	}
	return database.LoadFixtures(ctx, FixtureRows(fixtures))
}

func DumpFixtures(ctx context.Context, database *TestDatabase, model string) ([]Fixture, error) {
	if database == nil {
		return nil, fmt.Errorf("test database is required")
	}
	table := ModelTableName(model)
	rows, err := database.SQLDB().QueryContext(ctx, "SELECT * FROM "+database.quote(table)+" ORDER BY "+database.quote("id"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFixtures(rows, model)
}

func TransactionalFixtures(ctx context.Context, database *TestDatabase, fixtures []Fixture, fn func(context.Context, Transaction) error) error {
	if database == nil {
		return fmt.Errorf("test database is required")
	}
	return database.RollbackTransaction(ctx, func(ctx context.Context, tx Transaction) error {
		if err := loadFixtureRows(ctx, tx, database.Database.Dialect, FixtureRows(fixtures)); err != nil {
			return err
		}
		if fn != nil {
			return fn(ctx, tx)
		}
		return nil
	})
}

func FixtureRows(fixtures []Fixture) []FixtureRow {
	rows := make([]FixtureRow, len(fixtures))
	for i, fixture := range fixtures {
		values := cloneAnyMap(fixture.Fields)
		if fixture.PK != nil {
			values["id"] = fixture.PK
		}
		rows[i] = FixtureRow{Table: ModelTableName(fixture.Model), Values: values}
	}
	return rows
}

func ModelTableName(model string) string {
	app, name, ok := strings.Cut(model, ".")
	if !ok {
		return toSnake(model)
	}
	return toSnake(app) + "_" + toSnake(name)
}

func loadFixtureRows(ctx context.Context, executor Transaction, dialect dialects.Dialect, rows []FixtureRow) error {
	for _, row := range rows {
		if len(row.Values) == 0 {
			return fmt.Errorf("fixture row for %s has no values", row.Table)
		}
		columns := sortedKeys(row.Values)
		placeholders := make([]string, len(columns))
		values := make([]any, len(columns))
		for i, column := range columns {
			placeholders[i] = dialect.Placeholder(i + 1)
			values[i] = row.Values[column]
		}
		statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", dialect.QuoteIdent(row.Table), quoteFixtureColumns(dialect, columns), strings.Join(placeholders, ", "))
		if _, err := executor.ExecContext(ctx, statement, values...); err != nil {
			return err
		}
	}
	return nil
}

func quoteFixtureColumns(dialect dialects.Dialect, columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = dialect.QuoteIdent(column)
	}
	return strings.Join(quoted, ", ")
}

func scanFixtures(rows *sql.Rows, model string) ([]Fixture, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var fixtures []Fixture
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		fixture := Fixture{Model: model, Fields: map[string]any{}}
		for i, column := range columns {
			value := normalizeFixtureValue(values[i])
			if column == "id" || column == "pk" {
				fixture.PK = value
				continue
			}
			fixture.Fields[column] = value
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures, rows.Err()
}

func normalizeFixtureValue(value any) any {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return value
}

func resolveValue(value any, sequence int) any {
	if seq, ok := value.(Sequence); ok {
		return seq(sequence)
	}
	if fn, ok := value.(func(int) any); ok {
		return fn(sequence)
	}
	return value
}

func cloneAnyMap(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func toSnake(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for index, char := range value {
		if char == '-' || char == ' ' || char == '_' {
			if !lastUnderscore && builder.Len() > 0 {
				builder.WriteByte('_')
				lastUnderscore = true
			}
			continue
		}
		if unicode.IsUpper(char) {
			if index > 0 && !lastUnderscore {
				builder.WriteByte('_')
			}
			builder.WriteRune(unicode.ToLower(char))
			lastUnderscore = false
			continue
		}
		builder.WriteRune(unicode.ToLower(char))
		lastUnderscore = false
	}
	return strings.Trim(builder.String(), "_")
}
