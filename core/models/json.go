package models

import "database/sql/driver"

type jsonNullValue uint8

// JSONNull is an explicit top-level JSON null. A nil model field means SQL NULL;
// nested JSON nulls remain nil values inside their containing map or slice.
// Use an any or json.RawMessage Go field when both null states are needed.
// In JSON equality lookups, nil and JSONNull both match JSON null; use an
// explicit isnull lookup to match SQL NULL.
const JSONNull jsonNullValue = 0

func (jsonNullValue) MarshalJSON() ([]byte, error) { return []byte("null"), nil }
func (jsonNullValue) Value() (driver.Value, error) { return "null", nil }
func (jsonNullValue) String() string               { return "null" }
