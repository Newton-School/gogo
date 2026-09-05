package sqlcompiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func (c *Compiler) predicateField(p db.Predicate) (models.Field, bool) {
	name := p.Field
	if p.Expression != nil {
		if p.Expression.Kind == "json_path" {
			reference, err := c.jsonPathReference(*p.Expression)
			return reference.field, err == nil
		}
		name = ""
		if p.Expression.Kind == "field" {
			name = p.Expression.Name
		}
	}
	metadata, _, err := c.modelField(name)
	return metadata, err == nil
}

func jsonLookupValue(metadata models.Field, value any) (any, error) {
	if value == nil {
		value = models.JSONNull
	}
	if metadata.Codec != nil {
		return metadata.Codec.Encode(value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("orm: invalid JSON lookup value: %w", err)
	}
	return string(encoded), nil
}

func isJSONLookup(operation string) bool {
	switch operation {
	case "contains", "contained_by", "has_key", "has_keys", "has_any_keys":
		return true
	}
	return false
}

func (c *Compiler) jsonLookup(metadata models.Field, p db.Predicate, left string) (string, error) {
	dialect, supported := c.Dialect.(db.JSONLookupDialect)
	if !supported {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support JSON containment or key lookups"}
	}
	var right string
	if expression, ok := p.Value.(db.Expression); ok {
		var err error
		right, err = c.Expression(expression)
		if err != nil {
			return "", err
		}
	} else {
		var value any
		var err error
		switch p.Lookup {
		case "contains", "contained_by":
			if p.Value == nil {
				return "", errors.New("orm: JSON containment requires a JSON value; use JSONNull for a null scalar")
			}
			value, err = jsonLookupValue(metadata, p.Value)
		case "has_key":
			value, err = jsonKey(p.Value)
		case "has_keys", "has_any_keys":
			values := reflect.ValueOf(p.Value)
			if !values.IsValid() || (values.Kind() != reflect.Slice && values.Kind() != reflect.Array) || values.Len() > 1024 {
				return "", errors.New("orm: JSON key lookup requires at most 1024 string keys")
			}
			keys := make([]string, values.Len())
			for i := range keys {
				keys[i], err = jsonKey(values.Index(i).Interface())
				if err != nil {
					return "", err
				}
			}
			value = keys
		}
		if err != nil {
			return "", err
		}
		right = c.bound(value)
	}
	return dialect.JSONLookup(p.Lookup, left, right)
}

func jsonKey(value any) (string, error) {
	key, ok := value.(string)
	if !ok || len(key) > 64*1024 || strings.ContainsRune(key, 0) || !utf8.ValidString(key) {
		return "", errors.New("orm: JSON keys require valid UTF-8 strings without NUL, up to 64 KiB")
	}
	return key, nil
}
