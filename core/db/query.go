package db

import "github.com/Newton-School/gogo/core/models"

// Expression is a public, data-only SQL expression. A connector never needs
// access to Gogo's private query-builder implementation.
type Expression struct {
	Kind     string
	Name     string
	Value    any
	Args     []Expression
	Distinct bool
	// Output declares a schema type for conversion expressions. It is metadata,
	// never a caller-provided SQL type fragment; only the dialect supplies SQL.
	Output *models.Field
	// Filter is a row predicate for an aggregate expression, not a HAVING
	// predicate. Unsupported dialects must reject it before execution.
	Filter *Predicate
	// Branches is the ordered conditional branch list for a CASE expression.
	Branches []WhenBranch
}
type WhenBranch struct {
	Condition Predicate
	Then      Expression
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
	// Output is required for named row aliases; scalar aggregate projections
	// may keep their output metadata in the terminal query owner instead.
	Output *models.Field
}
type Select struct {
	Table         string
	Alias         string
	Joins         []Join
	Fields        []string
	Where         Predicate
	Order         []Order
	Limit, Offset *int
	Distinct      bool
	DistinctOn    []string
	Projections   []Projection
	// Aliases defines typed expressions independently of which are selected.
	// References expand inside SQL expressions, not as unsafe SQL identifiers.
	Aliases                              []Projection
	GroupBy                              []string
	Having                               Predicate
	ForUpdate, NoWait, SkipLocked, NoKey bool
	LockOf                               []string
}

// Join is a resolved schema-bound to-one join. Path is the public relation path;
// aliases and endpoint fields are validated identifiers, never SQL fragments.
type Join struct {
	Path, Alias, ParentPath  string
	Schema                   models.Schema
	ParentField, TargetField string
	Where                    Predicate
	Inner                    bool
}
