package postgres_test

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func TestPostgresDetectCreatesReferencedModelBeforeLexicallyEarlierSource(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "ZParent", Fields: []models.Field{models.BigAutoField("id")}}
	source := models.Schema{AppLabel: "tests", Name: "AChild", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: target.Key()})}}
	operations, err := migrations.Detect(nil, []models.Schema{source, target}, migrations.DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal("lexical model order created the referencing table before its target", err)
	}
	if err := e.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal("reverse did not remove referencing table before its target", err)
	}
}

func TestPostgresDetectAddsUniqueTargetBeforeLexicallyEarlierForeignKey(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "ZParent", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code")}}
	source := models.Schema{AppLabel: "tests", Name: "AChild", Fields: []models.Field{models.BigAutoField("id")}}
	afterTarget, afterSource := target.Clone(), source.Clone()
	afterTarget.Constraints = []models.Constraint{{Name: "unique_parent_code", Kind: "unique", Fields: []string{"code"}}}
	afterSource.Fields = append(afterSource.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}, models.Nullable))
	operations, err := migrations.Detect([]models.Schema{source, target}, []models.Schema{afterSource, afterTarget}, migrations.DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, change.Key()); err != nil {
		t.Fatal("foreign key preceded required target uniqueness", err)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal("reverse removed target uniqueness before the foreign key", err)
	}
}

func TestPostgresDetectRemovesReferencingTableBeforeLexicallyEarlierTarget(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "AParent", Fields: []models.Field{models.BigAutoField("id")}}
	source := models.Schema{AppLabel: "tests", Name: "ZChild", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: target.Key()})}}
	operations, err := migrations.Detect([]models.Schema{target, source}, nil, migrations.DetectOptions{AllowRemoveModels: map[string]bool{target.Key(): true, source.Key(): true}})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}
	remove := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, remove}}
	if err := e.Apply(ctx, remove.Key()); err != nil {
		t.Fatal("target table removal preceded referencing table removal", err)
	}
}

func TestPostgresDetectRemovesForeignKeyBeforeLexicallyEarlierTargetUniqueness(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "AParent", Fields: []models.Field{models.BigAutoField("id"), models.TextField("code")}, Constraints: []models.Constraint{{Name: "parent_code", Kind: "unique", Fields: []string{"code"}}}}
	source := models.Schema{AppLabel: "tests", Name: "ZChild", Fields: []models.Field{models.BigAutoField("id"), models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"code"}})}}
	afterTarget, afterSource := target.Clone(), source.Clone()
	afterTarget.Constraints = nil
	afterSource.Fields = afterSource.Fields[:1]
	operations, err := migrations.Detect([]models.Schema{target, source}, []models.Schema{afterTarget, afterSource}, migrations.DetectOptions{AllowRemoveFields: map[string]bool{source.Key() + ".parent": true}})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}
	remove := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, remove}}
	if err := e.Apply(ctx, remove.Key()); err != nil {
		t.Fatal("target uniqueness removal preceded source foreign key removal", err)
	}
}

func TestPostgresDetectSelfForeignKeyCreatesAndReverses(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	model := models.Schema{AppLabel: "tests", Name: "SelfNode", Fields: []models.Field{models.BigAutoField("id")}}
	model.Fields = append(model.Fields, models.ForeignKeyField("parent", models.Relation{Target: model.Key()}, models.Nullable))
	operations, err := migrations.Detect(nil, []models.Schema{model}, migrations.DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial}}
	if err := e.Apply(ctx, initial.Key()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, `INSERT INTO tests_selfnode(parent) VALUES (NULL),(1)`); err != nil {
		t.Fatal("self FK was not created in one table statement", err)
	}
	if err := e.Reverse(ctx, "tests.zero"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDetectNewTargetColumnAndUniqueIndexReverseInDependencyOrder(t *testing.T) {
	b := openTest(t)
	ctx := context.Background()
	target := models.Schema{AppLabel: "tests", Name: "ZParent", Fields: []models.Field{models.BigAutoField("id")}}
	source := models.Schema{AppLabel: "tests", Name: "AChild", Fields: []models.Field{models.BigAutoField("id")}}
	afterTarget, afterSource := target.Clone(), source.Clone()
	afterTarget.Fields = append(afterTarget.Fields, models.TextField("code", models.Nullable))
	afterTarget.Indexes = []models.Index{{Name: "parent_code", Fields: []string{"code"}, Unique: true}}
	afterSource.Fields = append(afterSource.Fields, models.ForeignKeyField("parent", models.Relation{Target: target.Key(), TargetFields: []string{"code"}}, models.Nullable))
	operations, err := migrations.Detect([]models.Schema{source, target}, []models.Schema{afterSource, afterTarget}, migrations.DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initial := migrations.Migration{App: "tests", Name: "0001", Operations: []migrations.Operation{migrations.CreateModel(target), migrations.CreateModel(source)}}
	change := migrations.Migration{App: "tests", Name: "0002", Dependencies: []string{initial.Key()}, Operations: operations}
	e := migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: []migrations.Migration{initial, change}}
	if err := e.Apply(ctx, change.Key()); err != nil {
		t.Fatal("new target column/index did not precede FK", err)
	}
	if err := e.Reverse(ctx, initial.Key()); err != nil {
		t.Fatal("reverse dropped referenced column/index too early", err)
	}
}
