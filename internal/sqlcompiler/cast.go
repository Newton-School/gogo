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
	value, err := c.Expression(expression.Args[0])
	if err != nil {
		c.Args = c.Args[:argumentsBefore]
		return "", err
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
