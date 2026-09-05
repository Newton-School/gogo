package sqlcompiler

import (
	"errors"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func (c *Compiler) cast(expression db.Expression) (string, error) {
	if len(expression.Args) != 1 || expression.Output == nil || expression.Distinct {
		return "", errors.New("orm: cast requires one expression and a declared output field")
	}
	if err := castField(expression.Output, 0); err != nil {
		return "", err
	}
	dialect, supported := c.Dialect.(db.CastDialect)
	if !supported {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support cast expressions"}
	}
	argumentsBefore := len(c.Args)
	argument := expression.Args[0]
	value, err := c.Expression(argument)
	if err != nil {
		c.Args = c.Args[:argumentsBefore]
		return "", err
	}
	// PostgreSQL and other typed drivers bind a parameter using the server's
	// inferred input type. Preserve a native literal's source type before its
	// requested conversion (e.g. an integer cannot be bound directly as text).
	if argument.Kind == "value" {
		if source, known := castLiteralType(argument.Value); known && source.Kind != expression.Output.Kind {
			value, err = dialect.CastExpression(value, source)
			if err != nil {
				c.Args = c.Args[:argumentsBefore]
				return "", err
			}
		}
	}
	result, err := dialect.CastExpression(value, *expression.Output)
	if err == nil && strings.TrimSpace(result) == "" {
		err = errors.New("orm: dialect returned an empty cast expression")
	}
	if err != nil {
		c.Args = c.Args[:argumentsBefore]
		return "", err
	}
	return result, nil
}

func castLiteralType(value any) (models.Field, bool) {
	kind := models.Kind("")
	switch value.(type) {
	case string:
		kind = models.Text
	case bool:
		kind = models.Boolean
	case int, int8, int16, int32, int64, uint8, uint16, uint32:
		kind = models.BigInteger
	case float32, float64:
		kind = models.Float
	case []byte:
		kind = models.Binary
	}
	return models.Field{Kind: kind}, kind != ""
}

func castField(field *models.Field, depth int) error {
	if depth > 32 || field == nil {
		return errors.New("orm: cast output metadata exceeds the nesting bound")
	}
	if field.IsAuto() || field.Kind == models.Generated || field.Relation != nil || field.Kind == models.ForeignKey || field.Kind == models.OneToOne || !field.IsStored() || field.GeneratedExpression != "" {
		return errors.New("orm: cast output must be a stored value type without identity, generated or relation behavior")
	}
	if field.Element != nil {
		return castField(field.Element, depth+1)
	}
	return nil
}
