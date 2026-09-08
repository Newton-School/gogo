package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestSchemaTransitionDropsOnlyDerivedAutomaticBridges(t *testing.T) {
	source := models.Schema{AppLabel: "tests", Name: "Source", Fields: []models.Field{models.BigAutoField("id")}}
	target := models.Schema{AppLabel: "tests", Name: "Target", Fields: []models.Field{models.BigAutoField("id")}}
	bridge := models.Schema{AppLabel: "tests", Name: "Manual", AutoCreatedBy: source.Key(), AutoCreatedField: "items", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("source", models.Relation{Target: source.Key()})}}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "unproven metadata", true: "explicit through"}[explicit], func(t *testing.T) {
			before := source.Clone()
			if explicit {
				before.Fields = append(before.Fields, models.ManyToManyField("items", models.Relation{Target: target.Key(), Through: bridge.Key()}))
			}
			editor, err := (SchemaEditor{}).WithSchemaTransition([]models.Schema{before, target, bridge}, []models.Schema{target, bridge})
			if err != nil {
				t.Fatal(err)
			}
			recorder := &indexRecorder{}
			if err := editor.DeleteModel(context.Background(), recorder, before); err != nil || len(recorder.statements) != 1 || strings.Contains(recorder.statements[0], bridge.DBTable()) {
				t.Fatal("unproven bridge was adopted", recorder.statements, err)
			}
		})
	}
}

func TestSchemaTransitionClonesSnapshotsAndIsolatesDropState(t *testing.T) {
	target := models.Schema{AppLabel: "tests", Name: "Target", Fields: []models.Field{models.BigAutoField("id")}}
	source := models.Schema{AppLabel: "tests", Name: "Source", Fields: []models.Field{models.BigAutoField("id"), models.ManyToManyField("items", models.Relation{Target: target.Key()})}}
	before := []models.Schema{source, target}
	base := SchemaEditor{}
	first, err := base.WithSchemaTransition(before, []models.Schema{target})
	if err != nil {
		t.Fatal(err)
	}
	second, err := base.WithSchemaTransition(before, []models.Schema{target})
	if err != nil {
		t.Fatal(err)
	}
	source.Fields[1].Relation.Target = "mutated.Target"
	var observed [][]string
	for _, editor := range []SchemaEditor{first.(SchemaEditor), second.(SchemaEditor)} {
		recorder := &indexRecorder{}
		if err := editor.DeleteModel(context.Background(), recorder, before[0]); err != nil || len(recorder.statements) != 2 {
			t.Fatal(recorder.statements, err)
		}
		observed = append(observed, recorder.statements)
	}
	if !reflect.DeepEqual(observed[0], observed[1]) || base.schemas != nil || base.dropped != nil {
		t.Fatal("migration editors shared lifecycle state", observed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := first.DeleteModel(ctx, nil, before[0]); err != context.Canceled {
		t.Fatal(err)
	}
}
