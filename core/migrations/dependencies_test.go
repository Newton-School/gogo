package migrations

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func dependencySchemas() (models.Schema, models.Schema) {
	target := models.Schema{AppLabel: "tests", Name: "ZParent", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code")}}
	source := models.Schema{AppLabel: "tests", Name: "AChild", Fields: []models.Field{models.BigAutoField("id")}}
	return target, source
}

func TestDetectOperationDependenciesHaveStableReadyOrderAndExactState(t *testing.T) {
	target, source := dependencySchemas()
	source.Fields = append(source.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key()}))
	independent := models.Schema{AppLabel: "tests", Name: "BIndependent", Fields: []models.Field{models.BigAutoField("id")}}
	want := []string{independent.Key(), target.Key(), source.Key()}
	var fingerprint string
	for i := 0; i < 30; i++ {
		input := []models.Schema{source, independent, target}
		if i%2 == 0 {
			input[0], input[2] = input[2], input[0]
		}
		operations, err := Detect(nil, input, DetectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, operation := range operations {
			got = append(got, operation.Schema.Key())
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("unstable ready-operation order", got)
		}
		encoded, err := json.Marshal(operations)
		if err != nil || fingerprint != "" && fingerprint != string(encoded) {
			t.Fatal("dependency ordering changed encoded migration", err)
		}
		fingerprint = string(encoded)
		e := Executor{Migrations: []Migration{{App: "tests", Name: "0001", Operations: operations}}}
		state, err := e.State("")
		if err != nil || !reflect.DeepEqual(state, []models.Schema{source, independent, target}) {
			t.Fatal("operation ordering changed historical schemas", state, err)
		}
	}
}

func TestDetectSelfReferencesAreNotDependencyCycles(t *testing.T) {
	model := models.Schema{AppLabel: "tests", Name: "Node", Fields: []models.Field{models.BigAutoField("id")}}
	model.Fields = append(model.Fields, models.ForeignKeyField("parent", models.Relation{Target: model.Key()}, models.Nullable))
	if operations, err := Detect(nil, []models.Schema{model}, DetectOptions{}); err != nil || len(operations) != 1 {
		t.Fatal("self reference became a false dependency cycle", operations, err)
	}
	if operations, err := Detect([]models.Schema{model}, nil, DetectOptions{AllowRemoveModels: map[string]bool{model.Key(): true}}); err != nil || len(operations) != 1 {
		t.Fatal("self-referencing table cannot be removed", operations, err)
	}
}

func TestDetectCyclesFailBeforeReturningAnyExecutableOperations(t *testing.T) {
	for _, remove := range []bool{false, true} {
		left, right := dependencySchemas()
		left.Fields = append(left.Fields, models.ForeignKeyField("right", models.Relation{Target: right.Key()}))
		right.Fields = append(right.Fields, models.ForeignKeyField("left", models.Relation{Target: left.Key()}))
		var operations []Operation
		var err error
		if remove {
			operations, err = Detect([]models.Schema{left, right}, nil, DetectOptions{AllowRemoveModels: map[string]bool{left.Key(): true, right.Key(): true}})
		} else {
			operations, err = Detect(nil, []models.Schema{left, right}, DetectOptions{})
		}
		if err == nil || !strings.Contains(err.Error(), "dependency cycle") || len(operations) != 0 {
			t.Fatal("cycle produced executable partial plan", operations, err)
		}
	}
}

func TestDetectTargetColumnAndUniqueIndexPrecedeForeignKey(t *testing.T) {
	target, source := dependencySchemas()
	nextTarget, nextSource := target.Clone(), source.Clone()
	nextTarget.Fields = append(nextTarget.Fields, models.TextField("key", models.Nullable))
	nextTarget.Indexes = []models.Index{{Name: "target_key", Fields: []string{"key"}, Unique: true}}
	nextSource.Fields = append(nextSource.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"key"}}, models.Nullable))
	operations, err := Detect([]models.Schema{source, target}, []models.Schema{nextSource, nextTarget}, DetectOptions{})
	if err != nil || len(operations) != 3 || operations[0].Schema.Key() != target.Key() || operations[0].Kind != "add_field" || operations[1].Kind != "add_index" || operations[2].Schema.Key() != source.Key() {
		t.Fatal("target field and unique index did not precede FK", operations, err)
	}
}

func TestDetectRetainedForeignKeyNeverSilentlyRetargetsRemovedUniqueness(t *testing.T) {
	target, source := dependencySchemas()
	target.Constraints = []models.Constraint{{Name: "parent_code", Kind: "unique", Fields: []string{"code"}}}
	source.Fields = append(source.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}))
	after := target.Clone()
	// Even a replacement unique object over the same fields cannot silently
	// retarget a retained PostgreSQL FK's dependency on the old backing index.
	after.Constraints[0].Name = "renamed_parent_code"
	if operations, err := Detect([]models.Schema{source, target}, []models.Schema{source, after}, DetectOptions{}); err == nil || !strings.Contains(err.Error(), "retains a foreign-key binding") || len(operations) != 0 {
		t.Fatal("retained FK was assumed to retarget", operations, err)
	}
}

func TestDetectUnconstrainedRelationsDoNotInventDatabaseDependencyCycles(t *testing.T) {
	left, right := dependencySchemas()
	left.Fields = append(left.Fields, models.ForeignKeyField("right", models.Relation{Target: right.Key(), NoConstraint: true}))
	right.Fields = append(right.Fields, models.ForeignKeyField("left", models.Relation{Target: left.Key(), NoConstraint: true}))
	if operations, err := Detect(nil, []models.Schema{left, right}, DetectOptions{}); err != nil || len(operations) != 2 {
		t.Fatal("unconstrained relation invented physical dependency", operations, err)
	}
}

func TestDetectNoncreatingModelsDoNotInventDatabaseDependencyCycles(t *testing.T) {
	for _, kind := range []string{"unmanaged", "proxy", "abstract"} {
		t.Run(kind, func(t *testing.T) {
			left, right := dependencySchemas()
			left.Unmanaged, left.Proxy, left.Abstract = kind == "unmanaged", kind == "proxy", kind == "abstract"
			left.Fields = append(left.Fields, models.ForeignKeyField("right", models.Relation{Target: right.Key()}))
			right.Fields = append(right.Fields, models.ForeignKeyField("left", models.Relation{Target: left.Key()}))
			if operations, err := Detect(nil, []models.Schema{left, right}, DetectOptions{}); err != nil || len(operations) != 2 {
				t.Fatal("model-creation no-op invented a database cycle", operations, err)
			}
		})
	}
}

func TestDetectNewForeignKeyCannotReferenceRemovedTargetModelOrField(t *testing.T) {
	for _, mode := range []string{"model", "field"} {
		t.Run(mode, func(t *testing.T) {
			target, source := dependencySchemas()
			target.Fields[1].Unique = true
			nextSource := source.Clone()
			relation := models.Relation{Target: target.Key()}
			if mode == "field" {
				relation.TargetFields = []string{"code"}
			}
			nextSource.Fields = append(nextSource.Fields, models.ForeignKeyField("parent", relation, models.Nullable))
			after := []models.Schema{nextSource}
			options := DetectOptions{AllowRemoveModels: map[string]bool{target.Key(): true}, AllowRemoveFields: map[string]bool{target.Key() + ".code": true}}
			if mode == "field" {
				nextTarget := target.Clone()
				nextTarget.Fields = nextTarget.Fields[:1]
				after = append(after, nextTarget)
			}
			if operations, err := Detect([]models.Schema{source, target}, after, options); err == nil || len(operations) != 0 {
				t.Fatal("new FK to removed target produced executable operations", operations, err)
			}
		})
	}
}

func TestDetectNonmanagedUniqueRemovalDoesNotInventPhysicalDrop(t *testing.T) {
	target, source := dependencySchemas()
	target.Unmanaged = true
	target.Constraints = []models.Constraint{{Name: "external_code", Kind: "unique", Fields: []string{"code"}}}
	source.Fields = append(source.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}))
	afterTarget := target.Clone()
	afterTarget.Constraints = nil
	if operations, err := Detect([]models.Schema{target, source}, []models.Schema{afterTarget, source}, DetectOptions{}); err != nil || len(operations) != 1 {
		t.Fatal("nonmanaged key metadata removal invented a storage drop", operations, err)
	}
}

func TestDetectUnmanagedModelDeletionCannotClaimForeignKeyRemoval(t *testing.T) {
	target, source := dependencySchemas()
	source.Unmanaged = true
	source.Fields = append(source.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key()}))
	if operations, err := Detect([]models.Schema{target, source}, nil, DetectOptions{AllowRemoveModels: map[string]bool{target.Key(): true, source.Key(): true}}); err == nil || !strings.Contains(err.Error(), "retains a foreign-key binding") || len(operations) != 0 {
		t.Fatal("no-op unmanaged deletion claimed to remove a native FK", operations, err)
	}
}
