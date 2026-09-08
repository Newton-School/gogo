package db

import (
	"context"

	"github.com/Newton-School/gogo/core/models"
)

// CatalogIntrospector is an optional read-only connector contract for inspectdb.
// InspectCatalog must use one consistent catalog snapshot and return no partial
// result on error. It must not read application rows, run DDL, adopt migration
// history, or change the connection's schema search path.
type CatalogIntrospector interface {
	InspectCatalog(context.Context, Backend, CatalogOptions) (Catalog, error)
}

type CatalogOptions struct {
	// Empty Schema selects the connection's current schema. Relation names are
	// exact catalog names; an empty list selects all ordinary tables and,
	// when IncludeViews is true, views in that schema.
	Schema       string
	Relations    []string
	IncludeViews bool
}

// Catalog preserves observed database names and definitions independently of
// which features the portable model descriptor can represent. Consumers must
// not silently turn unsupported catalog metadata into a different model.
type Catalog struct {
	Schema    string
	Relations []CatalogRelation
}

type CatalogRelation struct {
	Name, Kind, Comment, Tablespace string
	Columns                         []CatalogColumn
	Constraints                     []CatalogConstraint
	Indexes                         []CatalogIndex
}

type CatalogColumn struct {
	Name, DatabaseType, TypeSchema, TypeName string
	// Field contains the connector's portable type mapping. Custom without a
	// codec represents an unsupported type, never a fallback to text.
	Field models.Field
	// Collation is a fully qualified, connector-quoted SQL identifier. It must
	// not be split on dots, since a quoted component may itself contain a dot.
	Identity, Generated, Collation string
}

type CatalogConstraint struct {
	Name, Kind, Definition, Expression       string
	Columns                                  []string
	ReferencedSchema, ReferencedTable        string
	ReferencedColumns                        []string
	OnDelete, OnUpdate                       string
	Deferrable, InitiallyDeferred, Validated bool
}

type CatalogIndex struct {
	Name, Method, Definition, Condition, Constraint string
	// OpClasses and Collations use fully qualified connector-quoted names.
	Columns, Expressions, Include, OpClasses, Collations []string
	// Options preserves per-key ordering/null-order flags from the connector;
	// a source generator must reject options it cannot represent exactly.
	Options                                      []int
	DefaultOpClasses                             []bool
	Unique, Primary, Valid, Ready, NullsDistinct bool
}
