package orm

import "github.com/Newton-School/gogo/core/db"

// JSONPath extracts a JSON value using literal key/index components. Unlike a
// double-underscore lookup string, components can themselves contain "__" or
// collide with a lookup name. A missing path returns SQL NULL, while an existing
// JSON null remains JSONNull. A single numeric component selects an array index.
func JSONPath(field string, components ...string) db.Expression {
	return db.Expression{Kind: "json_path", Args: []db.Expression{F(field)}, Value: append([]string(nil), components...)}
}

// JSONTextPath extracts SQL text; both a missing path and a JSON null become
// SQL NULL. Use JSONPath when these two states must remain distinguishable.
func JSONTextPath(field string, components ...string) db.Expression {
	expression := JSONPath(field, components...)
	expression.Kind = "json_text_path"
	return expression
}
