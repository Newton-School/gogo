package postgres

import "github.com/Newton-School/gogo/core/db"

var _ db.JSONPathDialect = Dialect{}

func (Dialect) JSONExtract(left, right string, kind db.JSONPathKind, text bool) (string, error) {
	operator, cast := "->", ""
	switch kind {
	case db.JSONKey:
		cast = "text"
	case db.JSONIndex:
		cast = "integer"
	case db.JSONPath:
		operator, cast = "#>", "text[]"
	default:
		return "", &db.Error{Code: db.UnsupportedFeature, Message: "Unsupported PostgreSQL JSON extraction"}
	}
	if text {
		operator += ">"
	}
	return "(" + left + " " + operator + " CAST(" + right + " AS " + cast + "))", nil
}
