package db

import "github.com/Newton-School/gogo/core/models"

// CastDialect explicitly advertises database conversion expression support.
// operand is compiled SQL, never a raw client value; output is model metadata.
// Providers must resolve output through a closed type mapping and return
// UnsupportedFeature for unsupported conversions, not interpolate field names
// or arbitrary type fragments. Storage types alone do not imply CAST support.
type CastDialect interface {
	CastExpression(operand string, output models.Field) (string, error)
}
