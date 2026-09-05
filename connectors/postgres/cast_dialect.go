package postgres

import (
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

var _ db.CastDialect = Dialect{}

func (d Dialect) CastExpression(operand string, output models.Field) (string, error) {
	typeName, err := d.FieldType(output)
	if err != nil {
		return "", err
	}
	return "CAST(" + operand + " AS " + typeName + ")", nil
}
