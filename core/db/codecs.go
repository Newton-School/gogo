package db

import "github.com/Newton-School/gogo/core/models"

// FieldValueDecoder optionally normalizes a driver's database value into a
// portable Go value for a known model field. It is implemented by a Dialect,
// so Backend decorators that forward Dialect retain value conversion. Decode
// is pure: no connection access, model mutation, validation or external I/O.
// Unknown field kinds and already-native values must pass through unchanged.
//
// The ORM invokes it only when the model field has no explicit Codec; custom
// model codecs always receive the original driver value. Raw Executor.Query
// and Rows.Scan retain their driver-defined representation.
type FieldValueDecoder interface {
	DecodeFieldValue(models.Field, any) (any, error)
}
