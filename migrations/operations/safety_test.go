package operations

import (
	"errors"
	"testing"

	"github.com/Newton-School/gogo/migrations"
)

func TestMigrationSafetyChecksRequireConfirmation(t *testing.T) {
	ops := []migrations.Operation{
		DeleteModel{Model: migrations.ModelState{AppLabel: "blog", Name: "Post", TableName: "blog_post"}},
		RemoveField{AppLabel: "blog", ModelName: "Post", Field: migrations.FieldState{Name: "title"}},
		AlterField{AppLabel: "blog", ModelName: "Post", OldField: migrations.FieldState{Name: "body", Kind: "text"}, NewField: migrations.FieldState{Name: "body", Kind: "varchar(20)"}},
		RenameField{AppLabel: "blog", ModelName: "Post", OldName: "title", NewName: "headline"},
		RemoveConstraint{AppLabel: "blog", ModelName: "Post", ConstraintName: "uniq_title"},
		AddField{AppLabel: "blog", ModelName: "Post", Field: migrations.FieldState{Name: "slug", Null: false}},
	}
	err := migrations.CheckSafety(ops, migrations.SafetyOptions{NonInteractive: true})
	if !errors.Is(err, migrations.ErrUnsafeMigration) {
		t.Fatalf("CheckSafety() error = %v, want ErrUnsafeMigration", err)
	}
	if err := migrations.CheckSafety(ops, migrations.SafetyOptions{NonInteractive: true, Confirmed: true}); err != nil {
		t.Fatalf("CheckSafety(confirmed) error = %v", err)
	}
}
