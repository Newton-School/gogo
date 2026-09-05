package db

// Expression is a public, data-only SQL expression. A connector never needs
// access to Gogo's private query-builder implementation.
type Expression struct {
	Kind     string
	Name     string
	Value    any
	Args     []Expression
	Distinct bool
}
type Predicate struct {
	Field, Lookup string
	Value         any
	Children      []Predicate
	Connector     string
	Negated       bool
	Expression    *Expression
}
type Order struct {
	Field                       string
	Desc, NullsFirst, NullsLast bool
}
type Projection struct {
	Expression Expression
	Alias      string
}
type Select struct {
	Table                                string
	Fields                               []string
	Where                                Predicate
	Order                                []Order
	Limit, Offset                        *int
	Distinct                             bool
	DistinctOn                           []string
	Projections                          []Projection
	GroupBy                              []string
	Having                               Predicate
	ForUpdate, NoWait, SkipLocked, NoKey bool
}
