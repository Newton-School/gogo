package sqlcompiler

import (
	"errors"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func (c *Compiler) validateJoinThrough(join db.Join) error {
	features, ok := c.Dialect.(db.FeatureDialect)
	if !ok || !features.SupportsFeature("grouped_relation_joins") {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support grouped relation joins"}
	}
	through := join.Through
	if _, err := c.Dialect.QuoteIdentifier(through.Alias); err != nil {
		return err
	}
	if err := through.Schema.Validate(); err != nil {
		return err
	}
	if through.SourceField == through.TargetField {
		return errors.New("orm: intermediary endpoints must be distinct")
	}
	parent := c.Schema
	if join.ParentPath != "" {
		parent = c.Joins[join.ParentPath].Schema
	}
	if err := validateBridgeEndpoint(through.Schema, through.SourceField, parent, join.ParentField); err != nil {
		return err
	}
	return validateBridgeEndpoint(through.Schema, through.TargetField, join.Schema, join.TargetField)
}

func validateBridgeEndpoint(bridge models.Schema, name string, endpoint models.Schema, key string) error {
	field, exists := bridge.Field(name)
	if !exists || (field.Kind != models.ForeignKey && field.Kind != models.OneToOne) || field.Relation == nil || field.Relation.Target != endpoint.Key() || field.Codec != nil {
		return errors.New("orm: intermediary endpoint requires a matching scalar foreign key")
	}
	names := field.Relation.TargetFields
	if len(names) == 0 {
		for _, pk := range endpoint.PKFields() {
			names = append(names, pk.Name)
		}
	}
	if len(names) != 1 || names[0] != key {
		return errors.New("orm: intermediary foreign key does not match the declared endpoint")
	}
	target, exists := endpoint.Field(key)
	if !exists || !target.IsStored() || target.Relation != nil || target.Codec != nil {
		return errors.New("orm: intermediary endpoint must reference a stored scalar key")
	}
	switch target.Kind {
	case models.SmallAuto, models.Auto, models.BigAuto, models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger,
		models.UUID, models.Char, models.Text, models.Slug, models.Email, models.URL, models.GenericIPAddress, models.Boolean, models.Decimal, models.Float, models.Date, models.DateTime, models.Time, models.Duration, models.Binary:
	default:
		return &db.Error{Code: db.UnsupportedFeature, Message: "Intermediary endpoint type is not a supported scalar key"}
	}
	primary := endpoint.PKFields()
	unique := target.Unique || len(primary) == 1 && primary[0].Name == key
	for _, constraint := range endpoint.Constraints {
		if strings.EqualFold(constraint.Kind, "unique") && len(constraint.Fields) == 1 && constraint.Fields[0] == key && constraint.Condition == "" {
			unique = true
		}
	}
	if !unique {
		return errors.New("orm: intermediary endpoint requires an unconditionally unique scalar key")
	}
	return nil
}

// Filtering the target inside the group prevents invisible links from changing
// parent counts and sums. Two independent LEFT JOINs would retain those links.
func (c *Compiler) joinThroughSQL(join db.Join, parent, targetKey, target, alias string) (string, error) {
	through := join.Through
	table, err := c.Dialect.QuoteIdentifier(through.Schema.DBTable())
	if err != nil {
		return "", err
	}
	bridgeAlias, _ := c.Dialect.QuoteIdentifier(through.Alias)
	bridge := Compiler{Dialect: c.Dialect, Schema: through.Schema, Alias: through.Alias}
	source, err := bridge.field(through.SourceField)
	if err != nil {
		return "", err
	}
	destination, err := bridge.field(through.TargetField)
	if err != nil {
		return "", err
	}
	filter := Compiler{Dialect: c.Dialect, Schema: join.Schema, Alias: join.Alias, Args: c.Args}
	scope, err := filter.Predicate(join.Where)
	if err != nil {
		return "", err
	}
	result := "(" + table + " AS " + bridgeAlias + " INNER JOIN " + target + " AS " + alias + " ON " + destination + "=" + targetKey
	if scope != "" {
		result += " AND (" + scope + ")"
	}
	result += ") ON " + parent + "=" + source
	bridge.Args = filter.Args
	scope, err = bridge.Predicate(through.Where)
	if err != nil {
		return "", err
	}
	if scope != "" {
		result += " AND (" + scope + ")"
	}
	c.Args = bridge.Args
	return result, nil
}
