package orm

import (
	"fmt"

	"github.com/Newton-School/gogo/orm/dialects"
)

// RelationKind describes a related-loading relation family.
type RelationKind string

const (
	RelationForeignKey RelationKind = "foreign_key"
	RelationOneToOne   RelationKind = "one_to_one"
	RelationReverse    RelationKind = "reverse"
	RelationManyToMany RelationKind = "many_to_many"
)

// RelationMeta stores join metadata for related loading.
type RelationMeta struct {
	Path                string
	SourceTable         string
	TargetTable         string
	SourceColumn        string
	TargetColumn        string
	Type                RelationKind
	Reverse             bool
	Nullable            bool
	ThroughTable        string
	ThroughSourceColumn string
	ThroughTargetColumn string
}

// JoinClause stores rendered JOIN SQL for a relation path.
type JoinClause struct {
	Path string
	SQL  string
}

// JoinPlanner renders SELECT-related joins from relation metadata.
type JoinPlanner struct {
	dialect   dialects.Dialect
	relations map[string]RelationMeta
}

// NewJoinPlanner creates a relation join planner.
func NewJoinPlanner(dialect dialects.Dialect) *JoinPlanner {
	return &JoinPlanner{dialect: dialect, relations: make(map[string]RelationMeta)}
}

// Register adds relation metadata.
func (p *JoinPlanner) Register(relation RelationMeta) *JoinPlanner {
	p.relations[relation.Path] = relation
	return p
}

// PlanSelectRelated renders joins for selected relation paths.
func (p *JoinPlanner) PlanSelectRelated(paths ...string) ([]JoinClause, error) {
	if err := p.ValidateNoAmbiguousPaths(paths); err != nil {
		return nil, err
	}
	joins := make([]JoinClause, 0, len(paths))
	for _, path := range paths {
		relation, ok := p.relations[path]
		if !ok {
			return nil, fmt.Errorf("%w: unknown relation %s", ErrInvalidQuery, path)
		}
		rendered, err := p.renderRelation(relation)
		if err != nil {
			return nil, err
		}
		joins = append(joins, rendered...)
	}
	return joins, nil
}

// ValidateNoAmbiguousPaths rejects duplicate select-related paths.
func (p *JoinPlanner) ValidateNoAmbiguousPaths(paths []string) error {
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			return fmt.Errorf("%w: ambiguous related path %s", ErrInvalidQuery, path)
		}
		seen[path] = struct{}{}
	}
	return nil
}

func (p *JoinPlanner) renderRelation(relation RelationMeta) ([]JoinClause, error) {
	if relation.Type == RelationManyToMany {
		if relation.ThroughTable == "" || relation.ThroughSourceColumn == "" || relation.ThroughTargetColumn == "" {
			return nil, fmt.Errorf("%w: many-to-many relation %s requires through metadata", ErrInvalidQuery, relation.Path)
		}
		return []JoinClause{
			{Path: relation.Path, SQL: "INNER JOIN " + p.q(relation.ThroughTable) + " ON " + p.qc(relation.SourceTable, relation.SourceColumn) + " = " + p.qc(relation.ThroughTable, relation.ThroughSourceColumn)},
			{Path: relation.Path, SQL: "INNER JOIN " + p.q(relation.TargetTable) + " ON " + p.qc(relation.ThroughTable, relation.ThroughTargetColumn) + " = " + p.qc(relation.TargetTable, relation.TargetColumn)},
		}, nil
	}
	joinType := "INNER JOIN"
	if relation.Nullable {
		joinType = "LEFT JOIN"
	}
	return []JoinClause{{
		Path: relation.Path,
		SQL:  joinType + " " + p.q(relation.TargetTable) + " ON " + p.qc(relation.SourceTable, relation.SourceColumn) + " = " + p.qc(relation.TargetTable, relation.TargetColumn),
	}}, nil
}

func (p *JoinPlanner) q(identifier string) string {
	return p.dialect.QuoteIdent(identifier)
}

func (p *JoinPlanner) qc(table, column string) string {
	return p.dialect.QuoteIdent(table) + "." + p.dialect.QuoteIdent(column)
}
