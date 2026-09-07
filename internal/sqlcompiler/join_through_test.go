package sqlcompiler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

type throughDialect struct{ castDialect }

func (throughDialect) SupportsFeature(name string) bool { return name == "grouped_relation_joins" }

func throughQuery() (models.Schema, db.Select) {
	root := models.Schema{AppLabel: "tests", Name: "Parent", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	target := models.Schema{AppLabel: "tests", Name: "Target", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant")}}
	bridge := models.Schema{AppLabel: "tests", Name: "Bridge", Fields: []models.Field{models.BigAutoField("id"), models.IntegerField("tenant"), models.ForeignKeyField("source", models.Relation{Target: root.Key(), OnDelete: models.Cascade}), models.ForeignKeyField("target", models.Relation{Target: target.Key(), OnDelete: models.Cascade})}}
	join := db.Join{Path: "targets", Alias: "j", Schema: target, ParentField: "id", TargetField: "id", Where: db.Predicate{Field: "tenant", Value: 22}, Through: &db.JoinThrough{Schema: bridge, Alias: "b", SourceField: "source", TargetField: "target", Where: db.Predicate{Field: "tenant", Value: 33}}}
	return root, db.Select{Table: root.DBTable(), Alias: "r", Fields: []string{"id", "targets__id"}, Joins: []db.Join{join}, Where: db.Predicate{Field: "tenant", Value: 44}, Projections: []db.Projection{{Alias: "constant", Expression: db.Expression{Kind: "value", Value: 11}}}}
}

func TestGroupedRelationJoinsBindIndependentTargetBridgeAndRootScopes(t *testing.T) {
	root, query := throughQuery()
	statement, args, err := Select(throughDialect{}, root, query)
	if err != nil || !strings.Contains(statement, `LEFT JOIN ("tests_bridge" AS "b" INNER JOIN "tests_target" AS "j" ON "b"."target"="j"."id" AND ("j"."tenant" = $2)) ON "r"."id"="b"."source" AND ("b"."tenant" = $3) WHERE "r"."tenant" = $4`) || !reflect.DeepEqual(args, []any{11, 22, 33, 44}) {
		t.Fatal(statement, args, err)
	}
	query.Joins[0].Inner = true
	if statement, _, err := Select(throughDialect{}, root, query); err != nil || strings.Contains(statement, "LEFT JOIN") {
		t.Fatal(statement, err)
	}
	query.Fields = append(query.Fields, "b__tenant")
	if _, _, err := Select(throughDialect{}, root, query); err == nil {
		t.Fatal("intermediary alias became a public relation path")
	}
}

func TestGroupedRelationJoinsRejectInvalidMetadataBeforeBinding(t *testing.T) {
	tests := map[string]func(*models.Schema, *db.Select){
		"root alias collision":   func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.Alias = "r" },
		"target alias collision": func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.Alias = "j" },
		"future alias collision": func(_ *models.Schema, q *db.Select) {
			copy := q.Joins[0]
			copy.Alias = "b"
			copy.Path = "other"
			copy.Through = nil
			q.Joins = append(q.Joins, copy)
		},
		"invalid alias":             func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.Alias = "b;select" },
		"same endpoint":             func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.TargetField = "source" },
		"missing endpoint":          func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.SourceField = "missing" },
		"non relation bridge field": func(_ *models.Schema, q *db.Select) { q.Joins[0].Through.SourceField = "tenant" },
		"wrong target schema": func(_ *models.Schema, q *db.Select) {
			q.Joins[0].Through.Schema.Fields[2].Relation.Target = "tests.Other"
		},
		"wrong endpoint key": func(_ *models.Schema, q *db.Select) { q.Joins[0].ParentField = "tenant" },
		"composite endpoint": func(_ *models.Schema, q *db.Select) {
			q.Joins[0].Through.Schema.Fields[2].Relation.TargetFields = []string{"id", "tenant"}
		},
		"nonunique endpoint": func(root *models.Schema, q *db.Select) {
			q.Joins[0].ParentField = "tenant"
			q.Joins[0].Through.Schema.Fields[2].Relation.TargetFields = []string{"tenant"}
		},
		"composite type endpoint": func(root *models.Schema, q *db.Select) {
			root.Fields[0] = models.JSONField("id", func(f *models.Field) { f.PrimaryKey = true })
		},
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			root, query := throughQuery()
			modify(&root, &query)
			if _, args, err := Select(throughDialect{}, root, query); err == nil || len(args) != 0 {
				t.Fatal("invalid join compiled/bound", err, args)
			}
		})
	}
	root, query := throughQuery()
	if _, args, err := Select(castDialect{}, root, query); !db.IsCode(err, db.UnsupportedFeature) || len(args) != 0 {
		t.Fatal("provider syntax fallback", err, args)
	}
	// Each scope compiler owns only its declared schema and cannot reference
	// another join or the private bridge alias through a user-supplied path.
	for _, bridge := range []bool{false, true} {
		root, query := throughQuery()
		if bridge {
			query.Joins[0].Through.Where.Field = "targets__tenant"
		} else {
			query.Joins[0].Where.Field = "b__tenant"
		}
		if _, _, err := Select(throughDialect{}, root, query); err == nil {
			t.Fatal("cross-scope field reference compiled")
		}
	}
}
