package sqlcompiler

import (
	"errors"
	"strings"

	"github.com/Newton-School/gogo/core/db"
)

func (c *Compiler) conditional(expression db.Expression) (result string, err error) {
	if expression.Output == nil || len(expression.Args) != 1 || expression.Distinct || len(expression.Branches) > 128 {
		return "", errors.New("orm: case requires an output field, one fallback and at most 128 branches")
	}
	if err := castField(expression.Output, 0); err != nil {
		return "", err
	}
	if len(expression.Branches) == 0 {
		return c.cast(db.Expression{Kind: "cast", Output: expression.Output, Args: expression.Args})
	}
	features, advertised := c.Dialect.(db.FeatureDialect)
	dialect, casts := c.Dialect.(db.CastDialect)
	if !advertised || !features.SupportsFeature("conditional_expressions") || !casts {
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Selected dialect does not support typed conditional expressions"}
	}
	prior := len(c.Args)
	defer func() {
		if err != nil {
			c.Args = c.Args[:prior]
		}
	}()
	parts := []string{"CASE"}
	for _, branch := range expression.Branches {
		condition, err := c.Predicate(branch.Condition)
		if err != nil {
			return "", err
		}
		if condition == "" {
			return "", errors.New("orm: case branch requires an explicit predicate")
		}
		value, err := c.cast(db.Expression{Kind: "cast", Output: expression.Output, Args: []db.Expression{branch.Then}})
		if err != nil {
			return "", err
		}
		parts = append(parts, "WHEN "+condition+" THEN "+value)
	}
	fallback, err := c.cast(db.Expression{Kind: "cast", Output: expression.Output, Args: expression.Args})
	if err != nil {
		return "", err
	}
	parts = append(parts, "ELSE "+fallback, "END")
	result, err = dialect.CastExpression(strings.Join(parts, " "), *expression.Output)
	if err == nil && strings.TrimSpace(result) == "" {
		err = errors.New("orm: dialect returned an empty conditional expression")
	}
	return result, err
}
