package sqlcompiler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type fieldReference struct {
	field models.Field
	alias string
	path  []string
}

func (c *Compiler) resolveField(name string) (fieldReference, error) {
	if len(name) > 70*1024 {
		return fieldReference{}, errors.New("orm: field path exceeds length bound")
	}
	schema, alias := c.Schema, c.Alias
	if field, ok := schema.Field(name); ok {
		return fieldReference{field: field, alias: alias}, nil
	}
	// Resolve only joins already planned with their application scope. JSON
	// transforms must not invent an unscoped relational join.
	for at := strings.LastIndex(name, "__"); at >= 0; at = strings.LastIndex(name[:at], "__") {
		if join, ok := c.Joins[name[:at]]; ok {
			schema, alias, name = join.Schema, join.Alias, name[at+2:]
			break
		}
	}
	if field, ok := schema.Field(name); ok {
		return fieldReference{field: field, alias: alias}, nil
	}
	parts := strings.Split(name, "__")
	field, ok := schema.Field(parts[0])
	if !ok {
		return fieldReference{}, fmt.Errorf("orm: unknown or unresolved field %s", parts[0])
	}
	if len(parts) < 2 || field.Kind != models.JSON {
		return fieldReference{}, errors.New("orm: nested lookup requires a resolved relation or JSON field")
	}
	return fieldReference{field: field, alias: alias, path: parts[1:]}, nil
}

func (c *Compiler) referenceSQL(reference fieldReference, asText bool) (string, error) {
	column, err := c.Dialect.QuoteIdentifier(reference.field.DBColumn())
	if err != nil {
		return "", err
	}
	if reference.alias != "" {
		qualifier, err := c.Dialect.QuoteIdentifier(reference.alias)
		if err != nil {
			return "", err
		}
		column = qualifier + "." + column
	}
	if len(reference.path) == 0 {
		return column, nil
	}
	if reference.field.Codec != nil {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "JSON path extraction requires the standard JSON codec"}
	}
	return c.jsonExtract(column, reference.path, asText)
}

func (c *Compiler) jsonExtract(left string, path []string, asText bool) (string, error) {
	dialect, supported := c.Dialect.(db.JSONPathDialect)
	if !supported {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support JSON path extraction"}
	}
	if len(path) == 0 || len(path) > 64 {
		return "", errors.New("orm: JSON path requires between one and 64 components")
	}
	size := 0
	for _, component := range path {
		if _, err := jsonKey(component); err != nil {
			return "", err
		}
		size += len(component)
		if size > 64*1024 {
			return "", errors.New("orm: JSON path components exceed 64 KiB")
		}
	}
	var operand any = append([]string(nil), path...)
	kind := db.JSONPath
	if len(path) == 1 {
		kind, operand = db.JSONKey, path[0]
		if integer, err := strconv.ParseInt(path[0], 10, 32); err == nil {
			kind, operand = db.JSONIndex, int32(integer)
		} else if numericPathComponent(path[0]) {
			return "", errors.New("orm: JSON array index exceeds signed 32-bit range")
		}
	}
	return dialect.JSONExtract(left, c.bound(operand), kind, asText)
}

func numericPathComponent(component string) bool {
	if component != "" && (component[0] == '+' || component[0] == '-') {
		component = component[1:]
	}
	if component == "" {
		return false
	}
	for _, digit := range component {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (c *Compiler) jsonPathReference(expression db.Expression) (fieldReference, error) {
	if len(expression.Args) != 1 || expression.Args[0].Kind != "field" {
		return fieldReference{}, errors.New("orm: JSON path requires one schema-bound field expression")
	}
	reference, err := c.resolveField(expression.Args[0].Name)
	if err != nil {
		return fieldReference{}, err
	}
	path, ok := expression.Value.([]string)
	if !ok || reference.field.Kind != models.JSON {
		return fieldReference{}, errors.New("orm: JSON path requires a JSON field and string components")
	}
	reference.path = append(append([]string(nil), reference.path...), path...)
	if len(reference.path) == 0 {
		return fieldReference{}, errors.New("orm: JSON path requires a key or index")
	}
	return reference, nil
}

func jsonTextLookup(lookup string) bool {
	switch lookup {
	case "iexact", "icontains", "startswith", "istartswith", "endswith", "iendswith", "regex", "iregex":
		return true
	}
	return false
}

func (c *Compiler) predicateSQL(p db.Predicate, lookup string) (string, error) {
	if p.Expression == nil || p.Expression.Kind == "field" {
		name := p.Field
		if p.Expression != nil {
			name = p.Expression.Name
		}
		reference, err := c.resolveField(name)
		if err != nil {
			return "", err
		}
		return c.referenceSQL(reference, jsonTextLookup(lookup))
	}
	if p.Expression.Kind == "json_path" && jsonTextLookup(lookup) {
		reference, err := c.jsonPathReference(*p.Expression)
		if err != nil {
			return "", err
		}
		return c.referenceSQL(reference, true)
	}
	return c.Expression(*p.Expression)
}

// OutputField resolves the type of a schema-bound field or JSON transform for
// value decoding. It does not compile SQL or evaluate a connector/provider.
func OutputField(schema models.Schema, name string) (models.Field, error) {
	reference, err := (&Compiler{Schema: schema}).resolveField(name)
	if err != nil {
		return models.Field{}, err
	}
	if len(reference.path) > 0 {
		if reference.field.Codec != nil {
			return models.Field{}, &db.Error{Code: db.UnsupportedFeature, Message: "JSON path extraction requires the standard JSON codec"}
		}
		return models.JSONField(name, models.Nullable, models.Optional, models.ReadOnly), nil
	}
	return reference.field, nil
}
