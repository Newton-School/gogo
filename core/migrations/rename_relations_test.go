package migrations

import (
	"reflect"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestDetectRenamesDerivedRelationReferencesBeforeComparingModels(t *testing.T) {
	target := models.Schema{AppLabel: "tests", Name: "ZTarget", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code", models.UniqueValue)}}
	owner := models.Schema{AppLabel: "tests", Name: "AOwner", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("target", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}), models.ManyToManyField("targets", models.Relation{Target: target.Key(), TargetFields: []string{"code"}, Through: "tests.YLink", ThroughFields: []string{"owner", "target"}})}}
	link := models.Schema{AppLabel: "tests", Name: "YLink", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("owner", models.Relation{Target: owner.Key()}), models.ForeignKeyField("target", models.Relation{Target: target.Key(), TargetFields: []string{"code"}})}}
	before := []models.Schema{owner, link, target}
	after := []models.Schema{owner.Clone(), link.Clone(), target.Clone()}
	after[0].Fields[1].Relation.TargetFields[0] = "key"
	after[0].Fields[2].Relation.TargetFields[0] = "key"
	after[0].Fields[2].Relation.ThroughFields[1] = "endpoint"
	after[1].Fields[2].Name = "endpoint"
	after[1].Fields[2].Relation.TargetFields[0] = "key"
	after[2].Fields[1].Name = "key"
	operations, err := Detect(before, after, DetectOptions{Renames: map[string]string{link.Key() + ".target": "endpoint", target.Key() + ".code": "key"}})
	if err != nil || len(operations) != 2 || operations[0].Kind != "rename_field" || operations[1].Kind != "rename_field" {
		t.Fatal("derived relation metadata emitted spurious alterations", operations, err)
	}
	executor := Executor{Migrations: []Migration{{App: "tests", Name: "0001", Operations: []Operation{CreateModel(target), CreateModel(owner), CreateModel(link)}}, {App: "tests", Name: "0002", Operations: operations, Dependencies: []string{"tests.0001"}}}}
	state, err := executor.State("")
	if err != nil || !reflect.DeepEqual(state, after) {
		t.Fatal("detected relation rename state differs", state, err)
	}
}

func TestRenameBridgeUpdatesExternalThroughFields(t *testing.T) {
	left := models.Schema{AppLabel: "tests", Name: "Left", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("rights", models.Relation{Target: "tests.Right", Through: "tests.Link", ThroughFields: []string{"left", "right"}})}}
	right := models.Schema{AppLabel: "tests", Name: "Right", Fields: []models.Field{models.BigAutoField("id")}}
	link := models.Schema{AppLabel: "tests", Name: "Link", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("left", models.Relation{Target: left.Key()}), models.ForeignKeyField("right", models.Relation{Target: right.Key()})}}
	e := Executor{Migrations: []Migration{{App: "tests", Name: "0001", Operations: []Operation{CreateModel(left), CreateModel(right), CreateModel(link)}}, {App: "tests", Name: "0002", Dependencies: []string{"tests.0001"}, Operations: []Operation{RenameField(link, "left", "owner")}}}}
	state, err := e.State("")
	if err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range state {
		if schema.Key() == left.Key() {
			field, _ := schema.Field("rights")
			if field.Relation.ThroughFields[0] != "owner" {
				t.Errorf("historical bridge endpoint remains %q after rename", field.Relation.ThroughFields[0])
			}
		}
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Errorf("renamed historical registry cannot freeze: %v", err)
	}
	field, _ := left.Field("rights")
	if field.Relation.ThroughFields[0] != "left" {
		t.Fatal("historical rename mutated its input")
	}
}

func TestRenameFieldUpdatesSelfTargetAndThroughIndependently(t *testing.T) {
	schema := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{models.BigAutoField("id"), models.TextField("left"), models.TextField("right"), models.ManyToManyField("related", models.Relation{Target: "tests.Node", TargetFields: []string{"left"}, Through: "tests.Node", ThroughFields: []string{"left", "right"}})}}
	next, err := renameFieldState(schema, "left", "owner")
	if err != nil {
		t.Fatal(err)
	}
	field, _ := next.Field("related")
	if field.Relation.TargetFields[0] != "owner" || field.Relation.ThroughFields[0] != "owner" || field.Relation.ThroughFields[1] != "right" {
		t.Fatal("self target and bridge references were not updated independently", field.Relation)
	}
	before, _ := schema.Field("related")
	if before.Relation.TargetFields[0] != "left" || before.Relation.ThroughFields[0] != "left" {
		t.Fatal("self reference update mutated its input")
	}
}
