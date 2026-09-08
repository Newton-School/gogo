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
	// Window is the data-only OVER clause when Kind is "window". Args then
	// contains exactly one window-capable function. Nil means an empty OVER().
	Window *WindowSpec
	// Subquery is a resolved scalar SELECT or EXISTS expression. Its predicate
	// already includes the independently supplied inner resource scope.
	Subquery *Subquery
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

// Join is a resolved schema-bound relation join. Path is the public relation path;
// aliases and endpoint fields are validated identifiers, never SQL fragments.
type Join struct {
	Path, Alias, ParentPath  string
	Schema                   models.Schema
	ParentField, TargetField string
	Where                    Predicate
	Inner                    bool
	// Through groups a many-to-many intermediary with its authorized target
	// before joining the parent. Its fields are not public relation paths.
	Through *JoinThrough
}

// JoinThrough describes the schema-bound bridge of a grouped relation join.
// SourceField and TargetField are scalar foreign keys to the parent and target
// endpoints named by Join.ParentField and Join.TargetField. Where is scoped to
// this schema only; Join.Where is scoped independently to the target schema.
type JoinThrough struct {
	Schema                          models.Schema
	Alias, SourceField, TargetField string
	Where                           Predicate
}
