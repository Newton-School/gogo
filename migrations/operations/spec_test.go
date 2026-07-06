package operations

import (
	"testing"

	"github.com/Newton-School/gogo/migrations"
	"github.com/Newton-School/gogo/models"
)

func TestOperationSpecRoundTripsDatabaseDefault(t *testing.T) {
	defaultValue := models.DefaultSQL("now()")
	spec := migrations.OperationSpecFor(AddField{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Field: migrations.FieldState{
			Name:      "created_at",
			Column:    "created_at",
			Kind:      "timestamp",
			DBDefault: &defaultValue,
		},
		HasDefault: true,
	})

	data, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	decoded, err := migrations.OperationSpecFromJSON(data)
	if err != nil {
		t.Fatalf("OperationSpecFromJSON() error = %v", err)
	}
	if decoded.Field == nil || decoded.Field.DBDefault == nil || decoded.Field.DBDefault.Kind != models.DefaultExpression || decoded.Field.DBDefault.SQL != "now()" {
		t.Fatalf("decoded default = %#v", decoded.Field)
	}
}

func TestOperationSpecRoundTripsRichIndexAndConstraintMetadata(t *testing.T) {
	spec := migrations.OperationSpecFor(AddIndex{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Index: migrations.IndexState{
			Name:         "idx_blog_post_title",
			Fields:       []string{"title"},
			Unique:       true,
			Expressions:  []string{"LOWER(title)"},
			Method:       "gin",
			OpClasses:    []string{"gin_trgm_ops"},
			Include:      []string{"id"},
			ConditionSQL: "deleted_at IS NULL",
			Concurrently: true,
			Source:       "model",
		},
	})
	data, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON(index) error = %v", err)
	}
	decoded, err := migrations.OperationSpecFromJSON(data)
	if err != nil {
		t.Fatalf("OperationSpecFromJSON(index) error = %v", err)
	}
	if decoded.Index == nil || decoded.Index.Method != "gin" || decoded.Index.ConditionSQL == "" || !decoded.Index.Concurrently || !decoded.Index.Unique || decoded.Index.Expressions[0] != "LOWER(title)" {
		t.Fatalf("decoded rich index = %#v", decoded.Index)
	}

	constraintSpec := migrations.OperationSpecFor(AddConstraint{
		AppLabel:  "blog",
		ModelName: "Post",
		TableName: "blog_post",
		Constraint: migrations.ConstraintState{
			Name:              "fk_blog_post_author",
			Type:              "foreign_key",
			Fields:            []string{"author_id"},
			ReferencesTable:   "auth_user",
			ReferencesColumns: []string{"id"},
			OnDelete:          "CASCADE",
			Deferrable:        true,
			InitiallyDeferred: true,
		},
	})
	data, err = constraintSpec.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON(constraint) error = %v", err)
	}
	decoded, err = migrations.OperationSpecFromJSON(data)
	if err != nil {
		t.Fatalf("OperationSpecFromJSON(constraint) error = %v", err)
	}
	if decoded.Constraint == nil || decoded.Constraint.ReferencesTable != "auth_user" || decoded.Constraint.OnDelete != "CASCADE" || !decoded.Constraint.Deferrable || !decoded.Constraint.InitiallyDeferred {
		t.Fatalf("decoded rich constraint = %#v", decoded.Constraint)
	}
}
