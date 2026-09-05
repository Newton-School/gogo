package postgres

import "github.com/Newton-School/gogo/core/db"

var _ db.JSONLookupDialect = Dialect{}

func (Dialect) JSONLookup(operation, left, right string) (string, error) {
	operator, cast := "", ""
	switch operation {
	case "contains":
		operator, cast = "@>", "jsonb"
	case "contained_by":
		operator, cast = "<@", "jsonb"
	case "has_key":
		operator, cast = "?", "text"
	case "has_keys":
		operator, cast = "?&", "text[]"
	case "has_any_keys":
		operator, cast = "?|", "text[]"
	default:
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Unsupported PostgreSQL JSON lookup"}
	}
	return left + " " + operator + " CAST(" + right + " AS " + cast + ")", nil
}
