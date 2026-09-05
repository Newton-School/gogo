package orm

import (
	"testing"

	"github.com/Newton-School/gogo/core/models"
)

func TestCastSnapshotsNestedOutputMetadataAndExpression(t *testing.T) {
	element := models.DecimalField("value", 20, 4)
	output := models.Field{Kind: models.Array, Element: &element, Choices: []models.Choice{{Value: []any{"original"}}}}
	expression := JSONTextPath("payload", "amount")
	cast := Cast(expression, output)
	output.Element.MaxDigits = 2
	output.Choices[0].Value.([]any)[0] = "changed"
	expression.Value.([]string)[0] = "changed"
	if cast.Output.Element.MaxDigits != 20 || cast.Output.Choices[0].Value.([]any)[0] != "original" || cast.Args[0].Value.([]string)[0] != "amount" {
		t.Fatal("cast retains caller-owned metadata")
	}
	clone := cloneExpression(cast)
	cast.Output.Element.MaxDigits = 3
	if clone.Output.Element.MaxDigits != 20 {
		t.Fatal("query cloning retains mutable output")
	}
	cycle := models.Field{Kind: models.Array}
	cycle.Element = &cycle
	// Invalid metadata remains representable for fail-closed compiler rejection,
	// rather than overflowing while merely building a public query expression.
	bad := Cast(Value(1), cycle)
	if bad.Output.Element == nil || bad.Output.Element.Element != bad.Output.Element {
		t.Fatal("metadata cycle was not safely snapshotted")
	}
}
