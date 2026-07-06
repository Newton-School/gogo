package orm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/models"
	"github.com/Newton-School/gogo/orm/dialects"
)

// CompiledSQL stores SQL text and bound args.
type CompiledSQL struct {
	SQL  string
	Args []any
}

// Compiler renders Query state into parameterized SQL.
type Compiler struct {
	Dialect dialects.Dialect
	Lookups *LookupRegistry
}

// NewCompiler creates a SQL compiler for one dialect.
func NewCompiler(dialect dialects.Dialect) Compiler {
	return Compiler{Dialect: dialect, Lookups: NewLookupRegistry()}
}

// CompileSelect compiles a SELECT statement.
func (c Compiler) CompileSelect(query Query) (CompiledSQL, error) {
	return c.compileSelect(query, 1, true)
}

// CompileInsert compiles an INSERT statement.
func (c Compiler) CompileInsert(meta models.Metadata, values map[string]any, returning []string) (CompiledSQL, error) {
	if err := c.validateValues(meta, values); err != nil {
		return CompiledSQL{}, err
	}
	keys := sortedKeys(values)
	if len(keys) == 0 {
		return CompiledSQL{}, fmt.Errorf("%w: insert values are required", ErrInvalidQuery)
	}
	columns := make([]string, len(keys))
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		columns[i] = c.quoteField(meta, key)
		placeholders[i] = c.Dialect.Placeholder(i + 1)
		args[i] = values[key]
	}
	sql := "INSERT INTO " + c.table(meta) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if len(returning) > 0 {
		if !c.Dialect.SupportsReturning() {
			return CompiledSQL{}, fmt.Errorf("%w: returning is not supported", ErrUnsupportedDialectFeature)
		}
		parts := make([]string, len(returning))
		for i, field := range returning {
			if err := c.validateField(meta, field); err != nil {
				return CompiledSQL{}, err
			}
			parts[i] = c.quoteField(meta, field)
		}
		sql += " RETURNING " + strings.Join(parts, ", ")
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

// CompileUpdate compiles an UPDATE statement.
func (c Compiler) CompileUpdate(query Query, values map[string]any) (CompiledSQL, error) {
	if err := c.validateValues(query.Model, values); err != nil {
		return CompiledSQL{}, err
	}
	keys := sortedKeys(values)
	if len(keys) == 0 {
		return CompiledSQL{}, fmt.Errorf("%w: update values are required", ErrInvalidQuery)
	}
	assignments := make([]string, len(keys))
	args := make([]any, 0, len(keys))
	for i, key := range keys {
		assignments[i] = c.quoteField(query.Model, key) + " = " + c.Dialect.Placeholder(i+1)
		args = append(args, values[key])
	}
	where, nextArgs, err := c.compileWhere(query, len(args)+1)
	if err != nil {
		return CompiledSQL{}, err
	}
	args = append(args, nextArgs...)
	sql := "UPDATE " + c.table(query.Model) + " SET " + strings.Join(assignments, ", ")
	if where != "" {
		sql += " WHERE " + where
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

// CompileDelete compiles a DELETE statement.
func (c Compiler) CompileDelete(query Query) (CompiledSQL, error) {
	if err := c.validateQuery(query); err != nil {
		return CompiledSQL{}, err
	}
	where, args, err := c.compileWhere(query, 1)
	if err != nil {
		return CompiledSQL{}, err
	}
	sql := "DELETE FROM " + c.table(query.Model)
	if where != "" {
		sql += " WHERE " + where
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

// CompileCount compiles a COUNT query.
func (c Compiler) CompileCount(query Query) (CompiledSQL, error) {
	if err := c.validateQuery(query); err != nil {
		return CompiledSQL{}, err
	}
	where, args, err := c.compileWhere(query, 1)
	if err != nil {
		return CompiledSQL{}, err
	}
	sql := "SELECT COUNT(*) FROM " + c.table(query.Model)
	if where != "" {
		sql += " WHERE " + where
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

// CompileExists compiles an EXISTS query.
func (c Compiler) CompileExists(query Query) (CompiledSQL, error) {
	if err := c.validateQuery(query); err != nil {
		return CompiledSQL{}, err
	}
	where, args, err := c.compileWhere(query, 1)
	if err != nil {
		return CompiledSQL{}, err
	}
	inner := "SELECT 1 FROM " + c.table(query.Model)
	if where != "" {
		inner += " WHERE " + where
	}
	inner += " LIMIT 1"
	return CompiledSQL{SQL: "SELECT EXISTS(" + inner + ")", Args: args}, nil
}

// CompileAggregate compiles an aggregate SELECT.
func (c Compiler) CompileAggregate(query Query, aggregates ...AggregateExpression) (CompiledSQL, error) {
	if err := c.validateQuery(query); err != nil {
		return CompiledSQL{}, err
	}
	if len(aggregates) == 0 {
		return CompiledSQL{}, fmt.Errorf("%w: aggregates are required", ErrInvalidQuery)
	}
	parts := make([]string, 0, len(aggregates))
	args := make([]any, 0)
	for _, aggregate := range aggregates {
		fragment, err := aggregate.SelectionSQL(c.Dialect, len(args)+1)
		if err != nil {
			return CompiledSQL{}, err
		}
		parts = append(parts, fragment.SQL)
		args = append(args, fragment.Args...)
	}
	where, whereArgs, err := c.compileWhere(query, len(args)+1)
	if err != nil {
		return CompiledSQL{}, err
	}
	args = append(args, whereArgs...)
	sql := "SELECT " + strings.Join(parts, ", ") + " FROM " + c.table(query.Model)
	if where != "" {
		sql += " WHERE " + where
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

func (c Compiler) compileSelect(query Query, start int, includeSets bool) (CompiledSQL, error) {
	if err := c.validateQuery(query); err != nil {
		return CompiledSQL{}, err
	}
	selectSQL, args, err := c.selectList(query, start)
	if err != nil {
		return CompiledSQL{}, err
	}
	sql := "SELECT " + selectSQL + " FROM " + c.table(query.Model)
	where, whereArgs, err := c.compileWhere(query, start+len(args))
	if err != nil {
		return CompiledSQL{}, err
	}
	args = append(args, whereArgs...)
	if where != "" {
		sql += " WHERE " + where
	}
	if len(query.Grouping) > 0 {
		groups, err := c.fieldList(query.Model, query.Grouping)
		if err != nil {
			return CompiledSQL{}, err
		}
		sql += " GROUP BY " + strings.Join(groups, ", ")
	}
	if len(query.Having) > 0 {
		having, havingArgs, err := c.compilePredicates(query.Model, query.Having, start+len(args), " AND ")
		if err != nil {
			return CompiledSQL{}, err
		}
		args = append(args, havingArgs...)
		sql += " HAVING " + having
	}
	if len(query.Ordering) > 0 {
		ordering, err := c.compileOrdering(query.Model, query.Ordering)
		if err != nil {
			return CompiledSQL{}, err
		}
		sql += " ORDER BY " + ordering
	}
	if limitSQL := c.Dialect.LimitOffset(dialects.LimitOffset{Limit: query.Limit, Offset: query.Offset}); limitSQL != "" {
		sql += " " + limitSQL
	}
	if query.Locking.ForUpdate {
		lock, err := c.Dialect.LockClause(dialects.LockOptions{
			ForUpdate:  query.Locking.ForUpdate,
			NoWait:     query.Locking.NoWait,
			SkipLocked: query.Locking.SkipLocked,
			Of:         query.Locking.Of,
		})
		if err != nil {
			return CompiledSQL{}, fmt.Errorf("%w: %v", ErrUnsupportedDialectFeature, err)
		}
		if lock != "" {
			sql += " " + lock
		}
	}
	if includeSets {
		for _, operation := range query.SetOperations {
			next, err := c.compileSelect(operation.Query, start+len(args), false)
			if err != nil {
				return CompiledSQL{}, err
			}
			sql += " " + setOperationSQL(operation) + " " + next.SQL
			args = append(args, next.Args...)
		}
	}
	return CompiledSQL{SQL: sql, Args: args}, nil
}

func (c Compiler) selectList(query Query, start int) (string, []any, error) {
	prefix := ""
	if query.Distinct {
		if len(query.DistinctFields) > 0 && c.Dialect.Name() == "postgres" {
			fields, err := c.fieldList(query.Model, query.DistinctFields)
			if err != nil {
				return "", nil, err
			}
			prefix = "DISTINCT ON (" + strings.Join(fields, ", ") + ") "
		} else {
			prefix = "DISTINCT "
		}
	}
	parts := make([]string, 0)
	for _, field := range query.SelectedColumns {
		if err := c.validateField(query.Model, field); err != nil {
			return "", nil, err
		}
		parts = append(parts, c.quoteField(query.Model, field))
	}
	annotationKeys := sortedKeys(query.Annotations)
	args := make([]any, 0)
	for _, alias := range annotationKeys {
		expression := query.Annotations[alias]
		if alias == "" || expression.SQL == "" {
			return "", nil, fmt.Errorf("%w: invalid annotation %q", ErrInvalidQuery, alias)
		}
		parts = append(parts, expression.SQL+" AS "+c.Dialect.QuoteIdent(alias))
		args = append(args, expression.Args...)
	}
	for alias, window := range query.Windows {
		if alias == "" || window.Expression == "" {
			return "", nil, fmt.Errorf("%w: invalid window annotation %q", ErrInvalidQuery, alias)
		}
		parts = append(parts, window.Expression+" AS "+c.Dialect.QuoteIdent(alias))
	}
	if len(parts) == 0 {
		parts = append(parts, "*")
	}
	_ = start
	return prefix + strings.Join(parts, ", "), args, nil
}

func (c Compiler) compileWhere(query Query, start int) (string, []any, error) {
	if err := c.validateQuery(query); err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(query.Filters)+len(query.Excludes))
	args := make([]any, 0)
	for _, predicate := range query.Filters {
		sql, predicateArgs, err := c.compilePredicate(query.Model, predicate, start+len(args))
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, sql)
		args = append(args, predicateArgs...)
	}
	for _, predicate := range query.Excludes {
		sql, predicateArgs, err := c.compilePredicate(query.Model, predicate, start+len(args))
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "NOT ("+sql+")")
		args = append(args, predicateArgs...)
	}
	return strings.Join(parts, " AND "), args, nil
}

func (c Compiler) compilePredicates(meta models.Metadata, predicates []Predicate, start int, separator string) (string, []any, error) {
	parts := make([]string, 0, len(predicates))
	args := make([]any, 0)
	for _, predicate := range predicates {
		sql, predicateArgs, err := c.compilePredicate(meta, predicate, start+len(args))
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, sql)
		args = append(args, predicateArgs...)
	}
	return strings.Join(parts, separator), args, nil
}

func (c Compiler) compilePredicate(meta models.Metadata, predicate Predicate, start int) (string, []any, error) {
	if err := c.validateField(meta, predicate.Field); err != nil {
		return "", nil, err
	}
	fragment, err := c.Lookups.Compile(LookupContext{
		Dialect: c.Dialect,
		Column:  c.fieldColumn(meta, predicate.Field),
		Lookup:  predicate.Lookup,
		Value:   predicate.Value,
		Start:   start,
	})
	if err != nil {
		return "", nil, err
	}
	return fragment.SQL, fragment.Args, nil
}

func (c Compiler) validateQuery(query Query) error {
	for _, field := range query.SelectedColumns {
		if err := c.validateField(query.Model, field); err != nil {
			return err
		}
	}
	for _, predicate := range append(append([]Predicate(nil), query.Filters...), query.Excludes...) {
		if err := c.validateField(query.Model, predicate.Field); err != nil {
			return err
		}
	}
	if _, err := c.compileOrdering(query.Model, query.Ordering); err != nil {
		return err
	}
	seenJoins := map[string]Join{}
	for _, join := range query.Joins {
		if existing, ok := seenJoins[join.Path]; ok && (existing.Target != join.Target || existing.Type != join.Type) {
			return fmt.Errorf("%w: ambiguous join path %s", ErrInvalidQuery, join.Path)
		}
		seenJoins[join.Path] = join
	}
	for alias, expression := range query.Annotations {
		if alias == "" || expression.SQL == "" {
			return fmt.Errorf("%w: invalid annotation %q", ErrInvalidQuery, alias)
		}
	}
	return nil
}

func (c Compiler) validateValues(meta models.Metadata, values map[string]any) error {
	for field := range values {
		if err := c.validateField(meta, field); err != nil {
			return err
		}
	}
	return nil
}

func (c Compiler) validateField(meta models.Metadata, field string) error {
	if len(meta.Fields) == 0 {
		return nil
	}
	if _, ok := c.fieldMap(meta)[field]; !ok {
		return fmt.Errorf("%w: unknown field %s", ErrInvalidQuery, field)
	}
	return nil
}

func (c Compiler) fieldList(meta models.Metadata, fields []string) ([]string, error) {
	result := make([]string, len(fields))
	for i, field := range fields {
		if err := c.validateField(meta, field); err != nil {
			return nil, err
		}
		result[i] = c.quoteField(meta, field)
	}
	return result, nil
}

func (c Compiler) compileOrdering(meta models.Metadata, ordering []string) (string, error) {
	parts := make([]string, len(ordering))
	for i, value := range ordering {
		if value == "" || value == "-" {
			return "", fmt.Errorf("%w: invalid ordering %q", ErrInvalidQuery, value)
		}
		direction := "ASC"
		field := value
		if strings.HasPrefix(value, "-") {
			direction = "DESC"
			field = strings.TrimPrefix(value, "-")
		}
		if err := c.validateField(meta, field); err != nil {
			return "", err
		}
		parts[i] = c.quoteField(meta, field) + " " + direction
	}
	return strings.Join(parts, ", "), nil
}

func (c Compiler) quoteField(meta models.Metadata, field string) string {
	return c.Dialect.QuoteIdent(c.fieldColumn(meta, field))
}

func (c Compiler) fieldColumn(meta models.Metadata, field string) string {
	if fieldMeta, ok := c.fieldMap(meta)[field]; ok && fieldMeta.Column != "" {
		return fieldMeta.Column
	}
	return field
}

func (c Compiler) fieldMap(meta models.Metadata) map[string]models.FieldMeta {
	fields := make(map[string]models.FieldMeta, len(meta.Fields))
	for _, field := range meta.Fields {
		fields[field.Name] = field
	}
	return fields
}

func (c Compiler) table(meta models.Metadata) string {
	name := meta.TableName
	if name == "" {
		name = meta.DBTable
	}
	if name == "" {
		name = strings.ToLower(meta.ModelName)
	}
	return c.Dialect.QuoteIdent(name)
}

func setOperationSQL(operation SetOperation) string {
	var keyword string
	switch operation.Type {
	case SetIntersection:
		keyword = "INTERSECT"
	case SetDifference:
		keyword = "EXCEPT"
	default:
		keyword = "UNION"
	}
	if operation.All {
		keyword += " ALL"
	}
	return keyword
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
