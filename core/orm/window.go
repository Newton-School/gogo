package orm

import "github.com/Newton-School/gogo/core/db"

// Window evaluates a function over scoped rows without grouping model rows.
// Declare its decoded output with Typed when adding an annotation. Partition,
// order, frame and function arguments are snapshotted without provider calls.
func Window(function db.Expression, specification db.WindowSpec) db.Expression {
	return cloneExpression(db.Expression{Kind: "window", Args: []db.Expression{function}, Window: &specification})
}

func RowNumber() db.Expression                     { return Func("ROW_NUMBER") }
func Rank() db.Expression                          { return Func("RANK") }
func DenseRank() db.Expression                     { return Func("DENSE_RANK") }
func PercentRank() db.Expression                   { return Func("PERCENT_RANK") }
func CumeDist() db.Expression                      { return Func("CUME_DIST") }
func NTile(buckets int) db.Expression              { return Func("NTILE", Value(buckets)) }
func FirstValue(value db.Expression) db.Expression { return Func("FIRST_VALUE", value) }
func LastValue(value db.Expression) db.Expression  { return Func("LAST_VALUE", value) }
func NthValue(value db.Expression, position int) db.Expression {
	return Func("NTH_VALUE", value, Value(position))
}

// Lag and Lead accept an optional Value containing a positive 32-bit integer
// offset, then an optional default expression. With neither supplied, the
// native defaults are one row and NULL. Dynamic or non-positive offsets are
// intentionally unsupported in this constructor contract.
func Lag(value db.Expression, arguments ...db.Expression) db.Expression {
	return Func("LAG", append([]db.Expression{value}, arguments...)...)
}
func Lead(value db.Expression, arguments ...db.Expression) db.Expression {
	return Func("LEAD", append([]db.Expression{value}, arguments...)...)
}
