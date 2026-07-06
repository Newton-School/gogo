package orm

import (
	"testing"

	"github.com/Newton-School/gogo/orm/dialects/postgres"
)

func TestJoinPlannerSupportsRelationKinds(t *testing.T) {
	planner := NewJoinPlanner(postgres.New()).
		Register(RelationMeta{Path: "author", SourceTable: "blog_post", TargetTable: "auth_user", SourceColumn: "author_id", TargetColumn: "id", Type: RelationForeignKey}).
		Register(RelationMeta{Path: "profile", SourceTable: "auth_user", TargetTable: "account_profile", SourceColumn: "id", TargetColumn: "user_id", Type: RelationOneToOne, Reverse: true}).
		Register(RelationMeta{Path: "comments", SourceTable: "blog_post", TargetTable: "blog_comment", SourceColumn: "id", TargetColumn: "post_id", Type: RelationReverse}).
		Register(RelationMeta{Path: "tags", SourceTable: "blog_post", TargetTable: "blog_tag", SourceColumn: "id", TargetColumn: "id", Type: RelationManyToMany, ThroughTable: "blog_post_tags", ThroughSourceColumn: "post_id", ThroughTargetColumn: "tag_id"}).
		Register(RelationMeta{Path: "nullable_author", SourceTable: "blog_post", TargetTable: "auth_user", SourceColumn: "nullable_author_id", TargetColumn: "id", Type: RelationForeignKey, Nullable: true})

	joins, err := planner.PlanSelectRelated("author", "profile", "comments", "tags", "nullable_author")
	if err != nil {
		t.Fatalf("PlanSelectRelated() error = %v", err)
	}
	if len(joins) != 6 {
		t.Fatalf("joins = %#v", joins)
	}
	if joins[0].SQL != `INNER JOIN "auth_user" ON "blog_post"."author_id" = "auth_user"."id"` {
		t.Fatalf("author join = %q", joins[0].SQL)
	}
	if joins[3].SQL != `INNER JOIN "blog_post_tags" ON "blog_post"."id" = "blog_post_tags"."post_id"` || joins[4].SQL != `INNER JOIN "blog_tag" ON "blog_post_tags"."tag_id" = "blog_tag"."id"` {
		t.Fatalf("many-to-many joins = %#v", joins[3:5])
	}
	nullable, err := planner.PlanSelectRelated("nullable_author")
	if err != nil {
		t.Fatalf("nullable PlanSelectRelated() error = %v", err)
	}
	if nullable[0].SQL[:9] != "LEFT JOIN" {
		t.Fatalf("nullable join = %q", nullable[0].SQL)
	}
}

func TestJoinPlannerRejectsUnknownAndAmbiguousRelations(t *testing.T) {
	planner := NewJoinPlanner(postgres.New()).
		Register(RelationMeta{Path: "author", SourceTable: "blog_post", TargetTable: "auth_user", SourceColumn: "author_id", TargetColumn: "id", Type: RelationForeignKey})
	if _, err := planner.PlanSelectRelated("missing"); err == nil {
		t.Fatalf("missing relation did not fail")
	}
	if err := planner.ValidateNoAmbiguousPaths([]string{"author", "author"}); err == nil {
		t.Fatalf("duplicate relation path did not fail")
	}
}
