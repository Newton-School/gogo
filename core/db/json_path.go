package db

// JSONPathKind describes the bound operand for a JSON extraction expression.
// A single decimal component is an array index; other single components are
// object keys. Multiple components use an ordered path of string keys/indices.
type JSONPathKind string

const (
	JSONKey   JSONPathKind = "key"
	JSONIndex JSONPathKind = "index"
	JSONPath  JSONPathKind = "path"
)

// JSONPathDialect supplies extraction syntax for already-compiled operands.
// The right operand is a bound string, signed integer or []string according to
// kind. Text extraction returns SQL text (JSON null becomes SQL NULL); JSON
// extraction preserves a JSON null scalar and a missing path returns SQL NULL.
type JSONPathDialect interface {
	JSONExtract(left, right string, kind JSONPathKind, text bool) (string, error)
}
