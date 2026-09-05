package orm

import (
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// Cast converts a database expression to a declared field type. Conversion,
// rounding and overflow follow the selected backend; model validators do not
// run inside SQL. Typed aggregate/result outputs validate the decoded result.
func Cast(expression db.Expression, output models.Field) db.Expression {
	return cloneExpression(db.Expression{Kind: "cast", Args: []db.Expression{expression}, Output: &output})
}
