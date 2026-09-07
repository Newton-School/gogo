package postgres

import (
	"context"
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestConstraintLifecycleMetadataGuardsBeforeStorage(t *testing.T) {
	base := models.Schema{AppLabel: "tests", Name: "Guarded", Fields: []models.Field{models.BigAutoField("id"), models.TextField("title", models.WithDBIndex(true))}}
	for _, mode := range []string{"check_condition", "check_deferred", "check_nulls", "unique_expression", "unstored", "implicit_collision"} {
		t.Run(mode, func(t *testing.T) {
			schema := base.Clone()
			value := models.Constraint{Name: "rule", Kind: "check", Expression: "title <> ''"}
			switch mode {
			case "check_condition":
				value.Condition = "id > 0"
			case "check_deferred":
				value.Deferrable = true
			case "check_nulls":
				value.NullsDistinct = new(bool)
			case "unique_expression":
				value.Kind, value.Fields = "unique", []string{"title"}
			case "unstored":
				schema.Fields = append(schema.Fields, models.ManyToManyField("targets", models.Relation{Target: "tests.Target"}))
				value.Kind, value.Expression, value.Fields = "unique", "", []string{"targets"}
			case "implicit_collision":
				index, err := implicitFieldIndex(schema, schema.Fields[1])
				if err != nil {
					t.Fatal(err)
				}
				value.Name, value.Kind, value.Expression, value.Fields = index.name, "unique", "", []string{"title"}
			}
			recorder := &indexRecorder{}
			if err := (SchemaEditor{}).AddConstraint(context.Background(), recorder, schema, value); err == nil || len(recorder.statements) != 0 {
				t.Fatal("invalid metadata reached DDL", err, recorder.statements)
			}
		})
	}
}

func TestConstraintLifecycleNonmanagedAndCancellation(t *testing.T) {
	for _, mode := range []string{"unmanaged", "proxy", "abstract"} {
		t.Run(mode, func(t *testing.T) {
			schema := models.Schema{AppLabel: "tests", Name: "NoStorage", Fields: []models.Field{models.BigAutoField("id")}}
			schema.Unmanaged, schema.Proxy, schema.Abstract = mode == "unmanaged", mode == "proxy", mode == "abstract"
			value := models.Constraint{Name: "positive", Kind: "check", Expression: "id > 0"}
			editor := SchemaEditor{}
			if err := editor.AddConstraint(context.Background(), nil, schema, value); err != nil {
				t.Fatal(err)
			}
			if err := editor.RemoveConstraint(context.Background(), nil, schema, value); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := editor.AddConstraint(ctx, nil, schema, value); err != context.Canceled {
				t.Fatal(err)
			}
			if err := editor.RemoveConstraint(ctx, nil, schema, value); err != context.Canceled {
				t.Fatal(err)
			}
		})
	}
}
