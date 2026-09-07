package migrations

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func TestConstraintsDetectExactHistoricalStateAndOrder(t *testing.T) {
	a := models.Constraint{Name: "unique_title", Kind: "unique", Fields: []string{"title"}}
	b := models.Constraint{Name: "nonempty_title", Kind: "check", Fields: []string{"title"}, Expression: "title <> ''"}
	c := models.Constraint{Name: "positive_id", Kind: "check", Expression: "id > 0"}
	for _, mode := range []string{"add_before", "remove", "remove_all", "replace", "reorder", "field_dependency"} {
		t.Run(mode, func(t *testing.T) {
			before := testSchema()
			before.Constraints = []models.Constraint{a, c}
			after := before.Clone()
			switch mode {
			case "add_before":
				after.Constraints = []models.Constraint{b, a, c}
			case "remove":
				after.Constraints = []models.Constraint{c}
			case "remove_all":
				after.Constraints = nil
			case "replace":
				after.Constraints[0].Deferrable = true
			case "reorder":
				after.Constraints = []models.Constraint{c, a}
			case "field_dependency":
				after.Fields = append(after.Fields, models.TextField("note", models.Nullable))
				after.Constraints[0].Fields = []string{"note"}
			}
			operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{})
			if err != nil || len(operations) == 0 {
				t.Fatal("constraint drift was ignored", operations, err)
			}
			if mode == "field_dependency" && (operations[0].Kind != "remove_constraint" || operations[1].Kind != "add_field" || operations[2].Kind != "add_constraint") {
				t.Fatal("constraint dependency order", operations)
			}
			if mode == "reorder" && (len(operations) != 1 || operations[0].Kind != "constraint_order") {
				t.Fatal("metadata order caused DDL", operations)
			}
			change := Migration{App: "shop", Name: "0002", Dependencies: []string{"shop.0001"}, Operations: operations}
			encoded, err := json.Marshal(change)
			if err != nil {
				t.Fatal(err)
			}
			e := Executor{Migrations: []Migration{{App: "shop", Name: "0001", Operations: []Operation{CreateModel(before)}}, MustDecode(string(encoded))}}
			state, err := e.State("")
			if err != nil || len(state) != 1 || !reflect.DeepEqual(state[0], after) {
				t.Fatal("constraint state differs from declared target", state, err)
			}
			if again, err := Detect(state, []models.Schema{after}, DetectOptions{}); err != nil || len(again) != 0 {
				t.Fatal("constraint history drifted", again, err)
			}
		})
	}
}

func TestConstraintSnapshotsPreserveLegacyChecksumsAndCallerIsolation(t *testing.T) {
	distinct := false
	value := models.Constraint{Name: "unique_title", Kind: "unique", Fields: []string{"title"}, NullsDistinct: &distinct}
	operation := AddConstraint(testSchema(), value)
	value.Fields[0], distinct = "mutated", true
	if operation.Constraint.Fields[0] != "title" || *operation.Constraint.NullsDistinct {
		t.Fatal("constraint constructor retained mutable metadata")
	}
	legacy, err := json.Marshal(CreateModel(testSchema()))
	if err != nil || strings.Contains(string(legacy), `"Constraint":`) || strings.Contains(string(legacy), "ConstraintOrder") {
		t.Fatal("optional constraint metadata changed old checksum format", string(legacy), err)
	}
	legacyConstraint := models.Constraint{Name: "check_title", Kind: "check", Expression: "title <> ''", Fields: []string{}}
	if AddConstraint(testSchema(), legacyConstraint).Constraint.Fields == nil {
		t.Fatal("constructor collapsed empty historical field slice")
	}
}

type safeConstraintIOEditor struct{ noIndexIOEditor }
type droppingConstraintIOEditor struct{ safeConstraintIOEditor }

func (droppingConstraintIOEditor) WithSchemas([]models.Schema) (db.SchemaEditor, error) {
	return noIndexIOEditor{}, nil
}

func (safeConstraintIOEditor) AddConstraint(context.Context, db.Executor, models.Schema, models.Constraint) error {
	panic("constraint preflight reached storage")
}
func (safeConstraintIOEditor) RemoveConstraint(context.Context, db.Executor, models.Schema, models.Constraint) error {
	panic("constraint preflight reached storage")
}

func TestConstraintPreflightRejectsUnsupportedModesBeforeAnyIO(t *testing.T) {
	for _, mode := range []string{"provider", "resolved_provider", "non_atomic", "missing_descriptor"} {
		for _, reverse := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "_forward", true: "_reverse"}[reverse], func(t *testing.T) {
				constraint := models.Constraint{Name: "unique_title", Kind: "unique", Fields: []string{"title"}}
				migration := Migration{App: "shop", Name: "0001", Operations: []Operation{AddConstraint(testSchema(), constraint)}}
				e := Executor{Backend: noIndexIOBackend{}, Editor: safeConstraintIOEditor{}}
				switch mode {
				case "provider":
					e.Editor = noIndexIOEditor{}
				case "resolved_provider":
					e.Editor = droppingConstraintIOEditor{}
					migration.Operations = append([]Operation{CreateModel(testSchema())}, migration.Operations...)
				case "non_atomic":
					migration.NonAtomic = true
				case "missing_descriptor":
					migration.Operations[0].Constraint = nil
				}
				e.Migrations = []Migration{migration}
				if statements, err := e.SQL(context.Background(), migration.Key(), reverse); err == nil || len(statements) != 0 {
					t.Fatal("unsafe constraint mode produced preview", statements, err)
				}
				if !reverse {
					if err := e.Apply(context.Background(), migration.Key()); err == nil {
						t.Fatal("unsafe constraint mode reached storage")
					}
				}
			})
		}
	}
}

func TestConstraintUnsupportedMetadataIsNeverSilentlyIgnored(t *testing.T) {
	for _, mode := range []string{"unique_expression", "check_condition", "check_deferred", "check_nulls", "index_collision"} {
		t.Run(mode, func(t *testing.T) {
			before, after := testSchema(), testSchema()
			value := models.Constraint{Name: "rule", Kind: "check", Expression: "title <> ''"}
			switch mode {
			case "unique_expression":
				value.Kind, value.Fields = "unique", []string{"title"}
			case "check_condition":
				value.Condition = "id > 0"
			case "check_deferred":
				value.Deferrable = true
			case "check_nulls":
				value.NullsDistinct = new(bool)
			case "index_collision":
				value.Kind, value.Expression, value.Fields = "unique", "", []string{"title"}
				after.Indexes = []models.Index{{Name: value.Name, Fields: []string{"title"}}}
			}
			after.Constraints = []models.Constraint{value}
			if operations, err := Detect([]models.Schema{before}, []models.Schema{after}, DetectOptions{}); err == nil || len(operations) != 0 {
				t.Fatal("unsupported constraint options silently ignored", operations, err)
			}
		})
	}
}
