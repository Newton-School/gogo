package sqlcompiler

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type Compiler struct {
	Dialect db.Dialect
	Schema  models.Schema
	Args    []any
	Alias   string
	Joins   map[string]db.Join
}

func (c *Compiler) bound(value any) string {
	c.Args = append(c.Args, value)
	return c.Dialect.Placeholder(len(c.Args))
}
func (c *Compiler) field(name string) (string, error) {
	if name == "*" {
		return "*", nil
	}
	reference, err := c.resolveField(name)
	if err != nil {
		return "", err
	}
	return c.referenceSQL(reference, false)
}

func (c *Compiler) modelField(name string) (models.Field, string, error) {
	reference, err := c.resolveField(name)
	return reference.field, reference.alias, err
}
func (c *Compiler) Predicate(p db.Predicate) (string, error) {
	if len(p.Children) > 0 {
		op := p.Connector
		if op == "" {
			op = "AND"
		}
		if op != "AND" && op != "OR" && op != "XOR" {
			return "", errors.New("orm: invalid boolean connector")
		}
		parts := []string{}
		for _, child := range p.Children {
			part, err := c.Predicate(child)
			if err != nil {
				return "", err
			}
			if part != "" {
				parts = append(parts, "("+part+")")
			}
		}
		if len(parts) == 0 {
			return "", nil
		}
		value := strings.Join(parts, " "+op+" ")
		if op == "XOR" {
			if len(parts) != 2 {
				return "", errors.New("orm: XOR needs two operands")
			}
			value = "(" + parts[0] + ") <> (" + parts[1] + ")"
		}
		if p.Negated {
			return "NOT (" + value + ")", nil
		}
		return value, nil
	}
	if p.Field == "" && p.Expression == nil {
		return "", nil
	}
	lookup := p.Lookup
	if lookup == "" {
		lookup = "exact"
	}
	argumentsBeforeField := len(c.Args)
	field, err := c.predicateSQL(p, lookup)
	if err != nil {
		return "", err
	}
	metadata, hasMetadata := c.predicateField(p)
	if hasMetadata && metadata.Kind == models.JSON && isJSONLookup(lookup) {
		p.Lookup = lookup
		result, err := c.jsonLookup(metadata, p, field)
		if err != nil {
			return "", err
		}
		if p.Negated {
			return "NOT (" + result + ")", nil
		}
		return result, nil
	}
	// JSON equality encodes Go values as JSON literals, including a nil RHS as
	// JSON null. SQL NULL remains an explicit isnull lookup. In particular the
	// Go string "null" must compare with a JSON string, not a JSON null scalar.
	// Value expressions retain their explicitly declared SQL semantics.
	if lookup == "exact" || lookup == "gt" || lookup == "gte" || lookup == "lt" || lookup == "lte" {
		if hasMetadata && metadata.Kind == models.JSON {
			if p.Value == nil && lookup != "exact" {
				return "", errors.New("orm: NULL requires exact/isnull")
			}
			if _, expression := p.Value.(db.Expression); !expression {
				p.Value, err = jsonLookupValue(metadata, p.Value)
				if err != nil {
					return "", err
				}
			}
		}
	}
	var result string
	switch lookup {
	case "exact", "iexact", "gt", "gte", "lt", "lte":
		operators := map[string]string{"exact": "=", "iexact": "=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}
		if p.Value == nil {
			if lookup != "exact" {
				return "", errors.New("orm: NULL requires exact/isnull")
			}
			result = field + " IS NULL"
		} else if expr, ok := p.Value.(db.Expression); ok {
			rhs, err := c.Expression(expr)
			if err != nil {
				return "", err
			}
			result = field + " " + operators[lookup] + " " + rhs
		} else {
			param := c.bound(p.Value)
			if lookup == "iexact" {
				field = "LOWER(" + field + ")"
				param = "LOWER(" + param + ")"
			}
			result = field + " " + operators[lookup] + " " + param
		}
	case "isnull":
		isNull, ok := p.Value.(bool)
		if !ok {
			return "", errors.New("orm: isnull requires bool")
		}
		result = field + " IS "
		if !isNull {
			result += "NOT "
		}
		result += "NULL"
	case "in", "range":
		v := reflect.ValueOf(p.Value)
		if !v.IsValid() || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) {
			return "", errors.New("orm: in/range requires slice")
		}
		if lookup == "range" && v.Len() != 2 {
			return "", errors.New("orm: range requires two bounds")
		}
		items := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			value := v.Index(i).Interface()
			if lookup == "in" && value == nil && hasMetadata && metadata.Kind == models.JSON {
				// A direct nil member represents SQL NULL, not JSON null. Like
				// Django's IN lookup, it contributes no equality candidates.
				continue
			}
			if expression, ok := value.(db.Expression); ok {
				item, err := c.Expression(expression)
				if err != nil {
					return "", err
				}
				items = append(items, item)
				continue
			}
			if hasMetadata && metadata.Kind == models.JSON {
				value, err = jsonLookupValue(metadata, value)
				if err != nil {
					return "", err
				}
			}
			items = append(items, c.bound(value))
		}
		if len(items) == 0 {
			// A JSON extraction or other expression may have bound operands
			// while compiling the now-unused left side. Remove only those
			// operands; leaving placeholder holes makes PostgreSQL reject SQL.
			c.Args = c.Args[:argumentsBeforeField]
			result = "FALSE"
		} else {
			if lookup == "range" {
				result = field + " BETWEEN " + items[0] + " AND " + items[1]
			} else {
				result = field + " IN (" + strings.Join(items, ", ") + ")"
			}
		}
	case "contains", "icontains", "startswith", "istartswith", "endswith", "iendswith":
		text, ok := p.Value.(string)
		if !ok {
			return "", errors.New("orm: text lookup requires string")
		}
		text = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text)
		if strings.Contains(lookup, "contains") || strings.Contains(lookup, "endswith") {
			text = "%" + text
		}
		if strings.Contains(lookup, "contains") || strings.Contains(lookup, "startswith") {
			text += "%"
		}
		rhs := c.bound(text)
		if strings.HasPrefix(lookup, "i") {
			field = "LOWER(" + field + ")"
			rhs = "LOWER(" + rhs + ")"
		}
		result = field + " LIKE " + rhs + ` ESCAPE '\'`
	case "regex", "iregex":
		if c.Dialect.Name() != "postgres" {
			return "", &db.Error{Code: db.UnsupportedFeature, Message: "Regex not supported by selected dialect"}
		}
		op := " ~ "
		if lookup == "iregex" {
			op = " ~* "
		}
		result = field + op + c.bound(p.Value)
	default:
		return "", fmt.Errorf("orm: unsupported lookup %s", lookup)
	}
	if p.Negated {
		return "NOT (" + result + ")", nil
	}
	return result, nil
}
func (c *Compiler) Expression(e db.Expression) (string, error) {
	if e.Filter != nil && (e.Kind != "function" || !IsAggregateFunction(e.Name)) {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "FILTER requires an aggregate expression"}
	}
	switch e.Kind {
	case "cast":
		return c.cast(e)
	case "json_path", "json_text_path":
		reference, err := c.jsonPathReference(e)
		if err != nil {
			return "", err
		}
		return c.referenceSQL(reference, e.Kind == "json_text_path")
	case "field":
		return c.field(e.Name)
	case "value":
		return c.bound(e.Value), nil
	case "binary":
		if len(e.Args) != 2 {
			return "", errors.New("orm: binary expression needs two arguments")
		}
		if e.Name != "+" && e.Name != "-" && e.Name != "*" && e.Name != "/" && e.Name != "%" {
			return "", errors.New("orm: invalid expression operator")
		}
		a, err := c.Expression(e.Args[0])
		if err != nil {
			return "", err
		}
		b, err := c.Expression(e.Args[1])
		if err != nil {
			return "", err
		}
		return "(" + a + " " + e.Name + " " + b + ")", nil
	case "function":
		name := strings.ToUpper(e.Name)
		if !functions[name] {
			return "", fmt.Errorf("orm: unsupported function %s", name)
		}
		args := make([]string, len(e.Args))
		for i, arg := range e.Args {
			value, err := c.Expression(arg)
			if err != nil {
				return "", err
			}
			args[i] = value
		}
		prefix := ""
		if e.Distinct {
			prefix = "DISTINCT "
		}
		result := name + "(" + prefix + strings.Join(args, ", ") + ")"
		if e.Filter != nil {
			features, advertised := c.Dialect.(db.FeatureDialect)
			if !IsAggregateFunction(name) || !advertised || !features.SupportsFeature("filtered_aggregates") {
				return "", &db.Error{Code: db.UnsupportedFeature, Message: "Filtered aggregate is not supported by this expression or dialect"}
			}
			filter, err := c.Predicate(*e.Filter)
			if err != nil {
				return "", err
			}
			if filter != "" {
				result += " FILTER (WHERE " + filter + ")"
			}
		}
		return result, nil
	}
	return "", errors.New("orm: unknown expression kind")
}

var functions = map[string]bool{}

func IsAggregateFunction(name string) bool {
	switch strings.ToUpper(name) {
	case "AVG", "COUNT", "MIN", "MAX", "SUM", "STDDEV", "STDDEV_POP", "STDDEV_SAMP", "VARIANCE", "VAR_POP", "VAR_SAMP":
		return true
	}
	return false
}

func init() {
	for _, name := range strings.Fields("AVG COUNT MIN MAX SUM STDDEV VARIANCE ABS ACOS ASIN ATAN ATAN2 CEIL COS COT DEGREES EXP FLOOR LN LOG MOD PI POWER RADIANS RANDOM ROUND SIGN SIN SQRT TAN CHR CONCAT LEFT LENGTH LOWER LPAD LTRIM MD5 ASCII REPEAT REPLACE REVERSE RIGHT RPAD RTRIM STRPOS SUBSTR TRIM UPPER COALESCE GREATEST LEAST NULLIF JSON_BUILD_OBJECT JSON_BUILD_ARRAY NOW ROW_NUMBER RANK DENSE_RANK PERCENT_RANK CUME_DIST NTILE LAG LEAD FIRST_VALUE LAST_VALUE NTH_VALUE") {
		functions[name] = true
	}
	for _, name := range []string{"STDDEV_POP", "STDDEV_SAMP", "VAR_POP", "VAR_SAMP"} {
		functions[name] = true
	}
}
func Select(dialect db.Dialect, schema models.Schema, query db.Select) (string, []any, error) {
	c := &Compiler{Dialect: dialect, Schema: schema, Alias: query.Alias, Joins: map[string]db.Join{}}
	table, err := dialect.QuoteIdentifier(query.Table)
	if err != nil {
		return "", nil, err
	}
	if query.Alias != "" {
		alias, err := dialect.QuoteIdentifier(query.Alias)
		if err != nil {
			return "", nil, err
		}
		table += " AS " + alias
	}
	aliases := map[string]bool{query.Alias: true}
	for _, join := range query.Joins {
		if query.Alias == "" || join.Path == "" || aliases[join.Alias] {
			return "", nil, errors.New("orm: joins require a root alias and distinct relation aliases")
		}
		if _, exists := c.Joins[join.Path]; exists {
			return "", nil, errors.New("orm: duplicate join path")
		}
		if join.ParentPath != "" {
			if _, ok := c.Joins[join.ParentPath]; !ok {
				return "", nil, errors.New("orm: join parent must precede child")
			}
		}
		if _, err := dialect.QuoteIdentifier(join.Alias); err != nil {
			return "", nil, err
		}
		if err := join.Schema.Validate(); err != nil {
			return "", nil, err
		}
		c.Joins[join.Path] = join
		aliases[join.Alias] = true
	}
	fields := []string{}
	for _, name := range query.Fields {
		field, err := c.field(name)
		if err != nil {
			return "", nil, err
		}
		fields = append(fields, field)
	}
	for _, projection := range query.Projections {
		expression, err := c.Expression(projection.Expression)
		if err != nil {
			return "", nil, err
		}
		alias, err := dialect.QuoteIdentifier(projection.Alias)
		if err != nil {
			return "", nil, err
		}
		fields = append(fields, expression+" AS "+alias)
	}
	if len(fields) == 0 {
		return "", nil, errors.New("orm: empty select projection")
	}
	prefix := "SELECT "
	if query.Distinct {
		prefix += "DISTINCT "
	}
	if len(query.DistinctOn) > 0 {
		if dialect.Name() != "postgres" {
			return "", nil, &db.Error{Code: db.UnsupportedFeature, Message: "DISTINCT ON unsupported"}
		}
		names := []string{}
		for _, name := range query.DistinctOn {
			field, err := c.field(name)
			if err != nil {
				return "", nil, err
			}
			names = append(names, field)
		}
		prefix += "DISTINCT ON (" + strings.Join(names, ", ") + ") "
	}
	sql := prefix + strings.Join(fields, ", ") + " FROM " + table
	for _, join := range query.Joins {
		parentName := join.ParentField
		if join.ParentPath != "" {
			parentName = join.ParentPath + "__" + parentName
		}
		left, err := c.field(parentName)
		if err != nil {
			return "", nil, err
		}
		right, err := c.field(join.Path + "__" + join.TargetField)
		if err != nil {
			return "", nil, err
		}
		target, err := dialect.QuoteIdentifier(join.Schema.DBTable())
		if err != nil {
			return "", nil, err
		}
		alias, _ := dialect.QuoteIdentifier(join.Alias)
		kind := " LEFT JOIN "
		if join.Inner {
			kind = " INNER JOIN "
		}
		sql += kind + target + " AS " + alias + " ON " + left + "=" + right
		filter := Compiler{Dialect: dialect, Schema: join.Schema, Alias: join.Alias, Args: c.Args}
		scope, err := filter.Predicate(join.Where)
		if err != nil {
			return "", nil, err
		}
		c.Args = filter.Args
		if scope != "" {
			sql += " AND (" + scope + ")"
		}
	}
	where, err := c.Predicate(query.Where)
	if err != nil {
		return "", nil, err
	}
	if where != "" {
		sql += " WHERE " + where
	}
	if len(query.GroupBy) > 0 {
		names := []string{}
		for _, name := range query.GroupBy {
			field, err := c.field(name)
			if err != nil {
				return "", nil, err
			}
			names = append(names, field)
		}
		sql += " GROUP BY " + strings.Join(names, ", ")
	}
	having, err := c.Predicate(query.Having)
	if err != nil {
		return "", nil, err
	}
	if having != "" {
		sql += " HAVING " + having
	}
	if len(query.Order) > 0 {
		orders := []string{}
		for _, order := range query.Order {
			field, err := c.field(order.Field)
			if err != nil {
				return "", nil, err
			}
			if order.Desc {
				field += " DESC"
			} else {
				field += " ASC"
			}
			if order.NullsFirst {
				field += " NULLS FIRST"
			}
			if order.NullsLast {
				field += " NULLS LAST"
			}
			orders = append(orders, field)
		}
		sql += " ORDER BY " + strings.Join(orders, ", ")
	}
	if query.Limit != nil {
		if *query.Limit < 0 {
			return "", nil, errors.New("orm: negative limit")
		}
		sql += " LIMIT " + c.bound(*query.Limit)
	}
	if query.Offset != nil {
		if *query.Offset < 0 {
			return "", nil, errors.New("orm: negative offset")
		}
		sql += " OFFSET " + c.bound(*query.Offset)
	}
	if query.ForUpdate {
		if query.NoWait && query.SkipLocked {
			return "", nil, errors.New("orm: NOWAIT and SKIP LOCKED are incompatible")
		}
		if query.NoKey {
			sql += " FOR NO KEY UPDATE"
		} else {
			sql += " FOR UPDATE"
		}
		if len(query.LockOf) > 0 {
			aliases := []string{}
			for _, path := range query.LockOf {
				alias := query.Alias
				if path == "self" {
					if alias == "" {
						alias = query.Table
					}
				} else {
					join, ok := c.Joins[path]
					if !ok {
						return "", nil, errors.New("orm: lock OF path is not selected")
					}
					alias = join.Alias
				}
				quoted, err := dialect.QuoteIdentifier(alias)
				if err != nil {
					return "", nil, err
				}
				aliases = append(aliases, quoted)
			}
			sql += " OF " + strings.Join(aliases, ", ")
		}
		if query.NoWait {
			sql += " NOWAIT"
		}
		if query.SkipLocked {
			sql += " SKIP LOCKED"
		}
	}
	return sql, c.Args, nil
}
