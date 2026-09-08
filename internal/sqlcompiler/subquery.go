package sqlcompiler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func unsupportedSubquery(message string) error {
	return &db.Error{Code: db.UnsupportedFeature, Message: message}
}

func IsSubqueryExpression(e db.Expression) bool {
	return e.Subquery != nil || e.Kind == "subquery" || e.Kind == "exists" || e.Kind == "orm_subquery" || e.Kind == "outer_ref"
}

// SubqueryOutput derives an intrinsic value type, without model validation,
// defaults, identity behavior or codecs. Scalar SELECT absence is always NULL.
func SubqueryOutput(schema models.Schema, name string) (models.Field, error) {
	f, ok := schema.Field(name)
	if !ok || !models.ValidIdentifier(name) || strings.Contains(name, "__") || f.Codec != nil || f.Relation != nil || f.Element != nil || f.GeneratedExpression != "" {
		return models.Field{}, unsupportedSubquery("Subqueries require a local intrinsic scalar field")
	}
	kind := f.Kind
	switch kind {
	case models.SmallAuto:
		kind = models.SmallInteger
	case models.Auto:
		kind = models.Integer
	case models.BigAuto:
		kind = models.BigInteger
	case models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger,
		models.UUID, models.Decimal, models.Float, models.Boolean, models.Char, models.Text, models.Slug, models.Email, models.URL,
		models.GenericIPAddress, models.Date, models.DateTime, models.Time, models.Duration, models.Binary, models.JSON:
	default:
		return models.Field{}, unsupportedSubquery("Subqueries require a local intrinsic scalar field")
	}
	return models.Field{Kind: kind, Null: true, Blank: true, MaxLength: f.MaxLength, MaxDigits: f.MaxDigits, DecimalPlaces: f.DecimalPlaces}, nil
}

// CheckSubqueryOutput guards only a direct subquery annotation. CASE, Cast and
// other enclosing expressions keep their own explicit conversion/output rules.
func CheckSubqueryOutput(expression db.Expression, output models.Field) error {
	if expression.Kind != "subquery" && expression.Kind != "exists" && expression.Kind != "orm_subquery" {
		return nil
	}
	if expression.Output == nil {
		return errors.New("orm: subquery output metadata is missing")
	}
	want := expression.Output
	if output.Codec != nil || output.Kind != want.Kind || output.Null != want.Null || output.MaxDigits != want.MaxDigits || output.DecimalPlaces != want.DecimalPlaces {
		return errors.New("orm: direct subquery output must preserve its intrinsic type and nullability; use Cast for conversion")
	}
	return nil
}

type outerQuery struct {
	schema models.Schema
	alias  string
}

func (c *Compiler) validateSubqueries(query *db.Select) (bool, error) {
	found, window, aggregate, count := false, false, false, 0
	err := inspectQueryExpressions(*query, func(e *db.Expression, inner, parameter bool) error {
		if parameter {
			if IsSubqueryExpression(*e) {
				return unsupportedSubquery("Query expressions cannot be bound as parameter data")
			}
			// Ordinary expression-shaped JSON remains data, not a SQL owner.
			// Its children are still scanned under the shared expression budget.
			return nil
		}
		if e.Kind == "invalid_tree" {
			return errors.New("orm: invalid expression tree")
		}
		if e.Kind == "orm_subquery" {
			return unsupportedSubquery("ORM subqueries require terminal scope preparation")
		}
		window = window || e.Kind == "window" || e.Window != nil
		aggregate = aggregate || e.Kind == "function" && IsAggregateFunction(e.Name)
		if e.Kind == "outer_ref" {
			if !inner || e.Value != nil || len(e.Args) != 0 || e.Subquery != nil || e.Output != nil {
				return unsupportedSubquery("OuterRef requires one immediate enclosing SELECT")
			}
			_, err := SubqueryOutput(c.Schema, e.Name)
			return err
		}
		if e.Subquery != nil && e.Kind != "subquery" && e.Kind != "exists" {
			return errors.New("orm: subquery metadata requires a query expression")
		}
		if e.Kind != "subquery" && e.Kind != "exists" {
			return nil
		}
		found = true
		count++
		if count > 64 {
			return unsupportedSubquery("At most 64 subquery occurrences are supported")
		}
		if inner || e.Subquery == nil || e.Value != nil || len(e.Args) != 0 || e.Filter != nil || e.Window != nil || e.Distinct || len(e.Branches) != 0 {
			return unsupportedSubquery("Subqueries require a resolved, nonnested scalar SELECT")
		}
		features, ok := c.Dialect.(db.FeatureDialect)
		if !ok || !features.SupportsFeature("correlated_subqueries") {
			return unsupportedSubquery("Selected dialect does not support correlated subqueries")
		}
		s := e.Subquery
		q := s.Query
		if err := s.Schema.Validate(); err != nil {
			return err
		}
		if q.Table != s.Schema.DBTable() || q.Alias != "" || len(q.Joins) != 0 || len(q.Aliases) != 0 || len(q.Projections) != 0 || len(q.GroupBy) != 0 || q.ForUpdate || q.NoWait || q.SkipLocked || q.NoKey || len(q.LockOf) != 0 || q.Distinct || len(q.DistinctOn) != 0 || !emptySubqueryPredicate(q.Having) {
			return unsupportedSubquery("Inner SELECT supports only local filters, ordering and slicing")
		}
		if q.Limit != nil && *q.Limit < 0 || q.Offset != nil && *q.Offset < 0 {
			return errors.New("orm: negative subquery slice")
		}
		if e.Kind == "subquery" {
			if q.Limit == nil || *q.Limit != 1 || len(q.Fields) != 1 || q.Fields[0] != s.Field {
				return unsupportedSubquery("Scalar Subquery requires one explicit field and Limit(1)")
			}
			output, err := SubqueryOutput(s.Schema, s.Field)
			if err != nil {
				return err
			}
			if e.Output == nil || !sameSubqueryValueType(*e.Output, output) {
				return errors.New("orm: scalar subquery output differs from its selected field")
			}
		} else if s.Field != "" || len(q.Fields) != 0 || len(q.Order) != 0 || e.Output == nil || !sameSubqueryValueType(*e.Output, models.Field{Kind: models.Boolean}) {
			return errors.New("orm: EXISTS requires a boolean output and no projection or ordering")
		}
		for _, order := range q.Order {
			if _, err := SubqueryOutput(s.Schema, order.Field); err != nil {
				return err
			}
			if order.NullsFirst && order.NullsLast {
				return errors.New("orm: conflicting subquery null ordering")
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if found && (len(query.GroupBy) != 0 || query.ForUpdate || window || aggregate) {
		return false, unsupportedSubquery("Subqueries require ungrouped, unlocked SELECTs without window expressions")
	}
	if found {
		for _, join := range query.Joins {
			if join.ParentPath == "" {
				if _, stored := c.Schema.Field(join.ParentField); !stored {
					return false, unsupportedSubquery("Subquery aliases cannot define JOIN endpoints")
				}
			}
		}
	}
	for _, alias := range query.Aliases {
		if alias.Output != nil {
			if err := CheckSubqueryOutput(alias.Expression, *alias.Output); err != nil {
				return false, err
			}
		}
	}
	for _, projection := range query.Projections {
		if projection.Output != nil {
			if err := CheckSubqueryOutput(projection.Expression, *projection.Output); err != nil {
				return false, err
			}
		}
	}
	return found, nil
}

func sameSubqueryValueType(a, b models.Field) bool {
	return a.Kind == b.Kind && a.Null == b.Null && a.MaxDigits == b.MaxDigits && a.DecimalPlaces == b.DecimalPlaces && a.Codec == nil && a.Relation == nil && a.Element == nil
}
func emptySubqueryPredicate(p db.Predicate) bool {
	return p.Field == "" && p.Expression == nil && len(p.Children) == 0 && p.Value == nil && !p.Negated && p.Connector == "" && p.Lookup == ""
}

func (c *Compiler) subquery(expression db.Expression) (result string, err error) {
	if !c.readExpressions || c.outer != nil || expression.Subquery == nil {
		return "", unsupportedSubquery("Subqueries are supported only inside a prepared read SELECT")
	}
	c.subqueryOccurrences++
	if c.subqueryOccurrences > 64 {
		return "", unsupportedSubquery("At most 64 compiled subquery occurrences are supported")
	}
	s := expression.Subquery
	previousSchema, previousAlias, previousJoins, previousAliases := c.Schema, c.Alias, c.Joins, c.Aliases
	previousArgs := len(c.Args)
	c.subquerySerial++
	alias := fmt.Sprintf("gogo_subquery_%d", c.subquerySerial)
	for c.usedAliases[alias] {
		c.subquerySerial++
		alias = fmt.Sprintf("gogo_subquery_%d", c.subquerySerial)
	}
	c.usedAliases[alias] = true
	c.outer = &outerQuery{previousSchema, previousAlias}
	c.Schema, c.Alias, c.Joins, c.Aliases = s.Schema, alias, nil, nil
	defer func() {
		c.Schema, c.Alias, c.Joins, c.Aliases = previousSchema, previousAlias, previousJoins, previousAliases
		c.outer = nil
		if err != nil {
			c.Args = c.Args[:previousArgs]
		}
	}()
	table, err := c.Dialect.QuoteIdentifier(s.Query.Table)
	if err != nil {
		return "", err
	}
	quotedAlias, err := c.Dialect.QuoteIdentifier(alias)
	if err != nil {
		return "", err
	}
	field := "1"
	if expression.Kind == "subquery" {
		field, err = c.field(s.Field)
		if err != nil {
			return "", err
		}
	}
	result = "SELECT " + field + " FROM " + table + " AS " + quotedAlias
	where, err := c.Predicate(s.Query.Where)
	if err != nil {
		return "", err
	}
	if where != "" {
		result += " WHERE " + where
	}
	orders := make([]string, 0, len(s.Query.Order))
	for _, order := range s.Query.Order {
		value, err := c.field(order.Field)
		if err != nil {
			return "", err
		}
		if order.Desc {
			value += " DESC"
		} else {
			value += " ASC"
		}
		if order.NullsFirst {
			value += " NULLS FIRST"
		}
		if order.NullsLast {
			value += " NULLS LAST"
		}
		orders = append(orders, value)
	}
	if len(orders) != 0 {
		result += " ORDER BY " + strings.Join(orders, ", ")
	}
	if s.Query.Limit != nil {
		result += " LIMIT " + c.bound(*s.Query.Limit)
	}
	if s.Query.Offset != nil {
		result += " OFFSET " + c.bound(*s.Query.Offset)
	}
	if expression.Kind == "exists" {
		return "EXISTS (" + result + ")", nil
	}
	return "(" + result + ")", nil
}

func (c *Compiler) outerReference(expression db.Expression) (string, error) {
	if !c.readExpressions || c.outer == nil {
		return "", unsupportedSubquery("OuterRef requires an immediate outer SELECT")
	}
	if _, err := SubqueryOutput(c.outer.schema, expression.Name); err != nil {
		return "", err
	}
	field, _ := c.outer.schema.Field(expression.Name)
	return c.referenceSQL(fieldReference{field: field, alias: c.outer.alias}, false)
}
