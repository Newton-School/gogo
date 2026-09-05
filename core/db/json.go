package db

// JSONLookupDialect supplies JSON containment and key-existence syntax. The
// compiler passes only already-compiled operands (quoted identifiers or bound
// placeholders), never untrusted key strings or JSON source. Implementations
// must reject unknown operations; unsupported connectors must not substitute
// text matching for JSON semantics.
type JSONLookupDialect interface {
	JSONLookup(operation, left, right string) (string, error)
}
